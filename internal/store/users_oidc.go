package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Accounts of the login with OpenID Connect: created or updated at every login with the role from the groups.

// UpsertOIDCUser creates or refreshes the account of a single sign-on (OIDC) subject. Name and role follow the claims at
// every login. A local account with the same name keeps its name; the OIDC account then gets "<name>@sso".
func (s *Store) UpsertOIDCUser(ctx context.Context, subject, name, email, role string) (User, error) {
	if subject == "" || RoleRank(role) == 0 {
		return User{}, fmt.Errorf("%w: subject or role", ErrInvalid)
	}
	name = oidcName(subject, name, email)
	var u User
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		u, err = upsertOIDCUser(ctx, tx, subject, name, email, role)
		return err
	})
	return u, err
}

func upsertOIDCUser(ctx context.Context, tx *sql.Tx, subject, name, email, role string) (User, error) {
	id, err := oidcAccountID(ctx, tx, subject)
	if err != nil {
		return User{}, err
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
		return User{}, err
	}
	return User{ID: id, Kind: "oidc", Name: name, Role: role}, nil
}

// oidcAccountID returns the ID of the account of an OIDC subject, 0 when it has none yet.
func oidcAccountID(ctx context.Context, tx *sql.Tx, subject string) (int64, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM user WHERE kind = 'oidc' AND subject = ?`, subject).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	return id, nil
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
