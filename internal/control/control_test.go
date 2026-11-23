package control

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Netsession2024/Netsession-Relay/internal/compile"
	"github.com/Netsession2024/Netsession-Relay/internal/engineapi"
	"github.com/Netsession2024/Netsession-Relay/internal/store"
)

type fakeEngine struct {
	applied []*compile.Snapshot
	reject  string
}

func (f *fakeEngine) Validate(_ context.Context, s *compile.Snapshot) (engineapi.Result, error) {
	return engineapi.Result{OK: f.reject == "", Error: f.reject, Hash: s.Hash}, nil
}

func (f *fakeEngine) Apply(_ context.Context, s *compile.Snapshot) (engineapi.Result, error) {
	if f.reject != "" {
		return engineapi.Result{Error: f.reject, Hash: s.Hash}, nil
	}
	f.applied = append(f.applied, s)
	return engineapi.Result{OK: true, Hash: s.Hash}, nil
}

func (f *fakeEngine) Status(context.Context) (engineapi.Status, error) {
	if len(f.applied) == 0 {
		return engineapi.Status{}, nil
	}
	return engineapi.Status{SnapshotHash: f.applied[len(f.applied)-1].Hash}, nil
}

func setup(t *testing.T, preset string) (*store.Store, *fakeEngine) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if _, err := Init(context.Background(), st, "test", preset, "trunk0", false); err != nil {
		t.Fatal(err)
	}
	return st, &fakeEngine{}
}

func hasRule(s *compile.Snapshot, ports string) bool {
	for _, r := range s.Rules {
		if r.Ports == ports {
			return true
		}
	}
	return false
}

