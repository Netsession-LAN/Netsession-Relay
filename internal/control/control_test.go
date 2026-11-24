package control

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/Netsession2024/Netsession-Relay/internal/gamedb"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Netsession2024/Netsession-Relay/internal/compile"
	"github.com/Netsession2024/Netsession-Relay/internal/engineapi"
	"github.com/Netsession2024/Netsession-Relay/internal/lab"
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
	if !errors.Is(err, ErrEngineRejected) {
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

// Every preset compiles; the lab preset is the layout of the network lab, and an unknown name is refused with
// an error that has an identity.
func TestPresets(t *testing.T) {
	ctx := context.Background()
	for preset, vlans := range map[string]int{PresetLAN: 5, PresetNetsession: 17, PresetLab: 4} {
		st, eng := setup(t, preset)
		res, err := Apply(ctx, st, eng, "test", "first", false)
		if err != nil || len(res.Snapshot.VLANs) != vlans || !hasRule(res.Snapshot, "23757") {
			t.Errorf("preset %s: %v, %d networks (want %d)", preset, err, len(res.Snapshot.VLANs), vlans)
		}
	}
	st, eng := setup(t, PresetLab)
	res, err := Apply(ctx, st, eng, "test", "", false)
	if err != nil {
		t.Fatal(err)
	}
	labSnap, _, err := lab.Snapshot("trunk0", compile.ModeActive, 55)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Snapshot.VLANs, labSnap.VLANs) || !slices.EqualFunc(res.Snapshot.Rules, labSnap.Rules, func(a, b compile.Rule) bool { return a.ID == b.ID }) {
		t.Errorf("preset lab and the lab snapshot differ:\n%+v\n%+v", res.Snapshot.VLANs, labSnap.VLANs)
	}
	db, err := st.GameDB(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Preset("nope", "trunk0", db); !errors.Is(err, ErrUnknownPreset) {
		t.Errorf("unknown preset: %v, want ErrUnknownPreset", err)
	}
}

type deadEngine struct{ fakeEngine }

func (deadEngine) Apply(context.Context, *compile.Snapshot) (engineapi.Result, error) {
	return engineapi.Result{}, errors.New("dial unix /run/nsrelay/engine.sock: connect: no such file or directory")
}

// The three ways an apply fails are told apart by identity, not by their wording.
func TestApplyErrorsHaveIdentity(t *testing.T) {
	ctx := context.Background()
	st, eng := setup(t, PresetLab)
	eng.reject = "trunk interface missing"
	if _, err := Apply(ctx, st, eng, "test", "", false); !errors.Is(err, ErrEngineRejected) || errors.Is(err, ErrConfigInvalid) {
		t.Errorf("rejected snapshot: %v", err)
	}
	if _, err := Apply(ctx, st, &deadEngine{}, "test", "", false); !errors.Is(err, ErrEngineUnreachable) {
		t.Errorf("engine that does not answer: %v", err)
	}
	if revs, _ := st.Revisions(ctx, 10); len(revs) != 1 {
		t.Errorf("%d revisions, want only the rejected one (nothing is recorded when the engine does not answer)", len(revs))
	}
	// two networks with the same address range: the configuration does not compile
	v, err := st.VLAN(ctx, 1101)
	if err != nil {
		t.Fatal(err)
	}
	v.VID, v.Name = 1150, "Doppelt"
	if err := st.AddVLAN(ctx, "test", v); err != nil {
		t.Fatal(err)
	}
	_, err = Apply(ctx, st, &fakeEngine{}, "test", "", false)
	if !errors.Is(err, ErrConfigInvalid) || !errors.Is(err, compile.ErrOverlap) {
		t.Errorf("overlapping networks: %v, want ErrConfigInvalid and compile.ErrOverlap", err)
	}
}

// A backup holds password hashes and the secrets of the monitoring targets: only the owner may read it.
func TestBackupIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no permission bits on Windows")
	}
	st, _ := setup(t, PresetLab)
	dir := t.TempDir()
	p, err := Backup(context.Background(), st, dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{p, strings.TrimSuffix(p, ".db") + ".yaml"} {
		fi, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s has permissions %o, want 600", filepath.Base(f), perm)
		}
	}
}

// A rollback names what went wrong: a revision that does not exist, or one whose configuration cannot be
// restored any more (here: it switches on a game that has been deleted since).
func TestRollbackErrors(t *testing.T) {
	ctx := context.Background()
	st, eng := setup(t, "lab")
	if _, err := Rollback(ctx, st, eng, "test", 999); !errors.Is(err, ErrRevisionNotFound) {
		t.Errorf("missing revision: %v", err)
	}
	g := gamedb.Game{ID: "custom_game", Name: "Custom Game", Category: gamedb.CategoryGame, RelayNeeded: gamedb.RelayYes,
		Confidence: "high", SourceIP: gamedb.SourceOriginal, Discovery: []gamedb.Spec{{Kind: gamedb.KindBroadcast, Ports: "4711"}}}
	if err := st.AddGame(ctx, "test", "custom", g); err != nil {
		t.Fatal(err)
	}
	if err := st.SetGameEnabled(ctx, "test", g.ID, true); err != nil {
		t.Fatal(err)
	}
	r1, err := Apply(ctx, st, eng, "test", "with custom game", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteGame(ctx, "test", g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := Rollback(ctx, st, eng, "test", r1.Revision); !errors.Is(err, ErrRevisionRestore) {
		t.Errorf("revision with a deleted game: %v", err)
	}
}
