//go:build linux

package netif

import (
	"slices"
	"strings"
	"testing"

	"github.com/Netsession2024/Netsession-Relay/internal/compile"
)

// The parts that only read the state of the host run without privileges; creating and deleting interfaces is
// covered by the network lab (lab/test.sh).

func TestInterfacesListsTheLoopback(t *testing.T) {
	ifs, err := Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(ifs, func(h HostIface) bool { return h.Loopback })
	if i < 0 {
		t.Fatalf("no loopback interface in %+v", ifs)
	}
	lo := ifs[i]
	if lo.Relay || lo.DefaultRoute {
		t.Errorf("loopback marked as relay or management interface: %+v", lo)
	}
	if lo.Up && !slices.ContainsFunc(lo.Addrs, func(a string) bool { return strings.HasPrefix(a, "127.") }) {
		t.Errorf("loopback is up but lists no 127.x address: %+v", lo.Addrs)
	}
	if !slices.IsSortedFunc(ifs, func(a, b HostIface) int { return strings.Compare(a.Name, b.Name) }) {
		t.Error("interfaces are not sorted by name")
	}
	for _, h := range ifs {
		for _, a := range h.Addrs {
			if strings.HasPrefix(strings.ToLower(a), "fe80:") {
				t.Errorf("%s lists the IPv6 link-local address %s", h.Name, a)
			}
		}
	}
}

func TestDefaultRouteLinksNamesExistingInterfaces(t *testing.T) {
	def, err := DefaultRouteLinks()
	if err != nil {
		t.Fatal(err)
	}
	ifs, err := Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for name := range def {
		i := slices.IndexFunc(ifs, func(h HostIface) bool { return h.Name == name })
		if i < 0 || !ifs[i].DefaultRoute || ifs[i].Loopback {
			t.Errorf("default route on %q, which Interfaces does not mark as the management side", name)
		}
	}
}

func TestMissingInterfaces(t *testing.T) {
	const nope = "nsrelay-nope0"
	if HasIPv4(nope) {
		t.Error("an interface that does not exist has an IPv4 address")
	}
	if err := HardenLink(nope); err == nil {
		t.Error("HardenLink on a missing interface succeeded")
	}
	_, err := Ensure(compile.VLAN{VID: 1101, Name: "Tisch 1", Attach: compile.AttachDevice, Device: nope})
	if err == nil || !strings.Contains(err.Error(), "VLAN 1101") || !strings.Contains(err.Error(), nope) {
		t.Errorf("device that does not exist: %v, want an error that names the VLAN and the interface", err)
	}
	_, err = Ensure(compile.VLAN{VID: 1101, Attach: compile.AttachTrunk, Parent: nope})
	if err == nil || !strings.Contains(err.Error(), "trunk interface") {
		t.Errorf("trunk that does not exist: %v", err)
	}
	if _, err := Ensure(compile.VLAN{VID: 1101, Attach: "bridge"}); err == nil {
		t.Error("unknown attach mode accepted")
	}
}
