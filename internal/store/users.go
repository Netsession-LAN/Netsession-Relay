package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Roles, ordered by power.
const (
	RoleViewer   = "viewer"
	RoleOperator = "operator"
	RoleAdmin    = "admin"
)

// RoleRank orders roles for "at least" checks.
func RoleRank(role string) int {
	switch role {
	case RoleAdmin:
		return 3
	case RoleOperator:
		return 2
	case RoleViewer:
		return 1
	}
	return 0
}

// SessionTTL is the absolute lifetime of a session, for local and OIDC accounts alike: after it the user logs in
// again, and an OIDC account that lost its relay group is out at the latest then (its role is read from the
// database on every request; `nsrelay admin logout <name>` ends its sessions at once).
//
// Accounts are never locked after failed logins (that would let anybody keep the owner out and tell which names
// exist); the web part pauses per client address and name instead. The columns user.failed and user.locked_until
// of the first schema are therefore unused. They are not dropped: migrations never remove a column (see migrate),
// so that an older version, which still reads them, keeps working on a newer database.
const SessionTTL = 12 * time.Hour

// User is an account.
type User struct {
	ID           int64
	Kind, Name   string
	Role         string
	PasswordHash string
}

// SetLocalUser creates or updates a local account with a password hash and role.
func (s *Store) SetLocalUser(ctx context.Context, actor, name, hash, role string) error {
	if RoleRank(role) == 0 {
		return fmt.Errorf("%w: role %q (viewer|operator|admin)", ErrInvalid, role)
	}
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		e := AuditEntry{User: actor, Action: "user.set_password", Object: name, After: role}
		if _, err := tx.ExecContext(ctx, `INSERT INTO user(kind, name, role, password_hash) VALUES ('local', ?, ?, ?)
			ON CONFLICT(name) DO UPDATE SET password_hash = excluded.password_hash, role = excluded.role`,
			name, role, hash); err != nil {
			return e, err
		}
		// a new password or role ends every session of the account
		_, err := tx.ExecContext(ctx, deleteUserSessions, name)
		return e, err
	})
}

// DeleteUserSessions logs an account out everywhere and returns the number of sessions ended.
func (s *Store) DeleteUserSessions(ctx context.Context, name string) (int64, error) {
	res, err := s.db.ExecContext(ctx, deleteUserSessions, name)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

const deleteUserSessions = `DELETE FROM session WHERE user_id IN (SELECT id FROM user WHERE name = ?)`

// Logout ends all sessions of an account (CLI `nsrelay admin logout`) and records it.
func (s *Store) Logout(ctx context.Context, actor, name string) (int64, error) {
	if _, err := s.UserByName(ctx, name); err != nil {
		return 0, err
	}
	var n int64
	err := s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		res, err := tx.ExecContext(ctx, deleteUserSessions, name)
		if err != nil {
			return AuditEntry{}, err
		}
		n, _ = res.RowsAffected()
		return AuditEntry{User: actor, Action: "user.logout", Object: name, After: fmt.Sprint(n)}, nil
	})
	return n, err
}

// UserByName returns a user.
func (s *Store) UserByName(ctx context.Context, name string) (User, error) {
	var u User
	err := s.db.QueryRowContext(ctx, `SELECT id, kind, name, role, password_hash FROM user WHERE name = ?`, name).
		Scan(&u.ID, &u.Kind, &u.Name, &u.Role, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// LoginFailed records a failed attempt in the audit log. The account is not locked (see SessionTTL).
func (s *Store) LoginFailed(ctx context.Context, u User, ip string) error {
	return s.Audit(ctx, u.Name, "login.failed", ip, "", "")
}

// Session is a logged-in browser.
type Session struct {
	ID      string // hash of the cookie value
	User    User
	CSRF    string
	Expires time.Time
}

func hashToken(t string) string {
	sum := sha256.Sum256([]byte(t))
	return hex.EncodeToString(sum[:])
}

// CreateSession stores a session for the cookie value token and records the login.
func (s *Store) CreateSession(ctx context.Context, u User, token, csrf, ip string) error {
	now := s.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		e := AuditEntry{User: u.Name, Action: "login", Object: ip, After: u.Kind}
		if _, err := tx.ExecContext(ctx, `INSERT INTO session(id, user_id, csrf, created, expires, ip) VALUES (?,?,?,?,?,?)`,
			hashToken(token), u.ID, csrf, stamp, now.Add(SessionTTL).Format(time.RFC3339Nano), ip); err != nil {
			return e, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE user SET last_login = ? WHERE id = ?`, stamp, u.ID); err != nil {
			return e, err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM session WHERE expires < ?`, stamp)
		return e, err
	})
}

