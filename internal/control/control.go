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
	cfg, res, err := compileActive(ctx, st)
	if err != nil || dryRun {
		return res, err
	}
	res.Engine, err = eng.Apply(ctx, res.Snapshot)
	if err != nil {
		return res, fmt.Errorf("%w: %w", ErrEngineUnreachable, err)
	}
	err = recordApply(ctx, st, user, note, cfg, &res)
	return res, err
}

// compileActive reads the active configuration and compiles it. It is read once: what is compiled is what the
// revision records.
func compileActive(ctx context.Context, st *store.Store) (store.Config, ApplyResult, error) {
	var res ApplyResult
	cfg, err := st.ExportConfig(ctx)
	if err != nil {
		return cfg, res, err
	}
	in, err := st.InputFor(ctx, cfg)
	if err != nil {
		return cfg, res, err
	}
	snap, warns, err := compile.Compile(in)
	res.Warnings = warns
	if err != nil {
		res.Invalid = err
		return cfg, res, fmt.Errorf("%w:\n%w", ErrConfigInvalid, err)
	}
	res.Snapshot = snap
	return cfg, res, nil
}

// recordApply records the revision of an apply the engine answered and audits it; a rejection is an error.
func recordApply(ctx context.Context, st *store.Store, user, note string, cfg store.Config, res *ApplyResult) error {
	var err error
	res.Revision, err = st.AddRevision(ctx, newRevision(user, note, cfg, res.Snapshot, res.Warnings, res.Engine))
	if err != nil {
		return err
	}
	if !res.Engine.OK {
		_ = st.Audit(ctx, user, "apply.rejected", fmt.Sprintf("revision %d", res.Revision), "", res.Snapshot.Hash)
		return fmt.Errorf("%w: %s", ErrEngineRejected, res.Engine.Error)
	}
	_ = st.Audit(ctx, user, "apply", fmt.Sprintf("revision %d", res.Revision), "", res.Snapshot.Hash)
	return nil
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
