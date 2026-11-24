// Package store persists the relay configuration in SQLite: migrations, seed sync, VLANs, flow profiles,
// events, settings, revisions and the append-only audit log.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	"modernc.org/sqlite" // pure-Go SQLite driver, registers "sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is the configuration database.
type Store struct {
	Now func() time.Time

	db *sql.DB

	auditMu sync.Mutex
	onAudit func(AuditEntry)
}

// Open opens (and creates) the database file and applies pending migrations.
func Open(path string) (*Store, error) { return openUpTo(path, "") }

// openUpTo is Open that stops after the migration whose file name starts with last ("" = all of them). Tests
// use it to build a database of an older version.
func openUpTo(path, last string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: one writer; keeps transactions simple
	s := &Store{db: db, Now: time.Now}
	if err := s.migrate(context.Background(), last); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Ping reports whether the database answers (health check).
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// VacuumInto writes a consistent copy of the database to a new file (backup); the file must not exist or be
// empty.
func (s *Store) VacuumInto(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}

// migrate applies the migrations that are not recorded yet, each in its own transaction, in the order of their
// file names.
//
// Migrations never remove or rename a table or column an older version needs (they add tables, columns with a
// default and indexes, and may move data into a new place): when a deploy is rolled back, the previous version of
// the program has to work with the database the newer one left behind.
func (s *Store) migrate(ctx context.Context, last string) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migration (name TEXT PRIMARY KEY, applied TEXT NOT NULL)`); err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migration WHERE name = ?`, name).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		body, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		err = s.tx(ctx, func(tx *sql.Tx) error {
			for _, stmt := range splitSQL(string(body)) {
				if _, err := tx.ExecContext(ctx, stmt); err != nil {
					return fmt.Errorf("%s: %w\n%s", name, err, stmt)
				}
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO schema_migration(name, applied) VALUES (?, ?)`, name, s.ts())
			return err
		})
		if err != nil {
			return err
		}
		if last != "" && strings.HasPrefix(path.Base(name), last) {
			break
		}
	}
	return nil
}

// splitSQL splits a migration into statements, line by line: a statement ends with a line that ends in ";",
// comment lines and empty lines are dropped, and a trigger (CREATE TRIGGER ... END;) is kept together. That is
// all the migrations of this package need; a ";" at the end of a line inside a string literal would break it
// (TestSplitSQLShippedMigrations checks every shipped file).
func splitSQL(body string) []string {
	var out []string
	var cur strings.Builder
	inTrigger := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") || trimmed == "" {
			continue
		}
		cur.WriteString(line)
		cur.WriteString("\n")
		upper := strings.ToUpper(trimmed)
		if strings.HasPrefix(upper, "CREATE TRIGGER") {
			inTrigger = true
		}
		if strings.HasSuffix(trimmed, ";") && (!inTrigger || strings.HasSuffix(upper, "END;")) {
			out = append(out, cur.String())
			cur.Reset()
			inTrigger = false
		}
	}
	if strings.TrimSpace(cur.String()) != "" {
		out = append(out, cur.String())
	}
	return out
}

func (s *Store) ts() string { return s.Now().UTC().Format(time.RFC3339Nano) }

func (s *Store) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback() // the error of fn is the one that matters
		return err
	}
	return tx.Commit()
}

// ErrNotFound is returned for missing rows.
var ErrNotFound = errors.New("store: not found")

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

// isUniqueViolation reports whether err is SQLite refusing a duplicate key. It looks at the result code of the
// driver, never at the wording of the message.
func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	return se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || se.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
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

// Setting returns a setting, or def when it is not set. When the database cannot be read it returns def together
// with the error, so a caller that only needs a usable value may ignore the error.
func (s *Store) Setting(ctx context.Context, key, def string) (string, error) {
	var v string
	switch err := s.db.QueryRowContext(ctx, `SELECT value FROM setting WHERE key = ?`, key).Scan(&v); {
	case errors.Is(err, sql.ErrNoRows):
		return def, nil
	case err != nil:
		return def, err
	}
	return v, nil
}

// SetSetting stores a setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, upsertSetting, key, value)
	return err
}

const upsertSetting = `INSERT INTO setting(key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`

// Setting keys.
const (
	SettingMode = "engine_mode" // active | observe | paused
)
