//go:build !linux

package hostprep

import "github.com/Netsession-LAN/Netsession-Relay/internal/compile"

// Apply is only available on Linux.
func Apply(*compile.Snapshot) []Issue { return nil }

// Refresh is only available on Linux.
func Refresh(*compile.Snapshot) []Issue { return nil }