// SessionByToken returns the valid session for a cookie value.
func (s *Store) SessionByToken(ctx context.Context, token string) (Session, error) {
	var se Session
	var exp string
	err := s.db.QueryRowContext(ctx, `SELECT s.id, s.csrf, s.expires, u.id, u.kind, u.name, u.role
		FROM session s JOIN user u ON u.id = s.user_id WHERE s.id = ?`, hashToken(token)).
		Scan(&se.ID, &se.CSRF, &exp, &se.User.ID, &se.User.Kind, &se.User.Name, &se.User.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return se, ErrNotFound
	}
	if err != nil {
		return se, err
	}
	se.Expires, _ = time.Parse(time.RFC3339Nano, exp)
	if !se.Expires.After(s.Now()) {
		return se, ErrNotFound
	}
	return se, nil
}

// DeleteSession logs a browser out.
func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM session WHERE id = ?`, hashToken(token))
	return err
}

// UpsertOIDCUser creates or refreshes the account of a single sign-on (OIDC) subject. Name and role follow the claims at
// every login. A local account with the same name keeps its name; the OIDC account then gets "<name>@sso".
func (s *Store) UpsertOIDCUser(ctx context.Context, subject, name, email, role string) (User, error) {
	if subject == "" || RoleRank(role) == 0 {
		return User{}, fmt.Errorf("%w: subject or role", ErrInvalid)
	}
	name = oidcName(subject, name, email)
	var u User
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var id int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM user WHERE kind = 'oidc' AND subject = ?`, subject).Scan(&id)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		// a different account (local, or another subject) holds the name: use name@sso
		var other int64
		if e := tx.QueryRowContext(ctx, `SELECT id FROM user WHERE name = ? AND id != ?`, name, id).Scan(&other); e == nil {
			name += "@sso"
		}
		if id == 0 {
			id, err = insertOIDCUser(ctx, tx, subject, name, email, role)
		} else {
			err = updateOIDCUser(ctx, tx, id, name, email, role)
		}
		if err != nil {
			return err
		}
		u = User{ID: id, Kind: "oidc", Name: name, Role: role}
		return nil
	})
	return u, err
}

// oidcName is the account name of an OIDC subject: the name claim, else the e-mail address, else "sso-" and the
// start of the subject.
func oidcName(subject, name, email string) string {
	if name == "" {
		name = email
	}
	if name == "" {
		name = "sso-" + subject[:min(8, len(subject))]
	}
	return name
}

func insertOIDCUser(ctx context.Context, tx *sql.Tx, subject, name, email, role string) (int64, error) {
	res, err := tx.ExecContext(ctx, `INSERT INTO user(kind, name, role, subject, email) VALUES ('oidc', ?, ?, ?, ?)`, name, role, subject, email)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// updateOIDCUser refreshes an existing OIDC account; a changed role ends the sessions opened with the old one.
func updateOIDCUser(ctx context.Context, tx *sql.Tx, id int64, name, email, role string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM session WHERE user_id = ? AND (SELECT role FROM user WHERE id = ?) != ?`, id, id, role); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `UPDATE user SET name = ?, role = ?, email = ? WHERE id = ?`, name, role, email, id)
	return err
}

// LocalAccounts counts the local accounts that can log in (with a password).
func (s *Store) LocalAccounts(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM user WHERE kind = 'local' AND password_hash != ''`).Scan(&n)
	return n, err
}

// CreateFirstAdmin creates the admin account of the first-run setup. It only works while no local account
// exists (ErrExists otherwise, also when two browsers race), so the open setup page cannot add or replace
// accounts later.
func (s *Store) CreateFirstAdmin(ctx context.Context, name, hash string) (User, error) {
	var u User
	err := s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		e := AuditEntry{User: name, Action: "user.setup", Object: name, After: RoleAdmin}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM user WHERE kind = 'local' AND password_hash != ''`).Scan(&n); err != nil {
			return e, err
		}
		if n > 0 {
			return e, ErrExists
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM user WHERE name = ?`, name).Scan(&n); err != nil {
			return e, err
		}
		if n > 0 {
			return e, fmt.Errorf("%w: name", ErrInvalid)
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO user(kind, name, role, password_hash) VALUES ('local', ?, ?, ?)`, name, RoleAdmin, hash)
		if err != nil {
			return e, err
		}
		id, _ := res.LastInsertId()
		u = User{ID: id, Kind: "local", Name: name, Role: RoleAdmin, PasswordHash: hash}
		return e, nil
	})
	if err != nil {
		return User{}, err
	}
	return u, nil
}
