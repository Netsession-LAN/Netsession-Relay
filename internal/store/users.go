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

// Lockout policy for local logins.
const (
	MaxFailed   = 5
	LockoutTime = 15 * time.Minute
	SessionTTL  = 12 * time.Hour
)

// User is an account.
type User struct {
	ID           int64
	Kind, Name   string
	Role         string
	PasswordHash string
	Failed       int
	LockedUntil  time.Time
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
	return s.Audit(ctx, actor, "user.set_password", name, "", role)
}

// UserByName returns a user.
func (s *Store) UserByName(ctx context.Context, name string) (User, error) {
	var u User
	var locked string
	err := s.DB.QueryRowContext(ctx, `SELECT id, kind, name, role, password_hash, failed, locked_until FROM user WHERE name = ?`, name).
		Scan(&u.ID, &u.Kind, &u.Name, &u.Role, &u.PasswordHash, &u.Failed, &locked)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	if locked != "" {
		u.LockedUntil, _ = time.Parse(time.RFC3339Nano, locked)
	}
	return u, err
}

// LoginFailed counts a failed attempt and locks the account after MaxFailed.
func (s *Store) LoginFailed(ctx context.Context, u User, ip string) error {
	failed := u.Failed + 1
	locked := ""
	if failed >= MaxFailed {
		locked = s.Now().Add(LockoutTime).UTC().Format(time.RFC3339Nano)
		failed = 0
	}
	if _, err := s.DB.ExecContext(ctx, `UPDATE user SET failed = ?, locked_until = ? WHERE id = ?`, failed, locked, u.ID); err != nil {
		return err
	}
	action := "login.failed"
	if locked != "" {
		action = "login.locked"
	}
	return s.Audit(ctx, u.Name, action, ip, "", "")
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
