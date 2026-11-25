package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
		if err := firstAdminAllowed(ctx, tx, name); err != nil {
			return e, err
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

// firstAdminAllowed refuses the first-run admin when a local account exists already (ErrExists) or another
// account has the name.
func firstAdminAllowed(ctx context.Context, tx *sql.Tx, name string) error {
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
	return nil
}
