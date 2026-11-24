package web

import (
	"testing"

	"github.com/Netsession-LAN/Netsession-Relay/internal/netif"
)

func TestHiddenFromTrunkChoice(t *testing.T) {
	for _, c := range []struct {
		i      netif.HostIface
		hidden bool
	}{
		{netif.HostIface{Name: "lo", Loopback: true}, true},
		{netif.HostIface{Name: "nsrelay10", Relay: true}, true},
		{netif.HostIface{Name: "ens19.10", Kind: "vlan"}, true},
		{netif.HostIface{Name: "wg0", Kind: "wireguard"}, true},
		{netif.HostIface{Name: "tun0", Kind: "tun"}, true},
		{netif.HostIface{Name: "docker0", Kind: "bridge"}, true},
		{netif.HostIface{Name: "br-1a2b", Kind: "bridge"}, true},
		{netif.HostIface{Name: "veth12", Kind: "veth"}, true},
		{netif.HostIface{Name: "eth0", Kind: "veth"}, false}, // the real interface inside a container
		{netif.HostIface{Name: "ens19", Kind: "device"}, false},
		{netif.HostIface{Name: "vmbr1", Kind: "bridge"}, false},
	} {
		if got := hiddenFromTrunkChoice(c.i); got != c.hidden {
			t.Errorf("%s (%s): hidden %v, want %v", c.i.Name, c.i.Kind, got, c.hidden)
		}
	}
}

func TestTrunkCandidate(t *testing.T) {
	mgmt := netif.HostIface{Name: "ens18", DefaultRoute: true, Addrs: []string{"10.0.0.5/24"}}
	if c := trunkCandidate(mgmt, false); c.Blocked == "" || c.Note != "" {
		t.Errorf("management interface without a list: %+v", c)
	}
	if c := trunkCandidate(mgmt, true); c.Blocked != "" || c.Note == "" {
		t.Errorf("management interface with a list of allowed interfaces: %+v", c)
	}
	v6 := netif.HostIface{Name: "ens19", Addrs: []string{"fd00::1/64"}}
	if c := trunkCandidate(v6, false); c.Blocked != "" || c.Note != "" {
		t.Errorf("interface with only IPv6: %+v", c)
	}
}

func TestSoleTrunkCandidate(t *testing.T) {
	free := func(name string) setupIface { return setupIface{HostIface: netif.HostIface{Name: name}} }
	blocked := func(name string) setupIface {
		return setupIface{HostIface: netif.HostIface{Name: name}, Blocked: "Verwaltung"}
	}
	for _, c := range []struct {
		ifs  []setupIface
		want string
	}{
		{nil, ""},
		{[]setupIface{blocked("ens18")}, ""},
		{[]setupIface{blocked("ens18"), free("ens19")}, "ens19"},
		{[]setupIface{free("ens19"), blocked("ens18")}, "ens19"},
		{[]setupIface{free("ens19"), free("ens20")}, ""},
	} {
		if got := soleTrunkCandidate(c.ifs); got != c.want {
			t.Errorf("%v: %q, want %q", c.ifs, got, c.want)
		}
	}
}
