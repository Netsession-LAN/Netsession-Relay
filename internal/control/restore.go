package control

import (
	"database/sql"
	"fmt"
	"os"
	"time"
)

// Restore replaces the database at dbPath with a backup file after checking it opens and has a schema.
// The current database is kept as <dbPath>.before-restore-<time>. The web part must be stopped.
func Restore(dbPath, backup string) (string, error) {
	if err := checkBackup(backup); err != nil {
		return "", err
	}
	keep, err := setAside(dbPath)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(backup)
	if err != nil {
		return keep, err
	}
	return keep, os.WriteFile(dbPath, b, 0o600) //nolint:gosec // G703: the backup file is named by the operator on the command line
}

// checkBackup makes sure a file is a relay database: it opens read-only and has recorded migrations.
func checkBackup(backup string) error {
	db, err := sql.Open("sqlite", "file:"+backup+"?mode=ro")
	if err != nil {
		return err
	}
	var n int
	err = db.QueryRow(`SELECT count(*) FROM schema_migration`).Scan(&n)
	db.Close()
	if err != nil {
		return fmt.Errorf("%s is not a relay backup: %w", backup, err)
	}
	if n == 0 {
		return fmt.Errorf("%s is not a relay backup: no migrations recorded", backup)
	}
	return nil
}

// setAside renames the current database to <dbPath>.before-restore-<time> and removes its WAL files; it
// returns the new name, empty when there is no database yet.
func setAside(dbPath string) (string, error) {
	if !exists(dbPath) {
		return "", nil
	}
	keep := dbPath + ".before-restore-" + time.Now().UTC().Format("20060102-150405")
	if err := os.Rename(dbPath, keep); err != nil {
		return "", err
	}
	for _, suf := range []string{"-wal", "-shm"} {
		os.Remove(dbPath + suf)
	}
	return keep, nil
}

// exists reports whether path can be looked at.
func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
