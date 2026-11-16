//go:build !linux

package netif

import (
	"errors"

	"github.com/Netsession2024/Netsession-Relay/internal/compile"
)

var errNotLinux = errors.New("netif: only supported on Linux")

// Ensure is only available on Linux.
func Ensure(compile.VLAN) (Link, error) { return Link{}, errNotLinux }

// Cleanup is only available on Linux.
func Cleanup(map[string]bool) ([]string, error) { return nil, errNotLinux }

// SelfTest is only available on Linux.
func SelfTest() []string { return []string{errNotLinux.Error()} }
