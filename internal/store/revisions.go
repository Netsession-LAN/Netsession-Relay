package store

// Revisions: every applied (or attempted) configuration state.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Revision is one applied (or attempted) configuration state.
type Revision struct {
	ID            int64
	TS, User      string
	Note          string
	Hash          string
	Snapshot      string // JSON
	Config        string // JSON (Config)
	Warnings      []string
	AppliedOK     bool
	EngineMessage string
}

// AddRevision stores a revision and returns its ID.
func (s *Store) AddRevision(ctx context.Context, r Revision) (int64, error) {
	w, _ := json.Marshal(r.Warnings)
	res, err := s.db.ExecContext(ctx, `INSERT INTO config_revision(ts, user, note, hash, snapshot, config, warnings, applied_ok, engine_message) VALUES (?,?,?,?,?,?,?,?,?)`,
		s.ts(), r.User, r.Note, r.Hash, r.Snapshot, r.Config, string(w), r.AppliedOK, r.EngineMessage)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Revisions returns the newest revisions (without snapshot/config bodies).
func (s *Store) Revisions(ctx context.Context, limit int) ([]Revision, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, ts, user, note, hash, warnings, applied_ok, engine_message FROM config_revision ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Revision
	for rows.Next() {
		var r Revision
		var w string
		if err := rows.Scan(&r.ID, &r.TS, &r.User, &r.Note, &r.Hash, &w, &r.AppliedOK, &r.EngineMessage); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(w), &r.Warnings)
		out = append(out, r)
	}
	return out, rows.Err()
}

// RevisionByID returns a revision with snapshot and config.
func (s *Store) RevisionByID(ctx context.Context, id int64) (Revision, error) {
	var r Revision
	var w string
	err := s.db.QueryRowContext(ctx, `SELECT id, ts, user, note, hash, snapshot, config, warnings, applied_ok, engine_message FROM config_revision WHERE id = ?`, id).
		Scan(&r.ID, &r.TS, &r.User, &r.Note, &r.Hash, &r.Snapshot, &r.Config, &w, &r.AppliedOK, &r.EngineMessage)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("%w: revision %d", ErrNotFound, id)
	}
	_ = json.Unmarshal([]byte(w), &r.Warnings)
	return r, err
}
