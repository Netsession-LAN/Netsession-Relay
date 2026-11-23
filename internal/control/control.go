// Package control is the control plane shared by the CLI (nsrelay admin) and later the web UI:
// presets, apply with revision, rollback, backup and restore.
package control

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/Netsession2024/Netsession-Relay/internal/compile"
	"github.com/Netsession2024/Netsession-Relay/internal/engineapi"
	"github.com/Netsession2024/Netsession-Relay/internal/gamedb"
	"github.com/Netsession2024/Netsession-Relay/internal/lab"
	"github.com/Netsession2024/Netsession-Relay/internal/store"
)

// Engine is the part of the engine API the control plane uses (engineapi.Client or a fake in tests).
type Engine interface {
	Validate(context.Context, *compile.Snapshot) (engineapi.Result, error)
	Apply(context.Context, *compile.Snapshot) (engineapi.Result, error)
}

// Preset returns an initial configuration.
//
//	netsession: event network: gameserver 1100 = 10.10.0.0/24, table n = VLAN
//	            1100+n = 10.10.n.0/24 (n = 1..14), WiFi 1253, orga 1254, relay IP .55, WiFi not in the matrix
//	lab:        lab/up.sh layout (1100-1103)
func Preset(name, parent string, db *gamedb.DB) (store.Config, error) {
	c := store.Config{Version: 1, Mode: compile.ModeActive}
	prof := lab.GamesProfile()
	vlan := func(vid int, name, group string, third int) store.VLANConfig {
		return store.VLANConfig{VID: vid, Name: name, Group: group, CIDR: fmt.Sprintf("10.10.%d.0/24", third),
			RelayIP: fmt.Sprintf("10.10.%d.55", third), Attach: compile.AttachTrunk, Parent: parent, Enabled: true}
	}
	switch name {
	case "netsession":
		c.Event = "Netsession LAN"
		c.VLANs = append(c.VLANs, vlan(1100, "Gameserver", "gameserver", 0))
		for n := 1; n <= 14; n++ {
			c.VLANs = append(c.VLANs, vlan(1100+n, fmt.Sprintf("Tisch %d", n), "tische", n))
		}
		c.VLANs = append(c.VLANs, vlan(1253, "User-WiFi", "wlan", 253), vlan(1254, "Orga", "orga", 254))
	case "lab":
		c.Event = "Labor"
		for _, v := range lab.VLANs(parent, 55) {
			c.VLANs = append(c.VLANs, vlan(v.VID, v.Name, v.Group, int(v.CIDR.Addr().As4()[2])))
		}
		var edges []compile.Edge
		for _, e := range prof.Edges {
			if e.Src != "orga" && e.Dst != "orga" {
				edges = append(edges, e)
			}
		}
		prof.Edges = edges
	default:
		return c, fmt.Errorf("unknown preset %q (netsession|lab)", name)
	}
	c.Profiles = []store.ProfileConfig{{Name: prof.Name, Default: true, Edges: prof.Edges}}
	for _, g := range db.Games {
		if g.DefaultEnabled {
			c.Games = append(c.Games, store.EventGameConfig{ID: g.ID})
		}
	}
	return c, nil
}

// SetupConfig is the configuration the first-run setup in the browser creates: the given networks, the default
// matrix (tables, game servers and orga see each other; only groups that have a network) and the games the
// database switches on by default.
func SetupConfig(event, mode string, vlans []store.VLANConfig, db *gamedb.DB) store.Config {
	c := store.Config{Version: 1, Event: event, Mode: mode, VLANs: vlans}
	used := map[string]bool{}
	for _, v := range vlans {
		used[v.Group] = true
	}
	prof := lab.GamesProfile()
	p := store.ProfileConfig{Name: prof.Name, Default: true}
	for _, e := range prof.Edges {
		if used[e.Src] && used[e.Dst] {
			p.Edges = append(p.Edges, e)
		}
	}
	c.Profiles = []store.ProfileConfig{p}
	for _, g := range db.Games {
		if g.DefaultEnabled {
			c.Games = append(c.Games, store.EventGameConfig{ID: g.ID})
		}
	}
	return c
}

