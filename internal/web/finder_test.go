package web

import (
	"net/netip"
	"testing"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
)

func TestFinderCells(t *testing.T) {
	for _, c := range []struct {
		got, want string
	}{
		{finderSize(48, 48), "48 B"},
		{finderSize(48, 120), "48–120 B"},
		{finderLeft(90 * time.Second), "1:30"},
		{finderLeft(9 * time.Second), "0:09"},
		{finderSrcs(nil), ""},
		{finderSrcs([]netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("10.0.0.2")}), "10.0.0.1, 10.0.0.2"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestFinderRowNetsAndNoise(t *testing.T) {
	rc := finderRowCtx{vlanName: map[int]string{1101: "Tisch A"}}
	if got := rc.nets([]int{1101, 1102}); got != "Tisch A, VLAN 1102" {
		t.Errorf("nets = %q", got)
	}
	for _, c := range []struct {
		g         engineapi.FinderGroup
		showNoise bool
		hidden    bool
	}{
		{engineapi.FinderGroup{Kind: gamedb.KindBroadcast, Port: 5353}, false, true},
		{engineapi.FinderGroup{Kind: gamedb.KindBroadcast, Port: 5353}, true, false},
		{engineapi.FinderGroup{Kind: gamedb.KindBroadcast, Port: 23757}, false, false},
		{engineapi.FinderGroup{Kind: gamedb.KindMulticast, Group: "239.255.255.250", Port: 1900}, false, false},
	} {
		rc.showNoise = c.showNoise
		if got := rc.hidden(c.g); got != c.hidden {
			t.Errorf("hidden(%+v, showNoise=%v) = %v", c.g, c.showNoise, got)
		}
	}
}
