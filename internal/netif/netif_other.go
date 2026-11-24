//go:build !linux

package netif

import (
	"errors"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
)

var errNotLinux = errors.New("netif: only supported on Linux")

// Ensure is only available on Linux.
func Ensure(compile.VLAN) (Link, error) { return Link{}, errNotLinux }

// Cleanup is only available on Linux.
func Cleanup(map[string]bool) ([]string, error) { return nil, errNotLinux }

// DefaultRouteLinks is only available on Linux.
func DefaultRouteLinks() (map[string]bool, error) { return nil, errNotLinux }

// HardenLink is only available on Linux.
func HardenLink(string) error { return errNotLinux }

// DisableIPv6 is only available on Linux.
func DisableIPv6(string) error { return errNotLinux }

// HasIPv4 is only available on Linux.
func HasIPv4(string) bool { return false }

// Interfaces is only available on Linux.
func Interfaces() ([]HostIface, error) { return nil, errNotLinux }
