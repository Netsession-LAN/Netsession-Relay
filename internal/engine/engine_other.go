//go:build !linux

package engine

import (
	"context"
	"errors"
	"log/slog"
)

// ErrNotSupported is returned by Run on non-Linux systems.
var ErrNotSupported = errors.New("engine: only supported on Linux")

// Engine runs the data plane from a snapshot file (Linux only).
type Engine struct {
	Path      string
	Socket    string
	SocketGID int
	Version   string
	Log       *slog.Logger
	Counters  Counters
}

// Run is only available on Linux.
func (e *Engine) Run(context.Context) error { return ErrNotSupported }
