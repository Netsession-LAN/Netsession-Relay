package store

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
)

// stage is a database of an old version: the migrations up to and including upTo, and the rows that version
// could write.
type stage struct {
	upTo string
	rows []stageRow
}

type stageRow struct {
	sql  string
	args []any
}

const oldSessionToken = "old-session-cookie"

func oldGame() gamedb.Game {
	return gamedb.Game{ID: "lanparty_classic", Name: "LAN Party Classic", Category: gamedb.CategoryGame, RelayNeeded: gamedb.RelayYes,
		Confidence: "high", SourceIP: gamedb.SourceOriginal, Discovery: []gamedb.Spec{{Kind: gamedb.KindBroadcast, Ports: "4711"}}}
}

// oldStages writes a database the way the first versions did, one version after the other: the very first
// schema with a configuration, then accounts, then test results. Every later migration has to cope with all of it.
func oldStages(t *testing.T) []stage {
	t.Helper()
	game := oldGame()
	data, err := json.Marshal(game)
	if err != nil {
		t.Fatal(err)
	}
	return []stage{
		{"0001_", []stageRow{
			{`INSERT INTO game(id, name, category, relay_needed, confidence, origin, data) VALUES (?,?,?,?,?,'custom',?)`,
				[]any{game.ID, game.Name, game.Category, game.RelayNeeded, game.Confidence, string(data)}},
			{`INSERT INTO vlan(vid, name, grp, cidr, relay_ip, attach, parent) VALUES (1101, 'Tisch 1', 'tische', '10.10.1.0/24', '10.10.1.55', 'trunk', 'eth1')`, nil},
			{`INSERT INTO vlan(vid, name, grp, cidr, relay_ip, attach, parent) VALUES (1102, 'Tisch 2', 'tische', '10.10.2.0/24', '10.10.2.55', 'trunk', 'eth1')`, nil},
			{`INSERT INTO flow_profile(name, is_default) VALUES ('spiele', 1)`, nil},
			{`INSERT INTO flow_profile_edge(profile, src, dst) VALUES ('spiele', 'tische', 'tische')`, nil},
			{`INSERT INTO event(id, name, is_active, created) VALUES (1, 'Old LAN', 1, '2025-01-01T00:00:00Z')`, nil},
			{`INSERT INTO event_vlan(event_id, vid, enabled) VALUES (1, 1101, 1), (1, 1102, 0)`, nil},
			{`INSERT INTO event_game(event_id, game_id, flow_profile) VALUES (1, ?, 'spiele')`, []any{game.ID}},
			{`INSERT INTO setting(key, value) VALUES ('engine_mode', 'observe'), ('syslog_target', '192.0.2.5:514')`, nil},
			{`INSERT INTO audit_log(ts, user, action, object) VALUES ('2025-01-01T00:00:00Z', 'dana', 'game.enable', ?)`, []any{game.ID}},
			{`INSERT INTO config_revision(ts, user, note, hash, snapshot, config, applied_ok) VALUES ('2025-01-01T00:00:00Z', 'dana', 'first', 'abc123', '{}', '{}', 1)`, nil},
		}},
		{"0002_", []stageRow{
			{`INSERT INTO user(id, kind, name, role, password_hash, failed, locked_until) VALUES (7, 'local', 'dana', 'operator', 'hash-of-dana', 4, '2025-01-01T00:10:00Z')`, nil},
			{`INSERT INTO session(id, user_id, csrf, created, expires) VALUES (?, 7, 'csrf-token', '2025-01-01T00:00:00Z', '2999-01-01T00:00:00Z')`, []any{hashToken(oldSessionToken)}},
		}},
		{"0003_", []stageRow{
			{`INSERT INTO verification(game_id, event, result, user, ts) VALUES (?, 'Old LAN', 'works', 'dana', '2025-01-01T01:00:00Z')`, []any{game.ID}},
		}},
	}
}

