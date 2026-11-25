package web

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/portspec"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// The rows of the port finder: what was seen on a port, the games of the database that use it, and a sample of the
// payload.

// noisePorts are broadcasts every LAN has; the port finder can hide them.
var noisePorts = map[uint16]string{
	137: "NetBIOS", 138: "NetBIOS", 1900: "SSDP", 3702: "WS-Discovery", 5353: "mDNS", 5355: "LLMNR",
	17500: "Dropbox", 27036: "Steam", 57621: "Spotify", 6646: "McAfee", 1947: "Sentinel",
}

// gameHint is a game of the database that uses a port, with its state in the active event.
type gameHint struct {
	ID, Name string
	On       bool
}

type finderRow struct {
	Dest     string // "Broadcast" (translated by the template) or the multicast group
	Kind     string
	Group    string
	Port     uint16
	Rule     string
	Games    []gameHint // games of the active rule, or of the database for an unknown port
	Noise    string
	Count    uint64
	Interval string
	Size     string
	Srcs     string
	Nets     string
	New      bool
	Offset   string
	Hex      string
	ASCII    string
}

type unknownRow struct {
	Port  uint16
	Count uint32
	Noise string
	Games []gameHint
}

// portGames finds database games whose discovery uses kind/group/port.
func portGames(games []store.GameRow, kind, group string, port uint16) []gameHint {
	uses := func(d gamedb.Spec) bool { return specUsesPort(d, kind, group, port) }
	var out []gameHint
	for _, g := range games {
		if slices.ContainsFunc(g.Discovery, uses) {
			out = append(out, gameHint{ID: g.ID, Name: g.Name, On: g.Enabled})
		}
	}
	return out
}

// specUsesPort reports whether a discovery spec covers kind/port (and, for multicast, the group).
func specUsesPort(d gamedb.Spec, kind, group string, port uint16) bool {
	if d.Kind != kind || (kind == gamedb.KindMulticast && d.Group != group) {
		return false
	}
	ps, err := portspec.Parse(d.Ports)
	return err == nil && ps.Contains(port)
}

func hexASCII(b []byte) (string, string) {
	var h, a strings.Builder
	for i, c := range b {
		if i > 0 && i%4 == 0 {
			h.WriteByte(' ')
		}
		fmt.Fprintf(&h, "%02x", c)
		a.WriteByte(printableOrDot(c))
	}
	return h.String(), a.String()
}

// printableOrDot is how the ASCII column of the sample shows a byte: printable ASCII as itself, anything else
// as '.'.
func printableOrDot(c byte) byte {
	if c >= 0x20 && c < 0x7f {
		return c
	}
	return '.'
}

// finderRowCtx is what turning the engine's finder groups into table rows needs besides the group itself.
type finderRowCtx struct {
	games     []store.GameRow
	gameByID  map[string]gameHint
	idx       *ruleIndex
	vlanName  map[int]string
	started   time.Time
	showNoise bool
}

// finderUnknownRows lists the busiest broadcast ports without a rule, with the database games that use them.
func finderUnknownRows(ports []engineapi.PortCount, games []store.GameRow) []unknownRow {
	var out []unknownRow
	for _, p := range ports {
		out = append(out, unknownRow{Port: p.Port, Count: p.Count, Noise: noisePorts[p.Port],
			Games: portGames(games, gamedb.KindBroadcast, "", p.Port)})
	}
	return out
}

// hidden reports whether a group is background noise of every LAN and noise is not shown. Multicast groups are
// never noise.
func (rc finderRowCtx) hidden(g engineapi.FinderGroup) bool {
	return g.Kind != gamedb.KindMulticast && noisePorts[g.Port] != "" && !rc.showNoise
}

// row turns one group the finder saw into a table row.
func (rc finderRowCtx) row(g engineapi.FinderGroup) finderRow {
	row := finderRow{Kind: g.Kind, Group: g.Group, Port: g.Port, Rule: g.Rule, Count: g.Count, New: g.New,
		Noise: noisePorts[g.Port], Dest: "Broadcast"}
	if g.Kind == gamedb.KindMulticast {
		row.Dest, row.Noise = g.Group, ""
	}
	row.Games = rc.rowGames(g)
	if g.Count > 1 {
		row.Interval = fmt.Sprintf("%.1f s", g.Last.Sub(g.First).Seconds()/float64(g.Count-1))
	}
	row.Size = finderSize(g.MinLen, g.MaxLen)
	row.Srcs = finderSrcs(g.Srcs)
	row.Nets = rc.nets(g.VIDs)
	row.Offset = fmt.Sprintf("+%d s", int(g.First.Sub(rc.started).Seconds()))
	row.Hex, row.ASCII = hexASCII(g.Sample)
	return row
}

// rowGames gives the games of the active rule that forwards a group, or, when no rule does, the games of the
// database that use its port.
func (rc finderRowCtx) rowGames(g engineapi.FinderGroup) []gameHint {
	r, ok := rc.idx.byRule[g.Rule]
	if !ok {
		return portGames(rc.games, g.Kind, g.Group, g.Port)
	}
	var out []gameHint
	for _, id := range r.Specs {
		if h, ok := rc.gameByID[id]; ok {
			out = append(out, h)
		}
	}
	return out
}

// nets names the networks a group was seen in, "VLAN <id>" for one without a name.
func (rc finderRowCtx) nets(vids []int) string {
	nets := make([]string, len(vids))
	for i, id := range vids {
		nets[i] = rc.vlanName[id]
		if nets[i] == "" {
			nets[i] = fmt.Sprintf("VLAN %d", id)
		}
	}
	return strings.Join(nets, ", ")
}