// Init creates a fresh configuration: seed games plus preset. Refuses if a configuration exists unless force.
func Init(ctx context.Context, st *store.Store, user, preset, parent string, force bool) (store.SeedResult, error) {
	seed, err := gamedb.Seed()
	if err != nil {
		return store.SeedResult{}, err
	}
	res, err := st.SyncSeed(ctx, seed)
	if err != nil {
		return res, err
	}
	if _, err := st.ActiveEvent(ctx); err == nil && !force {
		return res, errors.New("configuration exists already (use -force to replace it)")
	}
	c, err := Preset(preset, parent, seed)
	if err != nil {
		return res, err
	}
	return res, st.ImportConfig(ctx, user, c)
}

// ApplyResult describes one apply.
type ApplyResult struct {
	Revision int64
	Snapshot *compile.Snapshot
	Warnings []compile.Warning
	Engine   engineapi.Result
}

// Apply compiles the active configuration, sends it to the engine and records a revision (also when the
// engine rejects it). dryRun only compiles.
func Apply(ctx context.Context, st *store.Store, eng Engine, user, note string, dryRun bool) (ApplyResult, error) {
	var res ApplyResult
	in, err := st.BuildInput(ctx)
	if err != nil {
		return res, err
	}
	snap, warns, err := compile.Compile(in)
	res.Warnings = warns
	if err != nil {
		return res, fmt.Errorf("configuration invalid:\n%w", err)
	}
	res.Snapshot = snap
	if dryRun {
		return res, nil
	}
	res.Engine, err = eng.Apply(ctx, snap)
	if err != nil {
		return res, err
	}
	cfg, err := st.ExportConfig(ctx)
	if err != nil {
		return res, err
	}
	sj, _ := json.Marshal(snap)
	cj, _ := json.Marshal(cfg)
	ws := make([]string, len(warns))
	for i, w := range warns {
		ws[i] = w.String()
	}
	res.Revision, err = st.AddRevision(ctx, store.Revision{User: user, Note: note, Hash: snap.Hash, Snapshot: string(sj),
		Config: string(cj), Warnings: ws, AppliedOK: res.Engine.OK, EngineMessage: res.Engine.Error})
	if err != nil {
		return res, err
	}
	action := "apply"
	if !res.Engine.OK {
		action = "apply.rejected"
	}
	_ = st.Audit(ctx, user, action, fmt.Sprintf("revision %d", res.Revision), "", snap.Hash)
	if !res.Engine.OK {
		return res, fmt.Errorf("engine rejected the snapshot: %s", res.Engine.Error)
	}
	return res, nil
}

// StatusEngine is an Engine that also reports which snapshot it runs.
type StatusEngine interface {
	Engine
	Status(context.Context) (engineapi.Status, error)
}

// Refresh re-applies the configuration after an update of the relay software. The engine keeps running its
// last snapshot file across restarts, so features of a new compiler (new rule fields, new seed games) would
// stay off until the next manual change. Refresh applies a new revision only if the engine runs the snapshot
// of the latest revision, the configuration is unchanged since then and compiling it now gives a different
// snapshot. Anything else (engine runs something else, configuration changed but not applied) is left alone.
// Returns the new revision or 0.
func Refresh(ctx context.Context, st *store.Store, eng StatusEngine, note string) (int64, error) {
	revs, err := st.Revisions(ctx, 1)
	if err != nil || len(revs) == 0 {
		return 0, err
	}
	rev, err := st.RevisionByID(ctx, revs[0].ID)
	if err != nil {
		return 0, err
	}
	status, err := eng.Status(ctx)
	if err != nil {
		return 0, err
	}
	if !rev.AppliedOK || status.SnapshotHash != rev.Hash {
		return 0, nil
	}
	var old store.Config
	if err := json.Unmarshal([]byte(rev.Config), &old); err != nil {
		return 0, nil
	}
	cur, err := st.ExportConfig(ctx)
	if err != nil {
		return 0, err
	}
	a, _ := json.Marshal(old)
	b, _ := json.Marshal(cur)
	if string(a) != string(b) {
		return 0, nil
	}
	dry, err := Apply(ctx, st, eng, "", "", true)
	if err != nil || dry.Snapshot.Hash == rev.Hash {
		return 0, err
	}
	res, err := Apply(ctx, st, eng, "system", note, false)
	return res.Revision, err
}

// Rollback restores the configuration of a revision and applies it (a new revision is recorded).
func Rollback(ctx context.Context, st *store.Store, eng Engine, user string, id int64) (ApplyResult, error) {
	rev, err := st.RevisionByID(ctx, id)
	if err != nil {
		return ApplyResult{}, err
	}
	var cfg store.Config
	if err := json.Unmarshal([]byte(rev.Config), &cfg); err != nil {
		return ApplyResult{}, fmt.Errorf("revision %d: %w", id, err)
	}
	if err := st.ImportConfig(ctx, user, cfg); err != nil {
		return ApplyResult{}, err
	}
	_ = st.Audit(ctx, user, "rollback", fmt.Sprintf("revision %d", id), "", "")
	return Apply(ctx, st, eng, user, fmt.Sprintf("rollback to revision %d", id), false)
}

