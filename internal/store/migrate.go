package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	// pure-Go SQLite driver, registers "sqlite"
)

// Migrations: the SQL files in migrations/ applied in order, each in its own transaction.

//go:embed migrations/*.sql
var migrations embed.FS

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
	return s.applyPending(ctx, names, last)
}

// applyPending applies the migrations of names that are not recorded yet, in this order, and stops after the
// migration last (see isLastMigration) if it was among them.
func (s *Store) applyPending(ctx context.Context, names []string, last string) error {
	for _, name := range names {
		ran, err := s.applyIfPending(ctx, name)
		if err != nil {
			return err
		}
		if ran && isLastMigration(name, last) {
			break
		}
	}
	return nil
}

// applyIfPending applies the migration file name unless it is recorded as applied; ran reports that it ran now.
func (s *Store) applyIfPending(ctx context.Context, name string) (ran bool, err error) {
	applied, err := s.migrationApplied(ctx, name)
	if err != nil || applied {
		return false, err
	}
	return true, s.applyMigration(ctx, name)
}

// isLastMigration reports whether the migration file name is the one openUpTo stops after: its base name starts
// with last ("" stops nowhere).
func isLastMigration(name, last string) bool {
	return last != "" && strings.HasPrefix(path.Base(name), last)
}

// migrationApplied reports whether the migration file name is recorded as applied.
func (s *Store) migrationApplied(ctx context.Context, name string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_migration WHERE name = ?`, name).Scan(&n)
	return n > 0, err
}

// applyMigration runs the statements of one migration file and records it, all in one transaction.
func (s *Store) applyMigration(ctx context.Context, name string) error {
	body, err := migrations.ReadFile(name)
	if err != nil {
		return err
	}
	return s.tx(ctx, func(tx *sql.Tx) error {
		for _, stmt := range splitSQL(string(body)) {
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("%s: %w\n%s", name, err, stmt)
			}
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO schema_migration(name, applied) VALUES (?, ?)`, name, s.ts())
		return err
	})
}

// splitSQL splits a migration into statements, line by line: a statement ends with a line that ends in ";",
// comment lines and empty lines are dropped, and a trigger (CREATE TRIGGER ... END;) is kept together. That is
// all the migrations of this package need; a ";" at the end of a line inside a string literal would break it
// (TestSplitSQLShippedMigrations checks every shipped file).
func splitSQL(body string) []string {
	var sp sqlSplitter
	for _, line := range strings.Split(body, "\n") {
		sp.add(line)
	}
	if strings.TrimSpace(sp.cur.String()) != "" {
		sp.out = append(sp.out, sp.cur.String())
	}
	return sp.out
}

// sqlSplitter collects the lines of the current statement for splitSQL.
type sqlSplitter struct {
	out       []string
	cur       strings.Builder
	inTrigger bool
}

func (sp *sqlSplitter) add(line string) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "--") || trimmed == "" {
		return
	}
	sp.cur.WriteString(line)
	sp.cur.WriteString("\n")
	upper := strings.ToUpper(trimmed)
	if strings.HasPrefix(upper, "CREATE TRIGGER") {
		sp.inTrigger = true
	}
	if sp.ends(trimmed, upper) {
		sp.out = append(sp.out, sp.cur.String())
		sp.cur.Reset()
		sp.inTrigger = false
	}
}

// ends reports whether the line ends the current statement: it ends in ";", and inside a trigger it is the END.
func (sp *sqlSplitter) ends(trimmed, upper string) bool {
	return strings.HasSuffix(trimmed, ";") && (!sp.inTrigger || strings.HasSuffix(upper, "END;"))
}
