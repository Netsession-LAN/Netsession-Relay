package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
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
// Accounts are not locked after failed logins any more (that let anybody keep the owner out and told which
// names exist); the web part pauses per client address and name instead. The columns failed/locked_until stay
// in the schema, locked_until is no longer read.
const SessionTTL = 12 * time.Hour

// User is an account.
type User struct {
	ID           int64
	Kind, Name   string
	Role         string
	PasswordHash string
	Failed       int // failed logins since the last successful one
}

// SetLocalUser creates or updates a local account with a password hash and role.
func (s *Store) SetLocalUser(ctx context.Context, actor, name, hash, role string) error {
	if RoleRank(role) == 0 {
		return fmt.Errorf("role %q invalid (viewer|operator|admin)", role)
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO user(kind, name, role, password_hash) VALUES ('local', ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET password_hash = excluded.password_hash, role = excluded.role, failed = 0, locked_until = ''`,
		name, role, hash)
	if err != nil {
		return err
	}
	// a new password or role ends every session of the account
	if _, err := s.DeleteUserSessions(ctx, name); err != nil {
		return err
	}
	return s.Audit(ctx, actor, "user.set_password", name, "", role)
}

// DeleteUserSessions logs an account out everywhere and returns the number of sessions ended.
func (s *Store) DeleteUserSessions(ctx context.Context, name string) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM session WHERE user_id IN (SELECT id FROM user WHERE name = ?)`, name)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// Logout ends all sessions of an account (CLI `nsrelay admin logout`) and records it.
func (s *Store) Logout(ctx context.Context, actor, name string) (int64, error) {
	if _, err := s.UserByName(ctx, name); err != nil {
		return 0, err
	}
	n, err := s.DeleteUserSessions(ctx, name)
	if err != nil {
		return 0, err
	}
	return n, s.Audit(ctx, actor, "user.logout", name, "", fmt.Sprint(n))
}

// UserByName returns a user.
func (s *Store) UserByName(ctx context.Context, name string) (User, error) {
	var u User
	err := s.DB.QueryRowContext(ctx, `SELECT id, kind, name, role, password_hash, failed FROM user WHERE name = ?`, name).
		Scan(&u.ID, &u.Kind, &u.Name, &u.Role, &u.PasswordHash, &u.Failed)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// LoginFailed counts a failed attempt and records it. The account is not locked (see SessionTTL).
func (s *Store) LoginFailed(ctx context.Context, u User, ip string) error {
	if _, err := s.DB.ExecContext(ctx, `UPDATE user SET failed = failed + 1 WHERE id = ?`, u.ID); err != nil {
		return err
	}
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
	_, err := s.DB.ExecContext(ctx, `INSERT INTO session(id, user_id, csrf, created, expires, ip) VALUES (?,?,?,?,?,?)`,
		hashToken(token), u.ID, csrf, now.Format(time.RFC3339Nano), now.Add(SessionTTL).Format(time.RFC3339Nano), ip)
	if err != nil {
		return err
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE user SET failed = 0, locked_until = '', last_login = ? WHERE id = ?`,
		now.Format(time.RFC3339Nano), u.ID); err != nil {
		return err
	}
	_, _ = s.DB.ExecContext(ctx, `DELETE FROM session WHERE expires < ?`, now.Format(time.RFC3339Nano))
	return s.Audit(ctx, u.Name, "login", ip, "", u.Kind)
}

// SessionByToken returns the valid session for a cookie value.
func (s *Store) SessionByToken(ctx context.Context, token string) (Session, error) {
	var se Session
	var exp string
	err := s.DB.QueryRowContext(ctx, `SELECT s.id, s.csrf, s.expires, u.id, u.kind, u.name, u.role
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
	_, err := s.DB.ExecContext(ctx, `DELETE FROM session WHERE id = ?`, hashToken(token))
	return err
}

// RoleFromGroups maps Authentik groups to a role: the highest of relay-admin, relay-operator, relay-viewer,
// "" if none of them.
func RoleFromGroups(groups []string) string {
	best := ""
	for _, g := range groups {
		var r string
		switch strings.ToLower(strings.TrimSpace(g)) {
		case "relay-admin":
			r = RoleAdmin
		case "relay-operator":
			r = RoleOperator
		case "relay-viewer":
			r = RoleViewer
		}
		if RoleRank(r) > RoleRank(best) {
			best = r
		}
	}
	return best
}

// UpsertOIDCUser creates or refreshes the account of an Authentik subject. Name and role follow the claims at
// every login. A local account with the same name keeps its name; the OIDC account then gets "<name>@sso".
func (s *Store) UpsertOIDCUser(ctx context.Context, subject, name, email, role string) (User, error) {
	if subject == "" || RoleRank(role) == 0 {
		return User{}, fmt.Errorf("%w: subject or role", ErrInvalid)
	}
	if name == "" {
		name = email
	}
	if name == "" {
		name = "sso-" + subject[:min(8, len(subject))]
	}
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
			res, err := tx.ExecContext(ctx, `INSERT INTO user(kind, name, role, subject, email) VALUES ('oidc', ?, ?, ?, ?)`, name, role, subject, email)
			if err != nil {
				return err
			}
			id, _ = res.LastInsertId()
		} else {
			// a changed role ends the sessions opened with the old one
			if _, err := tx.ExecContext(ctx, `DELETE FROM session WHERE user_id = ? AND (SELECT role FROM user WHERE id = ?) != ?`, id, id, role); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE user SET name = ?, role = ?, email = ? WHERE id = ?`, name, role, email, id); err != nil {
				return err
			}
		}
		u = User{ID: id, Kind: "oidc", Name: name, Role: role}
		return nil
	})
	return u, err
}

// LocalAccounts counts the local accounts that can log in (with a password).
func (s *Store) LocalAccounts(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM user WHERE kind = 'local' AND password_hash != ''`).Scan(&n)
	return n, err
}

// CreateFirstAdmin creates the admin account of the first-run setup. It only works while no local account
// exists (ErrExists otherwise, also when two browsers race), so the open setup page cannot add or replace
// accounts later.
func (s *Store) CreateFirstAdmin(ctx context.Context, name, hash string) (User, error) {
	var u User
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM user WHERE kind = 'local' AND password_hash != ''`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrExists
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM user WHERE name = ?`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("%w: name", ErrInvalid)
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO user(kind, name, role, password_hash) VALUES ('local', ?, ?, ?)`, name, RoleAdmin, hash)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		u = User{ID: id, Kind: "local", Name: name, Role: RoleAdmin, PasswordHash: hash}
		return nil
	})
	if err != nil {
		return u, err
	}
	return u, s.Audit(ctx, name, "user.setup", name, "", RoleAdmin)
}
