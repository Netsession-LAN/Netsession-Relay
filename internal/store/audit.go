package store

import (
	"context"
	"database/sql"
	// pure-Go SQLite driver, registers "sqlite"
)

// The append-only audit log: who changed what, and the hook that passes each entry on to the monitoring outputs.

// Audit appends to the audit log. Changes of the store itself do not use it: they write their entry in the
// transaction of the change (audited).
func (s *Store) Audit(ctx context.Context, user, action, object, before, after string) error {
	return s.audited(ctx, func(*sql.Tx) (AuditEntry, error) {
		return AuditEntry{User: user, Action: action, Object: object, Before: before, After: after}, nil
	})
}

// audited runs fn in one transaction together with the audit entry fn returns: the change and its entry are
// stored together or not at all. An entry without an action means "nothing changed, nothing to record". The
// listener (SetOnAudit) hears about the entry after the commit.
func (s *Store) audited(ctx context.Context, fn func(tx *sql.Tx) (AuditEntry, error)) error {
	var e AuditEntry
	err := s.tx(ctx, func(tx *sql.Tx) error {
		var err error
		if e, err = fn(tx); err != nil || e.Action == "" {
			return err
		}
		e.TS = s.ts()
		_, err = tx.ExecContext(ctx, `INSERT INTO audit_log(ts, user, action, object, before, after) VALUES (?,?,?,?,?,?)`,
			e.TS, e.User, e.Action, e.Object, e.Before, e.After)
		return err
	})
	if err == nil && e.Action != "" {
		s.notifyAudit(e)
	}
	return err
}

// SetOnAudit sets a function called after every audit entry (syslog). It must not block.
func (s *Store) SetOnAudit(fn func(AuditEntry)) {
	s.auditMu.Lock()
	s.onAudit = fn
	s.auditMu.Unlock()
}

func (s *Store) notifyAudit(e AuditEntry) {
	s.auditMu.Lock()
	fn := s.onAudit
	s.auditMu.Unlock()
	if fn != nil {
		fn(e)
	}
}

// AuditEntry is one audit log row.
type AuditEntry struct {
	ID                    int64
	TS, User, Action      string
	Object, Before, After string
}

// AuditLog returns the newest entries.
func (s *Store) AuditLog(ctx context.Context, limit int) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, ts, user, action, object, before, after FROM audit_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEntry
	for rows.Next() {
		var e AuditEntry
		if err := rows.Scan(&e.ID, &e.TS, &e.User, &e.Action, &e.Object, &e.Before, &e.After); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
