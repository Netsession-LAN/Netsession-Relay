//go:build linux

package engine

import (
	"fmt"
	"net"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/netif"
)

// Checking a snapshot without touching interfaces (engine API), and whether the interfaces it names exist.

// Validate checks a snapshot without touching interfaces (engine API).
func (e *Engine) Validate(s *compile.Snapshot) error {
	if s.Version != compile.SnapshotVersion {
		return fmt.Errorf("snapshot version %d, engine supports %d", s.Version, compile.SnapshotVersion)
	}
	if _, err := NewTable(s, nil); err != nil {
		return err
	}
	for _, v := range s.VLANs {
		if err := checkIfaceExists(v); err != nil {
			return err
		}
	}
	return e.checkIfaces(s)
}

// checkIfaceExists checks that the interface a VLAN is set up on (its trunk or device) exists on the host.
func checkIfaceExists(v compile.VLAN) error {
	if v.Attach == compile.AttachTrunk {
		if _, err := net.InterfaceByName(v.Parent); err != nil {
			return fmt.Errorf("VLAN %d: trunk interface %q: %w", v.VID, v.Parent, err)
		}
		return nil
	}
	if _, err := net.InterfaceByName(v.Device); err != nil {
		return fmt.Errorf("VLAN %d: interface %q: %w", v.VID, v.Device, err)
	}
	return nil
}

// checkIfaces applies the interface policy (CheckIfaces) with the host's current default routes.
func (e *Engine) checkIfaces(s *compile.Snapshot) error {
	var protected map[string]bool
	if len(e.AllowedIfaces) == 0 {
		var err error
		if protected, err = netif.DefaultRouteLinks(); err != nil {
			return fmt.Errorf("cannot read the routes of the host to protect the management interface: %w", err)
		}
	}
	return CheckIfaces(s, e.AllowedIfaces, protected)
}