func TestInitNetsessionPreset(t *testing.T) {
	st, eng := setup(t, "netsession")
	res, err := Apply(context.Background(), st, eng, "test", "first", false)
	if err != nil {
		t.Fatal(err)
	}
	s := res.Snapshot
	if len(s.VLANs) != 17 || !hasRule(s, "23757") || res.Revision != 1 {
		t.Fatalf("vlans=%d flatout=%v rev=%d", len(s.VLANs), hasRule(s, "23757"), res.Revision)
	}
	// same result as the compiler golden test: the preset is the real event network
	golden, err := os.ReadFile(filepath.Join("..", "compile", "testdata", "example_snapshot.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g compile.Snapshot
	if err := json.Unmarshal(golden, &g); err != nil {
		t.Fatal(err)
	}
	if s.Hash != g.Hash {
		t.Errorf("netsession preset snapshot %s differs from the compiler golden example %s", s.Hash[:12], g.Hash[:12])
	}
	if _, err := Init(context.Background(), st, "test", "netsession", "trunk0", false); err == nil {
		t.Error("second init without -force accepted")
	}
}

func TestEnableApplyRollback(t *testing.T) {
	ctx := context.Background()
	st, eng := setup(t, "lab")
	r1, err := Apply(ctx, st, eng, "test", "", false)
	if err != nil || !hasRule(r1.Snapshot, "23757") {
		t.Fatalf("rev1: %v", err)
	}
	for _, id := range []string{"flatout1", "flatout2"} {
		if err := st.SetGameEnabled(ctx, "test", id, false); err != nil {
			t.Fatal(err)
		}
	}
	r2, err := Apply(ctx, st, eng, "test", "flatout off", false)
	if err != nil || hasRule(r2.Snapshot, "23757") {
		t.Fatalf("rev2 still has FlatOut: %v", err)
	}
	r3, err := Rollback(ctx, st, eng, "test", r1.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !hasRule(r3.Snapshot, "23757") || r3.Snapshot.Hash != r1.Snapshot.Hash {
		t.Errorf("rollback did not restore revision 1 (hash %s vs %s)", r3.Snapshot.Hash[:8], r1.Snapshot.Hash[:8])
	}
	revs, _ := st.Revisions(ctx, 10)
	if len(revs) != 3 || revs[0].Note != "rollback to revision 1" {
		t.Errorf("revisions %+v", revs)
	}
	games, _ := st.Games(ctx)
	for _, g := range games {
		if g.ID == "flatout2" && !g.Enabled {
			t.Error("flatout2 not enabled again after rollback")
		}
	}
	if len(eng.applied) != 3 {
		t.Errorf("engine got %d applies", len(eng.applied))
	}
	if err := st.SetGameEnabled(ctx, "test", "nope", true); err == nil {
		t.Error("unknown game accepted")
	}
}

func TestEngineRejectRecorded(t *testing.T) {
	ctx := context.Background()
	st, eng := setup(t, "lab")
	eng.reject = "trunk interface \"trunk0\" missing"
	_, err := Apply(ctx, st, eng, "test", "", false)
	if err == nil || !strings.Contains(err.Error(), "rejected") {
		t.Fatalf("err = %v", err)
	}
	revs, _ := st.Revisions(ctx, 1)
	if len(revs) != 1 || revs[0].AppliedOK || revs[0].EngineMessage == "" {
		t.Errorf("rejected revision not recorded: %+v", revs)
	}
}

func TestDryRunAndModes(t *testing.T) {
	ctx := context.Background()
	st, eng := setup(t, "lab")
	if err := st.SetMode(ctx, "test", compile.ModeObserve); err != nil {
		t.Fatal(err)
	}
	res, err := Apply(ctx, st, eng, "test", "", true)
	if err != nil || res.Snapshot.Mode != compile.ModeObserve || len(eng.applied) != 0 || res.Revision != 0 {
		t.Fatalf("dry run: %v mode=%s applied=%d", err, res.Snapshot.Mode, len(eng.applied))
	}
}

func TestBackupRestorePrune(t *testing.T) {
	ctx := context.Background()
	st, _ := setup(t, "lab")
	dir := t.TempDir()
	clock := time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC)
	st.Now = func() time.Time { return clock }
	var paths []string
	for i := range 3 {
		clock = clock.Add(time.Duration(i+1) * time.Minute)
		p, err := Backup(ctx, st, dir, 2)
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	dbs, _ := filepath.Glob(filepath.Join(dir, "*.db"))
	yamls, _ := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if len(dbs) != 2 || len(yamls) != 2 || slices.Contains(dbs, paths[0]) {
		t.Fatalf("after prune: %v %v", dbs, yamls)
	}
	y, _ := os.ReadFile(strings.TrimSuffix(paths[2], ".db") + ".yaml")
	if !strings.Contains(string(y), "flatout2") || !strings.Contains(string(y), "vid: 1101") {
		t.Error("yaml export incomplete")
	}
	// restore into a new path and open it
	target := filepath.Join(t.TempDir(), "restored.db")
	if _, err := Restore(target, paths[2]); err != nil {
		t.Fatal(err)
	}
	st2, err := store.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	if ev, err := st2.ActiveEvent(ctx); err != nil || ev != "Labor" {
		t.Errorf("restored event %q %v", ev, err)
	}
	if _, err := Restore(target, filepath.Join(dir, "missing.db")); err == nil {
		t.Error("restore of a missing file accepted")
	}
}

// Refresh applies again only when the engine runs the latest revision, the configuration is unchanged and a
// new compile differs (a changed setting stands in for a new compiler here).
func TestRefreshAfterUpdate(t *testing.T) {
	ctx := context.Background()
	st, eng := setup(t, "netsession")
	if _, err := Apply(ctx, st, eng, "test", "first", false); err != nil {
		t.Fatal(err)
	}
	if rev, err := Refresh(ctx, st, eng, "update"); err != nil || rev != 0 {
		t.Fatalf("nothing changed: rev=%d err=%v", rev, err)
	}
	if err := st.SetSetting(ctx, store.SettingProbeServers, "0"); err != nil {
		t.Fatal(err)
	}
	rev, err := Refresh(ctx, st, eng, "update")
	if err != nil || rev != 2 || len(eng.applied) != 2 || eng.applied[1].Probe {
		t.Fatalf("compile changed: rev=%d err=%v applied=%d", rev, err, len(eng.applied))
	}
	if r, _ := st.RevisionByID(ctx, rev); r.User != "system" || r.Note != "update" {
		t.Errorf("revision by %q note %q", r.User, r.Note)
	}
	if rev, err := Refresh(ctx, st, eng, "update"); err != nil || rev != 0 {
		t.Fatalf("second refresh: rev=%d err=%v", rev, err)
	}

	// configuration changed but not applied: left alone
	if err := st.SetSetting(ctx, store.SettingProbeServers, "1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMode(ctx, "test", compile.ModeObserve); err != nil {
		t.Fatal(err)
	}
	if rev, err := Refresh(ctx, st, eng, "update"); err != nil || rev != 0 {
		t.Fatalf("unapplied change: rev=%d err=%v", rev, err)
	}
	if err := st.SetMode(ctx, "test", compile.ModeActive); err != nil {
		t.Fatal(err)
	}
	// engine runs something else (e.g. a snapshot file put there by hand): left alone
	eng.applied = append(eng.applied, &compile.Snapshot{Hash: "other"})
	if rev, err := Refresh(ctx, st, eng, "update"); err != nil || rev != 0 {
		t.Fatalf("foreign snapshot: rev=%d err=%v", rev, err)
	}
}
