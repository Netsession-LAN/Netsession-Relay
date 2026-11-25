// Package store persists the relay configuration in SQLite: migrations, seed sync, VLANs, flow profiles,
// events, settings, revisions and the append-only audit log.
package store

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

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

// ErrExists is returned when a new entry (game, VLAN, flow profile, account, target) would replace an existing one.
var ErrExists = errors.New("exists")

// ErrInvalid is returned for names or values the store refuses.
var ErrInvalid = errors.New("invalid")

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

// isUniqueViolation reports whether err is SQLite refusing a duplicate key. It looks at the result code of the
// driver, never at the wording of the message.
func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	return se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE || se.Code() == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}
