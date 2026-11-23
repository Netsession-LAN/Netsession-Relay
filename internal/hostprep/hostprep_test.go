package hostprep

import (
	"net/netip"
	"slices"
	"testing"

	"github.com/Netsession2024/Netsession-Relay/internal/compile"
)

func TestPlanFor(t *testing.T) {
	vlan := func(vid int, attach, parent, device, cidr string) compile.VLAN {
		return compile.VLAN{VID: vid, Attach: attach, Parent: parent, Device: device, CIDR: netip.MustParsePrefix(cidr)}
	}
	s := &compile.Snapshot{VLANs: []compile.VLAN{
		vlan(11, compile.AttachTrunk, "ens19", "", "10.10.1.0/24"),
		vlan(12, compile.AttachTrunk, "ens19", "", "10.10.2.0/24"),
		vlan(20, compile.AttachDevice, "", "ens20", "10.10.20.0/24"),
		vlan(30, compile.AttachDevice, "", "eth0", "192.0.2.0/24"), // the relay on the management interface itself
		vlan(40, compile.AttachTrunk, "ens21", "", "10.10.40.0/24"),
		vlan(50, compile.AttachTrunk, "eth0", "", "10.10.50.0/24"), // VLANs on the management interface
	}}
	protected := map[string]bool{"eth0": true}
	hasAddr := func(n string) bool { return n == "ens21" } // a trunk that also carries an address of its own
	p := PlanFor(s, protected, hasAddr)
	want := []string{"ens19", "ens19.11", "ens19.12", "ens20", "ens21.40", "eth0.50"}
	if !slices.Equal(p.Ifaces, want) {
		t.Errorf("shielded interfaces %v, want %v (never the management interface, no trunk with an address)", p.Ifaces, want)
	}
	nets := []netip.Prefix{netip.MustParsePrefix("10.10.1.0/24"), netip.MustParsePrefix("10.10.2.0/24"), netip.MustParsePrefix("10.10.20.0/24"),
		netip.MustParsePrefix("10.10.40.0/24"), netip.MustParsePrefix("10.10.50.0/24")}
	if !slices.Equal(p.Networks, nets) {
		t.Errorf("networks %v, want %v", p.Networks, nets)
	}
	if p := PlanFor(&compile.Snapshot{}, protected, hasAddr); len(p.Ifaces) != 0 || len(p.Networks) != 0 {
		t.Errorf("empty snapshot: %+v", p)
	}
}
