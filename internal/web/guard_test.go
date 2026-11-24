package web

import (
	"testing"

	"github.com/Netsession-LAN/Netsession-Relay/internal/netif"
)

func TestGuardRelayIface(t *testing.T) {
	configured := map[string]bool{"eth1.1101": true}
	for _, c := range []struct {
		i    netif.HostIface
		want bool
	}{
		{netif.HostIface{Name: "eth1.1101"}, true},
		{netif.HostIface{Name: "nsr1102", Relay: true}, true},
		{netif.HostIface{Name: "eth0"}, false},
		{netif.HostIface{Name: "eth1.1101", DefaultRoute: true}, false}, // management side, even when configured
		{netif.HostIface{Name: "lo", Relay: true, Loopback: true}, false},
	} {
		if got := guardRelayIface(c.i, configured); got != c.want {
			t.Errorf("guardRelayIface(%+v) = %v", c.i, got)
		}
	}
	got := guardAddrsWhere([]netif.HostIface{{Addrs: []string{"10.0.0.1/24", "nonsense", "fd00::1/64"}}, {Name: "eth9", Addrs: []string{"10.9.9.9/24"}}}, func(i netif.HostIface) bool { return i.Name == "" })
	if len(got) != 2 || got[0].String() != "10.0.0.1" || got[1].String() != "fd00::1" {
		t.Errorf("guardAddrsWhere = %v", got)
	}
}
