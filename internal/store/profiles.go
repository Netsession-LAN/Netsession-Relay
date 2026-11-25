package store

// Flow profiles: the named VLAN matrices (create, rename, default, delete); their edges are in profile_edges.go.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
)

var profileNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)

// ErrDefaultProfile is returned when the default profile would be deleted.
var ErrDefaultProfile = errors.New("default profile")

func (s *Store) profileNames(ctx context.Context) ([]ProfileConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name, is_default FROM flow_profile ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProfileConfig
	for rows.Next() {
		var p ProfileConfig
		if err := rows.Scan(&p.Name, &p.Default); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// profileExists reports whether a flow profile of that name exists.
func profileExists(ctx context.Context, tx *sql.Tx, name string) (bool, error) {
	var n int
	err := tx.QueryRowContext(ctx, `SELECT count(*) FROM flow_profile WHERE name = ?`, name).Scan(&n)
	return n > 0, err
}

// requireProfile returns an ErrNotFound when the flow profile does not exist.
func requireProfile(ctx context.Context, tx *sql.Tx, name string) error {
	ok, err := profileExists(ctx, tx, name)
	if err == nil && !ok {
		err = fmt.Errorf("%w: profile %q", ErrNotFound, name)
	}
	return err
}

// requireNewProfile returns an ErrExists when a flow profile of that name exists already.
func requireNewProfile(ctx context.Context, tx *sql.Tx, name string) error {
	ok, err := profileExists(ctx, tx, name)
	if err == nil && ok {
		err = fmt.Errorf("%w: profile %q", ErrExists, name)
	}
	return err
}

// AddProfile creates a flow profile, optionally as a copy of another one.
func (s *Store) AddProfile(ctx context.Context, user, name, copyFrom string) error {
	if !profileNameRE.MatchString(name) {
		return fmt.Errorf("%w: profile name %q", ErrInvalid, name)
	}
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		e := AuditEntry{User: user, Action: "profile.create", Object: name, Before: copyFrom}
		if err := requireNewProfile(ctx, tx, name); err != nil {
			return e, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO flow_profile(name, is_default) VALUES (?, 0)`, name); err != nil {
			return e, err
		}
		return e, copyProfileEdges(ctx, tx, name, copyFrom)
	})
}

// copyProfileEdges gives profile name the edges of profile from ("" copies nothing).
func copyProfileEdges(ctx context.Context, tx *sql.Tx, name, from string) error {
	if from == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO flow_profile_edge(profile, src, dst) SELECT ?, src, dst FROM flow_profile_edge WHERE profile = ?`, name, from)
	return err
}

// DeleteProfile removes a flow profile; games that used it fall back to the default profile.
func (s *Store) DeleteProfile(ctx context.Context, user, name string) error {
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		e := AuditEntry{User: user, Action: "profile.delete", Object: name}
		var def bool
		err := tx.QueryRowContext(ctx, `SELECT is_default FROM flow_profile WHERE name = ?`, name).Scan(&def)
		if errors.Is(err, sql.ErrNoRows) {
			return e, fmt.Errorf("%w: profile %q", ErrNotFound, name)
		}
		if err != nil {
			return e, err
		}
		if def {
			return e, fmt.Errorf("%w: %s", ErrDefaultProfile, name)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE event_game SET flow_profile = '' WHERE flow_profile = ?`, name); err != nil {
			return e, err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM flow_profile WHERE name = ?`, name)
		return e, err
	})
}

// SetDefaultProfile makes name the default flow profile.
func (s *Store) SetDefaultProfile(ctx context.Context, user, name string) error {
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		e := AuditEntry{User: user, Action: "profile.default", Object: name}
		if _, err := tx.ExecContext(ctx, `UPDATE flow_profile SET is_default = (name = ?)`, name); err != nil {
			return e, err
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM flow_profile WHERE name = ? AND is_default = 1`, name).Scan(&n); err != nil {
			return e, err
		}
		if n == 0 {
			return e, fmt.Errorf("%w: profile %q", ErrNotFound, name)
		}
		return e, nil
	})
}

// RenameProfile renames a flow profile; edges follow by ON UPDATE CASCADE, games of all events are updated.
func (s *Store) RenameProfile(ctx context.Context, user, name, to string) error {
	if !profileNameRE.MatchString(to) {
		return fmt.Errorf("%w: profile name %q", ErrInvalid, to)
	}
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		e := AuditEntry{User: user, Action: "profile.rename", Object: name, Before: name, After: to}
		if err := requireNewProfile(ctx, tx, to); err != nil {
			return e, err
		}
		res, err := tx.ExecContext(ctx, `UPDATE flow_profile SET name = ? WHERE name = ?`, to, name)
		if err != nil {
			return e, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return e, fmt.Errorf("%w: profile %q", ErrNotFound, name)
		}
		_, err = tx.ExecContext(ctx, `UPDATE event_game SET flow_profile = ? WHERE flow_profile = ?`, to, name)
		return e, err
	})
}
