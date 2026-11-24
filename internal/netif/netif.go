// Package netif manages the relay's VLAN interfaces via netlink and runs the host self-test (Linux only).
//
// Trunk attach: the engine creates "<parent>.<vid>" (802.1Q) on the trunk interface, marks it with the
// link alias "nsrelay", brings it up and assigns the relay IP. Device attach: the interface already exists
// (one vNIC per VLAN); the engine only brings it up and assigns the relay IP.
package netif

import (
	"fmt"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

// Alias marks links created by the engine; Cleanup only ever deletes links carrying it.
const Alias = "nsrelay"

// stale reports whether Cleanup may delete a link: the engine created it (it carries the alias) and no VLAN of
// the current configuration uses it any more. A link without the alias is never touched, whatever it is called.
func stale(name, alias string, keep map[string]bool) bool {
	return alias == Alias && !keep[name]
}

// Link is a ready-to-use VLAN interface.
type Link struct {
	Name  string
	Index int
	MAC   pkt.MAC
}

// LinkName returns the interface name the engine uses for a VLAN.
func LinkName(v compile.VLAN) string {
	if v.Attach == compile.AttachDevice {
		return v.Device
	}
	return fmt.Sprintf("%s.%d", v.Parent, v.VID)
}
