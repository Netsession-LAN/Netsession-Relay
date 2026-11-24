// Package control is the control plane shared by the CLI (nsrelay admin) and the web UI: presets, apply with
// revision, rollback, backup and restore.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// Engine is the part of the engine API the control plane uses (engineapi.Client or a fake in tests).
type Engine interface {
	Validate(context.Context, *compile.Snapshot) (engineapi.Result, error)
	Apply(context.Context, *compile.Snapshot) (engineapi.Result, error)
}

// ApplyResult describes one apply.
type ApplyResult struct {
	Revision int64
	Snapshot *compile.Snapshot
	Warnings []compile.Warning
	Engine   engineapi.Result
	// Invalid is the compiler's error when the configuration does not compile (ErrConfigInvalid): every mistake,
	// one per line.
	Invalid error
}

// Errors of Apply, Rollback and Refresh (errors.Is); the message carries the detail.
var (
	// ErrConfigInvalid: the configuration does not compile (the wrapped error lists every mistake).
	ErrConfigInvalid = errors.New("configuration invalid")
	// ErrEngineUnreachable: the engine did not answer; nothing was applied and no revision recorded.
	ErrEngineUnreachable = errors.New("engine not reachable")
	// ErrEngineRejected: the engine refused the snapshot and keeps running the previous one; the revision is
	// recorded as not applied.
	ErrEngineRejected = errors.New("engine rejected the snapshot")
	// ErrRevisionNotFound: Rollback was given a revision that does not exist.
	ErrRevisionNotFound = errors.New("revision not found")
	// ErrRevisionRestore: the configuration of the revision could not be restored, typically because it names a
	// game that has been deleted since; the wrapped error has the detail.
	ErrRevisionRestore = errors.New("revision cannot be restored")
)

// Apply compiles the active configuration, sends it to the engine and records a revision (also when the
// engine rejects it). dryRun only compiles.
func Apply(ctx context.Context, st *store.Store, eng Engine, user, note string, dryRun bool) (ApplyResult, error) {
	var res ApplyResult
	// one read of the configuration: what is compiled is what the revision records
	cfg, err := st.ExportConfig(ctx)
	if err != nil {
		return res, err
	}
	in, err := st.InputFor(ctx, cfg)
	if err != nil {
		return res, err
	}
	snap, warns, err := compile.Compile(in)
	res.Warnings = warns
	if err != nil {
		res.Invalid = err
		return res, fmt.Errorf("%w:\n%w", ErrConfigInvalid, err)
	}
	res.Snapshot = snap
	if dryRun {
		return res, nil
	}
	res.Engine, err = eng.Apply(ctx, snap)
	if err != nil {
		return res, fmt.Errorf("%w: %w", ErrEngineUnreachable, err)
	}
	res.Revision, err = st.AddRevision(ctx, newRevision(user, note, cfg, snap, warns, res.Engine))
	if err != nil {
		return res, err
	}
	if !res.Engine.OK {
		_ = st.Audit(ctx, user, "apply.rejected", fmt.Sprintf("revision %d", res.Revision), "", snap.Hash)
		return res, fmt.Errorf("%w: %s", ErrEngineRejected, res.Engine.Error)
	}
	_ = st.Audit(ctx, user, "apply", fmt.Sprintf("revision %d", res.Revision), "", snap.Hash)
	return res, nil
}

// newRevision is the record of an apply the engine answered: the configuration that was compiled, the snapshot,
// the warnings and the engine's verdict.
func newRevision(user, note string, cfg store.Config, snap *compile.Snapshot, warns []compile.Warning, eng engineapi.Result) store.Revision {
	sj, _ := json.Marshal(snap)
	cj, _ := json.Marshal(cfg)
	ws := make([]string, len(warns))
	for i, w := range warns {
		ws[i] = w.String()
	}
	return store.Revision{User: user, Note: note, Hash: snap.Hash, Snapshot: string(sj), Config: string(cj), Warnings: ws,
		AppliedOK: eng.OK, EngineMessage: eng.Error}
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
	same, err := sameConfig(ctx, st, rev)
	if err != nil || !same {
		return 0, err
	}
	dry, err := Apply(ctx, st, eng, "", "", true)
	if err != nil || dry.Snapshot.Hash == rev.Hash {
		return 0, err
	}
	res, err := Apply(ctx, st, eng, "system", note, false)
	return res.Revision, err
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

// Rollback restores the configuration of a revision and applies it (a new revision is recorded).
func Rollback(ctx context.Context, st *store.Store, eng Engine, user string, id int64) (ApplyResult, error) {
	rev, err := st.RevisionByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return ApplyResult{}, fmt.Errorf("revision %d: %w", id, ErrRevisionNotFound)
	}
	if err != nil {
		return ApplyResult{}, err
	}
	var cfg store.Config
	if err := json.Unmarshal([]byte(rev.Config), &cfg); err != nil {
		return ApplyResult{}, fmt.Errorf("revision %d: %w: %w", id, ErrRevisionRestore, err)
	}
	if err := st.ImportConfig(ctx, user, cfg); err != nil {
		return ApplyResult{}, fmt.Errorf("revision %d: %w: %w", id, ErrRevisionRestore, err)
	}
	_ = st.Audit(ctx, user, "rollback", fmt.Sprintf("revision %d", id), "", "")
	return Apply(ctx, st, eng, user, fmt.Sprintf("rollback to revision %d", id), false)
}
