package netif

import (
	"testing"

	"github.com/Netsession2024/Netsession-Relay/internal/compile"
)

// The name of the interface of a VLAN: "<parent>.<vid>" on a trunk, the device itself otherwise. The fields of
// the other attach mode are ignored (an edited network may still carry them).
func TestLinkName(t *testing.T) {
	for _, c := range []struct {
		v    compile.VLAN
		want string
	}{
		{compile.VLAN{VID: 1101, Attach: compile.AttachTrunk, Parent: "trunk0"}, "trunk0.1101"},
		{compile.VLAN{VID: 7, Attach: compile.AttachTrunk, Parent: "ens19", Device: "eth9"}, "ens19.7"},
		{compile.VLAN{VID: 4094, Attach: compile.AttachTrunk, Parent: "bond0.10"}, "bond0.10.4094"},
		{compile.VLAN{VID: 1101, Attach: compile.AttachDevice, Device: "ens20"}, "ens20"},
		{compile.VLAN{VID: 1101, Attach: compile.AttachDevice, Device: "ens20", Parent: "trunk0"}, "ens20"},
	} {
		if got := LinkName(c.v); got != c.want {
			t.Errorf("LinkName(%+v) = %q, want %q", c.v, got, c.want)
		}
	}
}

// Cleanup may only ever delete what the engine created itself and no longer needs: the alias decides, never
// the name.
func TestStale(t *testing.T) {
	keep := map[string]bool{"trunk0.1101": true}
	for _, c := range []struct {
		name, alias string
		want        bool
	}{
		{"trunk0.1102", Alias, true},        // ours, not needed any more
		{"trunk0.1101", Alias, false},       // ours, still needed
		{"trunk0.1103", "", false},          // looks like ours but carries no alias: somebody else's VLAN
		{"trunk0.1104", "uplink", false},    // another alias
		{"eth0", "", false},                 // the management interface
		{"trunk0.1105", "nsrelay ", false},  // not exactly the alias
		{"trunk0.1106", "NSRELAY", false},   // the alias is case sensitive
		{"anything-at-all", Alias, true},    // the name does not matter
		{"trunk0.1101", "something", false}, // needed and foreign
	} {
		if got := stale(c.name, c.alias, keep); got != c.want {
			t.Errorf("stale(%q, alias %q) = %v, want %v", c.name, c.alias, got, c.want)
		}
	}
	if !stale("trunk0.1101", Alias, nil) {
		t.Error("with nothing to keep, every link of the engine is stale")
	}
}