// A database that started with the very first schema (migration 0001) and was filled by each of the first
// versions is upgraded through every migration and keeps its data: the configuration, accounts and sessions,
// test results, the audit log, revisions and the one syslog target of the first version.
func TestUpgradeFromOldSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "relay.db")
	for i, st := range oldStages(t) {
		old, err := openUpTo(path, st.upTo)
		if err != nil {
			t.Fatalf("schema %s: %v", st.upTo, err)
		}
		var applied int
		if err := old.db.QueryRow(`SELECT count(*) FROM schema_migration`).Scan(&applied); err != nil || applied != i+1 {
			t.Fatalf("schema %s has %d migrations, want %d (%v)", st.upTo, applied, i+1, err)
		}
		for _, q := range st.rows {
			if _, err := old.db.Exec(q.sql, q.args...); err != nil {
				t.Fatalf("schema %s: %s: %v", st.upTo, q.sql, err)
			}
		}
		if err := old.Close(); err != nil {
			t.Fatal(err)
		}
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	defer s.Close()
	game := oldGame()

	t.Run("every migration is recorded", func(t *testing.T) {
		files, _ := fs.Glob(migrations, "migrations/*.sql")
		var applied int
		if err := s.db.QueryRow(`SELECT count(*) FROM schema_migration`).Scan(&applied); err != nil || applied != len(files) {
			t.Fatalf("after the upgrade %d of %d migrations are recorded (%v)", applied, len(files), err)
		}
	})

	t.Run("account and session survive", func(t *testing.T) {
		u, err := s.UserByName(ctx, "dana")
		if err != nil || u.ID != 7 || u.Role != RoleOperator || u.PasswordHash != "hash-of-dana" || u.Kind != "local" {
			t.Errorf("account after the upgrade: %+v, %v", u, err)
		}
		if se, err := s.SessionByToken(ctx, oldSessionToken); err != nil || se.User.Name != "dana" || se.CSRF != "csrf-token" {
			t.Errorf("session after the upgrade: %+v, %v", se, err)
		}
		if err := s.CreateSession(ctx, u, "new-cookie", "csrf2", "192.0.2.9"); err != nil {
			t.Errorf("login after the upgrade: %v", err)
		}
	})

	// No migration removes a table or column an older version needs: what the old version wrote and read is still
	// there, so it would keep working on this database after a rollback of the program (the unused columns of the
	// old account lock included).
	t.Run("old columns and constraints stay", func(t *testing.T) {
		var failed int
		var locked string
		if err := s.db.QueryRow(`SELECT failed, locked_until FROM user WHERE id = 7`).Scan(&failed, &locked); err != nil || failed != 4 || locked == "" {
			t.Errorf("columns of the first schema after the upgrade: failed=%d locked_until=%q, %v", failed, locked, err)
		}
		if _, err := s.db.Exec(`INSERT INTO event_game(event_id, game_id, flow_profile) VALUES (1, ?, '')`, "x"); err == nil {
			t.Error("foreign keys are not enforced after the upgrade")
		}
	})

	// networks, profile, the game (rows from before the flag "enabled" count as switched on)
	t.Run("configuration", func(t *testing.T) {
		c, err := s.ExportConfig(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if c.Event != "Old LAN" || c.Mode != compile.ModeObserve || len(c.VLANs) != 2 || !c.VLANs[0].Enabled || c.VLANs[1].Enabled ||
			c.VLANs[0].Parent != "eth1" || len(c.Profiles) != 1 || !c.Profiles[0].Default || len(c.Profiles[0].Edges) != 1 {
			t.Errorf("configuration after the upgrade: %+v", c)
		}
		if len(c.Games) != 1 || c.Games[0].ID != game.ID || c.Games[0].FlowProfile != "spiele" || c.Games[0].Off || len(c.Games[0].Sources) != 0 {
			t.Errorf("games of the event after the upgrade: %+v", c.Games)
		}
		g, err := s.Game(ctx, game.ID)
		if err != nil || !g.Enabled || g.Origin != "custom" || len(g.Discovery) != 1 || g.Discovery[0].Ports != "4711" {
			t.Errorf("game after the upgrade: %+v, %v", g, err)
		}
		if in, err := s.BuildInput(ctx); err != nil || len(in.Games) != 1 || len(in.VLANs) != 2 {
			t.Errorf("compiler input after the upgrade: %+v, %v", in, err)
		}
	})

	// the single syslog target of the first version became a monitoring target (migration 0005)
	t.Run("syslog target moved", func(t *testing.T) {
		ts, err := s.Targets(ctx)
		if err != nil || len(ts) != 1 || ts[0].Kind != TargetSyslog || ts[0].Endpoint != "192.0.2.5:514" || !ts[0].Enabled {
			t.Errorf("monitoring targets after the upgrade: %+v, %v", ts, err)
		}
		if v, _ := s.Setting(ctx, "syslog_target", ""); v != "" {
			t.Errorf("old setting syslog_target still there: %q", v)
		}
	})

	t.Run("history", func(t *testing.T) {
		if vs, err := s.Verifications(ctx, game.ID); err != nil || len(vs) != 1 || vs[0].Result != "works" {
			t.Errorf("test results after the upgrade: %+v, %v", vs, err)
		}
		if revs, err := s.Revisions(ctx, 10); err != nil || len(revs) != 1 || revs[0].Note != "first" || !revs[0].AppliedOK {
			t.Errorf("revisions after the upgrade: %+v, %v", revs, err)
		}
		log, err := s.AuditLog(ctx, 10)
		if err != nil || len(log) == 0 || log[len(log)-1].Action != "game.enable" || log[len(log)-1].User != "dana" {
			t.Errorf("audit log after the upgrade: %+v, %v", log, err)
		}
		if _, err := s.EventLogo(ctx); err == nil {
			t.Error("an event logo appeared from nowhere")
		}
	})

	// the upgraded database takes the writes of the current version
	t.Run("writes after the upgrade", func(t *testing.T) {
		if err := s.SetGameEnabled(ctx, "dana", game.ID, false); err != nil {
			t.Errorf("switching the old game off: %v", err)
		}
		if err := s.SetVLANEnabled(ctx, "dana", 1102, true); err != nil {
			t.Errorf("switching an old network on: %v", err)
		}
		if _, err := s.SaveTarget(ctx, "dana", Target{Kind: TargetSyslog, Name: "second", Endpoint: "192.0.2.6:514"}); err != nil {
			t.Errorf("a second monitoring target: %v", err)
		}
	})
}

// A change and its audit entry are one transaction: when the entry cannot be written the change is not stored
// either, and a refused change leaves no entry behind.
func TestAuditIsPartOfTheChange(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if err := s.ImportConfig(ctx, "test", Config{Version: 1, Event: "LAN", Mode: compile.ModeObserve}); err != nil {
		t.Fatal(err)
	}
	var heard []AuditEntry
	s.SetOnAudit(func(e AuditEntry) { heard = append(heard, e) })
	count := func() (n int) {
		t.Helper()
		if err := s.db.QueryRow(`SELECT count(*) FROM audit_log`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	before := count()
	if err := s.SetMode(ctx, "dana", compile.ModeActive); err != nil {
		t.Fatal(err)
	}
	if count() != before+1 || len(heard) != 1 || heard[0].Action != "mode.set" || heard[0].Before != compile.ModeObserve || heard[0].After != compile.ModeActive {
		t.Errorf("a change: %d entries more, listener heard %+v", count()-before, heard)
	}

	// a refused change: no entry, nothing for the listener
	before, heard = count(), nil
	if _, err := s.SaveTarget(ctx, "dana", Target{Kind: TargetSyslog, Name: "A", Endpoint: "192.0.2.5:514"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveTarget(ctx, "dana", Target{Kind: TargetSyslog, Name: "A", Endpoint: "192.0.2.6:514"}); !errors.Is(err, ErrExists) {
		t.Errorf("second target with the same name: %v, want ErrExists", err)
	}
	if err := s.SetVLANEnabled(ctx, "dana", 4000, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown network: %v, want ErrNotFound", err)
	}
	if count() != before+1 || len(heard) != 1 {
		t.Errorf("refused changes left entries: %d more, listener heard %+v", count()-before, heard)
	}

	// the audit log refuses the entry (here: the table is gone): the change must not be stored
	heard = nil
	if _, err := s.db.Exec(`DROP TABLE audit_log`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMode(ctx, "dana", compile.ModePaused); err == nil {
		t.Fatal("a change without its audit entry was accepted")
	}
	if mode, _ := s.Setting(ctx, SettingMode, ""); mode != compile.ModeActive {
		t.Errorf("mode %q after the failed change, want it rolled back to active", mode)
	}
	if err := s.AddProfile(ctx, "dana", "neu", ""); err == nil {
		t.Error("a profile without its audit entry was accepted")
	}
	if c, err := s.ExportConfig(ctx); err != nil || len(c.Profiles) != 0 {
		t.Errorf("profiles after the failed change: %+v, %v", c.Profiles, err)
	}
	if len(heard) != 0 {
		t.Errorf("the listener heard about changes that were rolled back: %+v", heard)
	}
}

// splitSQL is line based (see its comment). Every shipped migration must come out as complete statements:
// none empty, each ending with ";", triggers in one piece, and nothing but comments lost.
func TestSplitSQLShippedMigrations(t *testing.T) {
	files, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("migrations: %v, %v", files, err)
	}
	for _, name := range files {
		body, err := migrations.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		stmts := splitSQL(string(body))
		if len(stmts) == 0 {
			t.Errorf("%s: no statement", name)
		}
		var kept, want int
		for _, line := range strings.Split(string(body), "\n") {
			if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "--") {
				want++
			}
		}
		for _, st := range stmts {
			trimmed := strings.TrimSpace(st)
			kept += strings.Count(trimmed, "\n") + 1
			upper := strings.ToUpper(trimmed)
			switch {
			case !strings.HasSuffix(trimmed, ";"):
				t.Errorf("%s: statement without end: %q", name, trimmed)
			case strings.HasPrefix(upper, "CREATE TRIGGER") && !strings.HasSuffix(upper, "END;"):
				t.Errorf("%s: trigger cut apart: %q", name, trimmed)
			case !strings.HasPrefix(upper, "CREATE TRIGGER") && strings.Count(trimmed, ";") != 1:
				t.Errorf("%s: several statements in one, or a ';' inside a statement: %q", name, trimmed)
			}
		}
		if kept != want {
			t.Errorf("%s: %d of %d lines of SQL ended up in statements", name, kept, want)
		}
	}
}

func TestSplitSQL(t *testing.T) {
	got := splitSQL("-- comment\nCREATE TABLE a (\n  id INTEGER -- trailing comment\n);\n\nCREATE TRIGGER t BEFORE UPDATE ON a BEGIN SELECT 1; END;\nINSERT INTO a\n  VALUES (1);\n")
	if len(got) != 3 || !strings.HasPrefix(got[0], "CREATE TABLE a") || !strings.Contains(got[1], "SELECT 1; END;") || !strings.HasPrefix(got[2], "INSERT INTO a") {
		t.Errorf("statements: %q", got)
	}
	if got := splitSQL("SELECT 1"); len(got) != 1 { // a last statement without ";" is still run
		t.Errorf("statement without the final ';': %q", got)
	}
}
