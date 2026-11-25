package control

import (
	"context"
	"encoding/json"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// Refresh after an update of the relay software: a new revision when the unchanged configuration compiles
// differently now.

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
	rev, ok, err := latestRevision(ctx, st)
	if err != nil || !ok {
		return 0, err
	}
	outdated, err := outdatedSnapshot(ctx, st, eng, rev)
	if err != nil || !outdated {
		return 0, err
	}
	res, err := Apply(ctx, st, eng, "system", note, false)
	return res.Revision, err
}

// latestRevision returns the newest revision with its configuration; ok is false when there is none.
func latestRevision(ctx context.Context, st *store.Store) (rev store.Revision, ok bool, err error) {
	revs, err := st.Revisions(ctx, 1)
	if err != nil || len(revs) == 0 {
		return rev, false, err
	}
	rev, err = st.RevisionByID(ctx, revs[0].ID)
	return rev, err == nil, err
}

// outdatedSnapshot reports whether rev is what runs and what is configured, but compiling it now gives another
// snapshot: the case Refresh applies again.
func outdatedSnapshot(ctx context.Context, st *store.Store, eng StatusEngine, rev store.Revision) (bool, error) {
	if running, err := runsRevision(ctx, eng, rev); err != nil || !running {
		return false, err
	}
	if same, err := sameConfig(ctx, st, rev); err != nil || !same {
		return false, err
	}
	return compilesDifferently(ctx, st, eng, rev)
}

// compilesDifferently reports whether compiling the configuration now gives another snapshot than rev.
func compilesDifferently(ctx context.Context, st *store.Store, eng Engine, rev store.Revision) (bool, error) {
	dry, err := Apply(ctx, st, eng, "", "", true)
	if err != nil {
		return false, err
	}
	return dry.Snapshot.Hash != rev.Hash, nil
}

// runsRevision reports whether the engine runs the snapshot of rev and rev was applied successfully.
func runsRevision(ctx context.Context, eng StatusEngine, rev store.Revision) (bool, error) {
	status, err := eng.Status(ctx)
	if err != nil {
		return false, err
	}
	return rev.AppliedOK && status.SnapshotHash == rev.Hash, nil
}

// sameConfig reports whether the active configuration is still the one the revision recorded. A revision whose
// configuration cannot be read counts as different: it is left alone like any other mismatch, retrying would
// not help.
func sameConfig(ctx context.Context, st *store.Store, rev store.Revision) (bool, error) {
	old, ok := recordedConfig(rev)
	if !ok {
		return false, nil
	}
	cur, err := st.ExportConfig(ctx)
	if err != nil {
		return false, err
	}
	a, _ := json.Marshal(old)
	b, _ := json.Marshal(cur)
	return string(a) == string(b), nil
}

// recordedConfig decodes the configuration a revision recorded; ok is false when it cannot be read.
func recordedConfig(rev store.Revision) (store.Config, bool) {
	var c store.Config
	err := json.Unmarshal([]byte(rev.Config), &c)
	return c, err == nil
}