// Backup writes a consistent SQLite copy (VACUUM INTO) and a readable YAML export of the active
// configuration into dir, keeping the newest keep backups. Returns the database copy path.
func Backup(ctx context.Context, st *store.Store, dir string, keep int) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	stamp := st.Now().UTC().Format("20060102-150405")
	dbPath := filepath.Join(dir, "nsrelay-"+stamp+".db")
	if _, err := st.DB.ExecContext(ctx, `VACUUM INTO ?`, dbPath); err != nil {
		return "", fmt.Errorf("backup: %w", err)
	}
	if cfg, err := st.ExportConfig(ctx); err == nil {
		b, _ := yaml.Marshal(cfg)
		header := "# Netsession Relay configuration export " + stamp + " (readable copy; restore uses the .db file)\n"
		if err := os.WriteFile(filepath.Join(dir, "nsrelay-"+stamp+".yaml"), append([]byte(header), b...), 0o640); err != nil {
			return dbPath, err
		}
	}
	return dbPath, prune(dir, keep)
}

// LastBackup returns the time of the newest backup in dir (from its file name), zero if there is none.
func LastBackup(dir string) time.Time {
	m, _ := filepath.Glob(filepath.Join(dir, "nsrelay-*.db"))
	sort.Strings(m)
	for i := len(m) - 1; i >= 0; i-- {
		stamp := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m[i]), "nsrelay-"), ".db")
		if t, err := time.Parse("20060102-150405", stamp); err == nil {
			return t
		}
	}
	return time.Time{}
}

// BackupEvery is the rhythm of the automatic backup.
const BackupEvery = 24 * time.Hour

// AutoBackup writes a backup into dir whenever the newest one is older than BackupEvery (checked a minute after
// the start, then every hour) and keeps the newest keep. It replaces a timer on the host; backups written by
// `nsrelay backup` in between count, too. report gets every attempt. Returns when ctx ends.
func AutoBackup(ctx context.Context, st *store.Store, dir string, keep int, report func(path string, err error)) {
	wait := time.Minute
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = time.Hour
		if !backupDue(dir, st.Now()) {
			continue
		}
		path, err := Backup(ctx, st, dir, keep)
		report(path, err)
	}
}

// backupDue: no backup yet or the newest one is a day old (five minutes of slack for the hourly check).
func backupDue(dir string, now time.Time) bool {
	last := LastBackup(dir)
	return last.IsZero() || now.Sub(last) >= BackupEvery-5*time.Minute
}

func prune(dir string, keep int) error {
	if keep <= 0 {
		return nil
	}
	m, err := filepath.Glob(filepath.Join(dir, "nsrelay-*.db"))
	if err != nil {
		return err
	}
	sort.Strings(m) // timestamped names sort chronologically
	for len(m) > keep {
		old := m[0]
		m = m[1:]
		os.Remove(old)
		os.Remove(strings.TrimSuffix(old, ".db") + ".yaml")
	}
	return nil
}

// Restore replaces the database at dbPath with a backup file after checking it opens and has a schema.
// The current database is kept as <dbPath>.before-restore-<time>. The web part must be stopped.
func Restore(dbPath, backup string) (string, error) {
	db, err := sql.Open("sqlite", "file:"+backup+"?mode=ro")
	if err != nil {
		return "", err
	}
	var n int
	err = db.QueryRow(`SELECT count(*) FROM schema_migration`).Scan(&n)
	db.Close()
	if err != nil || n == 0 {
		return "", fmt.Errorf("%s is not a relay backup: %v", backup, err)
	}
	keep := dbPath + ".before-restore-" + time.Now().UTC().Format("20060102-150405")
	if _, err := os.Stat(dbPath); err == nil {
		if err := os.Rename(dbPath, keep); err != nil {
			return "", err
		}
		for _, suf := range []string{"-wal", "-shm"} {
			os.Remove(dbPath + suf)
		}
	} else {
		keep = ""
	}
	b, err := os.ReadFile(backup)
	if err != nil {
		return keep, err
	}
	return keep, os.WriteFile(dbPath, b, 0o640)
}
