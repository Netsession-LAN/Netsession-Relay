// Package lab contains the test tools behind "nsrelay lab": a lab snapshot for the network-namespace lab
// (lab/up.sh) and raw packet send/expect helpers (Linux only).
package lab

import (
	"fmt"
	"net/netip"
	"slices"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
)

// VLANs is the lab layout (the same networks as the preset "lab" of internal/control): gameserver 1100 and
// tables 1101-1103, relay IP 10.10.N.<relayHost>, all on trunk parent.
func VLANs(parent string, relayHost int) []compile.VLAN {
	mk := func(vid int, name, group string, third int) compile.VLAN {
		return compile.VLAN{
			VID: vid, Name: name, Group: group, Enabled: true, Attach: compile.AttachTrunk, Parent: parent,
			CIDR:    netip.MustParsePrefix(fmt.Sprintf("10.10.%d.0/24", third)),
			RelayIP: netip.MustParseAddr(fmt.Sprintf("10.10.%d.%d", third, relayHost)),
		}
	}
	return []compile.VLAN{
		mk(1100, "Gameserver", compile.GroupGameserver, 0),
		mk(1101, "Tisch 1", compile.GroupTables, 1),
		mk(1102, "Tisch 2", compile.GroupTables, 2),
		mk(1103, "Tisch 3", compile.GroupTables, 3),
	}
}

// Snapshot compiles the lab configuration. games lists the game ids to switch on; empty means the defaults of
// the game database (a second instance for other ports, e.g. only "mdns", passes its own list).
func Snapshot(parent, mode string, relayHost int, games ...string) (*compile.Snapshot, []compile.Warning, error) {
	return SnapshotFiltered(parent, mode, relayHost, nil, games...)
}

// SnapshotFiltered is Snapshot with the same source filter (compile.Selection.Sources) on every switched-on game.
func SnapshotFiltered(parent, mode string, relayHost int, sources []netip.Prefix, games ...string) (*compile.Snapshot, []compile.Warning, error) {
	return SnapshotLimited(parent, mode, relayHost, sources, compile.Limits{}, games...)
}

// SnapshotLimited is SnapshotFiltered with engine limits (zero value: the defaults), e.g. a low storm_pps for
// the storm brake case.
func SnapshotLimited(parent, mode string, relayHost int, sources []netip.Prefix, limits compile.Limits, games ...string) (*compile.Snapshot, []compile.Warning, error) {
	db, err := gamedb.Seed()
	if err != nil {
		return nil, nil, err
	}
	var sel []compile.Selection
	for _, g := range db.Games {
		if (len(games) == 0 && g.DefaultEnabled) || slices.Contains(games, g.ID) {
			sel = append(sel, compile.Selection{GameID: g.ID, Sources: sources})
		}
	}
	prof := compile.DefaultFlowProfile()
	// the lab has no orga VLAN: drop edges to groups without VLANs
	var edges []compile.Edge
	for _, e := range prof.Edges {
		if e.Src != compile.GroupOrga && e.Dst != compile.GroupOrga {
			edges = append(edges, e)
		}
	}
	prof.Edges = edges
	return compile.Compile(compile.Input{
		Mode: mode, DB: db, VLANs: VLANs(parent, relayHost), Limits: limits,
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

// A2SOptions describe a fake Steam/Source server (A2S_INFO with challenge) for the server probe case.
type A2SOptions struct {
	Iface   string
	IP      netip.Addr // the server's address (answers are sent from it)
	Port    uint16     // default 27015
	Name    string
	Timeout int // milliseconds to keep answering
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
