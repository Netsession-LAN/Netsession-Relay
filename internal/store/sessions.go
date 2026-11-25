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

// Login sessions: created at the login, found by the token of the cookie, ended at the logout.

// SessionTTL is the absolute lifetime of a session, for local and OIDC accounts alike: after it the user logs in
// again, and an OIDC account that lost its relay group is out at the latest then (its role is read from the
// database on every request; `nsrelay admin logout <name>` ends its sessions at once).
//
// Accounts are never locked after failed logins (that would let anybody keep the owner out and tell which names
// exist); the web part pauses per client address and name instead. The columns user.failed and user.locked_until
// of the first schema are therefore unused. They are not dropped: migrations never remove a column (see migrate),
// so that an older version, which still reads them, keeps working on a newer database.
const SessionTTL = 12 * time.Hour

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
