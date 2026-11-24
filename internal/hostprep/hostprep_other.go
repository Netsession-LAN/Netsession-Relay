//go:build !linux

package hostprep

import (
	"errors"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
)

// Apply is only available on Linux.
func Apply(*compile.Snapshot) []Issue { return nil }

// Refresh is only available on Linux.
func Refresh(*compile.Snapshot) []Issue { return nil }

// Remove is only available on Linux.
func Remove() ([]string, error) { return nil, errors.New("hostprep: only available on Linux") }
