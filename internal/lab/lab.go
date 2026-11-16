// Package lab contains the test tools behind "nsrelay lab": a lab snapshot for the network-namespace lab
// (lab/up.sh) and raw packet send/expect helpers (Linux only), see docs/02-plan.md 8.2.
package lab

import (
	"fmt"
	"net/netip"

	"github.com/Netsession2024/Netsession-Relay/internal/compile"
	"github.com/Netsession2024/Netsession-Relay/internal/gamedb"
)

// VLANs is the lab layout, a reduced copy of the event network: gameserver 1100 and tables 1101-1103,
// relay IP 10.10.N.<relayHost> (55 in the event network), all on trunk parent.
func VLANs(parent string, relayHost int) []compile.VLAN {
	mk := func(vid int, name, group string, third int) compile.VLAN {
		return compile.VLAN{
			VID: vid, Name: name, Group: group, Enabled: true, Attach: compile.AttachTrunk, Parent: parent,
			CIDR:    netip.MustParsePrefix(fmt.Sprintf("10.10.%d.0/24", third)),
			RelayIP: netip.MustParseAddr(fmt.Sprintf("10.10.%d.%d", third, relayHost)),
		}
	}
	return []compile.VLAN{
		mk(1100, "Gameserver", "gameserver", 0),
		mk(1101, "Tisch 1", "tische", 1),
		mk(1102, "Tisch 2", "tische", 2),
		mk(1103, "Tisch 3", "tische", 3),
	}
}

// GamesProfile is the default VLAN matrix (decision 2026-11-16): tables, gameserver and orga among each
// other in all directions; WiFi is not part of it.
func GamesProfile() compile.FlowProfile {
	groups := []string{"tische", "gameserver", "orga"}
	var edges []compile.Edge
	for _, a := range groups {
		for _, b := range groups {
			edges = append(edges, compile.Edge{Src: a, Dst: b})
		}
	}
	return compile.FlowProfile{Name: "spiele", Edges: edges}
}

// Snapshot compiles the seed database with all default-enabled games for the lab VLANs.
func Snapshot(parent, mode string, relayHost int) (*compile.Snapshot, []compile.Warning, error) {
	db, err := gamedb.Seed()
	if err != nil {
		return nil, nil, err
	}
	var sel []compile.Selection
	for _, g := range db.Games {
		if g.DefaultEnabled {
			sel = append(sel, compile.Selection{GameID: g.ID})
		}
	}
	prof := GamesProfile()
	// the lab has no orga VLAN: drop edges to groups without VLANs
	var edges []compile.Edge
	for _, e := range prof.Edges {
		if e.Src != "orga" && e.Dst != "orga" {
			edges = append(edges, e)
		}
	}
	prof.Edges = edges
	return compile.Compile(compile.Input{
		Mode: mode, DB: db, VLANs: VLANs(parent, relayHost),
		Profiles: []compile.FlowProfile{prof}, DefaultProfile: prof.Name, Games: sel,
	})
}

// SendOptions describe one test datagram.
type SendOptions struct {
	Iface    string
	Src      netip.Addr // may be spoofed (foreign-relay test)
	Dst      netip.Addr
	SrcPort  uint16
	DstPort  uint16
	Payload  []byte
	Count    int
	Interval int // milliseconds between frames
}

// ExpectOptions describe what lab expect waits for.
type ExpectOptions struct {
	Iface   string
	DstPort uint16
	Src     netip.Addr // optional filter
	Dst     netip.Addr // optional filter
	Count   int        // matches needed (default 1)
	Max     int        // >0: collect until timeout, succeed if 1..Max frames arrived (rate limit, duplicates)
	None    bool       // succeed only if nothing matches within the timeout
	Timeout int        // milliseconds
}
