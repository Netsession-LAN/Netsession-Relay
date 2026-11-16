package store

import (
	"context"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/Netsession2024/Netsession-Relay/internal/gamedb"
)

func open(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relay.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrateIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relay.db")
	for range 2 {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		var n int
		files, _ := fs.Glob(migrations, "migrations/*.sql")
		if err := s.DB.QueryRow(`SELECT count(*) FROM schema_migration`).Scan(&n); err != nil || n != len(files) || n < 2 {
			t.Fatalf("migrations = %d of %d files, %v", n, len(files), err)
		}
		s.Close()
	}
}

func TestSeedSync(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	seed, err := gamedb.Seed()
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.SyncSeed(ctx, seed)
	if err != nil || res.Added != len(seed.Games) {
		t.Fatalf("first sync %+v %v", res, err)
	}
	if res, _ = s.SyncSeed(ctx, seed); res != (SeedResult{}) {
		t.Fatalf("second sync changed something: %+v", res)
	}
	// simulate an older seed version: unmodified row gets updated, modified row gets "pending"
	if _, err := s.DB.Exec(`UPDATE game SET seed_hash = 'old' WHERE id IN ('flatout1', 'flatout2')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE game SET locally_modified = 1 WHERE id = 'flatout2'`); err != nil {
		t.Fatal(err)
	}
	res, err = s.SyncSeed(ctx, seed)
	if err != nil || res.Updated != 1 || res.Pending != 1 {
		t.Fatalf("sync after change %+v %v", res, err)
	}
	db, err := s.GameDB(ctx)
	if err != nil || len(db.Games) != len(seed.Games) {
		t.Fatalf("GameDB: %v", err)
	}
}

func TestAuditAppendOnly(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.Audit(ctx, "alice", "test", "x", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE audit_log SET user = 'mallory'`); err == nil {
		t.Error("audit_log update allowed")
	}
	if _, err := s.DB.Exec(`DELETE FROM audit_log`); err == nil {
		t.Error("audit_log delete allowed")
	}
	log, err := s.AuditLog(ctx, 10)
	if err != nil || len(log) != 1 || log[0].User != "alice" {
		t.Fatalf("audit log %+v %v", log, err)
	}
}

func TestSetModeValidates(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.SetMode(ctx, "cli", "turbo"); err == nil {
		t.Error("invalid mode accepted")
	}
	if err := s.SetMode(ctx, "cli", "observe"); err != nil {
		t.Fatal(err)
	}
	if m, _ := s.Setting(ctx, SettingMode, ""); m != "observe" {
		t.Errorf("mode = %q", m)
	}
}

func TestSplitSQLKeepsTriggers(t *testing.T) {
	stmts := splitSQL("CREATE TABLE a (x INT);\n-- c\nCREATE TRIGGER t BEFORE UPDATE ON a BEGIN SELECT 1; END;\nCREATE TABLE b (y INT);\n")
	if len(stmts) != 3 {
		t.Fatalf("%d statements: %q", len(stmts), stmts)
	}
}
