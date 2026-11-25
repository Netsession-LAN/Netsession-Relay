package web

import (
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
)

// The groups of the players page: one game with everybody who searches or hosts it, the roles of the senders
// and the marks for addresses that look like a scanner.

// playerRow is one sender of discovery packets as shown on /spieler.
type playerRow struct {
	VLAN  string
	Key   string
	VID   int
	IP    string
	Role  string // roleHost | roleSearch | roleActive (also the data-role of the styles); roleLabel words it
	First time.Time
	Last  time.Time
	Ago   string // "vor 4 s", in the page language
	Count uint64
	// Spread is the number of games this address appears in; Scanner is set from scannerSpread on, where it
	// looks like a game server scanner (Discord notifier) rather than a player.
	Spread  int
	Scanner bool
	// Server: found by the relay's server probe (answers, does not broadcast); Port and, for Steam/Source
	// servers, name, map and players
	Server  bool
	Port    uint16
	SrvName string
	SrvMap  string
	Slots   string // "3/10"
}

// What a sender does, as far as its packets tell.
const (
	roleHost   = "host"
	roleSearch = "search"
	roleActive = "active" // searches or hosts: not to be told apart from the packet
)

// roleLabel is the word shown for a role (German source text, translated by the template).
func roleLabel(role string) string {
	switch role {
	case roleHost:
		return "hostet"
	case roleSearch:
		return "sucht"
	}
	return "aktiv"
}

// scannerSpread is the number of games from which on an address is offered as "Dienst?" (a scanner).
const scannerSpread = 3

// playerGroup is one game with everybody who searches or hosts it. Games that share a port cannot be told apart
// from the packet: the group is named after the first one and lists the others behind "+N". A game with several
// ports is several rules; they end up in the same group, and a PC seen on more than one of them is listed once.
type playerGroup struct {
	Rule    string
	Title   string    // short name of the first game, or the rule id if no game is known
	GameID  string    // "" if no game is known
	More    []gameRef // the other games on the same port
	Ports   string
	Players []playerRow
	PCs     int    // different addresses (several servers on one host are one PC)
	Tables  int    // number of different networks
	HostAt  string // names of the networks with an open server
}

// addRule notes the ports of a rule the group was seen on and the other games that share them.
func (g *playerGroup) addRule(rule compile.Rule, refs []gameRef) {
	switch {
	case g.Ports == "":
		g.Ports = rule.Ports
	case !strings.Contains(g.Ports, rule.Ports):
		g.Ports += ", " + rule.Ports
	}
	g.addOtherGames(refs)
}

// addOtherGames notes the games besides the group's own one that share its ports.
func (g *playerGroup) addOtherGames(refs []gameRef) {
	for _, r := range refs {
		if r.ID != g.GameID && !slices.Contains(g.More, r) {
			g.More = append(g.More, r)
		}
	}
}

// addPlayer adds a sender to the group. The same PC on another port of the same game is one line with the
// widest time span; servers on different ports of one host (four CS2 servers on 27015-27018) stay separate.
func (g *playerGroup) addPlayer(row playerRow) {
	i := slices.IndexFunc(g.Players, row.samePlayer)
	if i < 0 {
		g.Players = append(g.Players, row)
		return
	}
	g.Players[i].merge(row)
}

// samePlayer reports whether x is the line of p's PC: same address in the same network, and for two servers
// also the same port.
func (p playerRow) samePlayer(x playerRow) bool {
	return x.IP == p.IP && x.VID == p.VID && (!x.Server || !p.Server || x.Port == p.Port)
}

// merge widens the line to the time span and count of row. A host stays a host, "aktiv" gives way to what row
// says, and a server found by the probe brings its details.
func (p *playerRow) merge(row playerRow) {
	if row.First.Before(p.First) {
		p.First = row.First
	}
	if row.Last.After(p.Last) {
		p.Last, p.Ago = row.Last, row.Ago
	}
	p.Count += row.Count
	if row.Role == roleHost || p.Role == roleActive {
		p.Role = row.Role
	}
	if row.Server {
		p.Server, p.Port, p.SrvName, p.SrvMap, p.Slots = true, row.Port, row.SrvName, row.SrvMap, row.Slots
	}
}

// finish sorts the players (hosts first, then by network) and fills in the figures of the group.
func (g *playerGroup) finish() {
	sort.SliceStable(g.Players, func(i, j int) bool { return playerRowBefore(g.Players[i], g.Players[j]) })
	seen := map[int]bool{}
	pcs := map[string]bool{}
	var hosts []string
	for _, p := range g.Players {
		if p.Role == roleHost && !slices.Contains(hosts, p.VLAN) {
			hosts = append(hosts, p.VLAN)
		}
		seen[p.VID] = true
		pcs[fmt.Sprint(p.VID, p.IP)] = true
	}
	g.PCs = len(pcs)
	g.Tables, g.HostAt = len(seen), strings.Join(hosts, ", ")
}

// playerRowBefore orders the players of a group: hosts first, then by network, address and port.
func playerRowBefore(a, b playerRow) bool {
	if (a.Role == roleHost) != (b.Role == roleHost) {
		return a.Role == roleHost
	}
	if a.VID != b.VID {
		return a.VID < b.VID
	}
	if a.IP != b.IP {
		return a.IP < b.IP
	}
	return a.Port < b.Port
}

// markScanners counts the games every address searches in and marks the ones that look like a scanner. Servers
// found by the probe do not count: one host with several servers is not a scanner.
func markScanners(groups []playerGroup) {
	spread := map[string]int{}
	for _, g := range groups {
		for ip := range g.searchingIPs() {
			spread[ip]++
		}
	}
	for gi := range groups {
		groups[gi].markSpread(spread)
	}
}

// markSpread sets the number of games every searching player appears in (spread by address) and marks scanners.
func (g *playerGroup) markSpread(spread map[string]int) {
	for pi := range g.Players {
		if p := &g.Players[pi]; !p.Server {
			p.Spread = spread[p.IP]
			p.Scanner = p.Spread >= scannerSpread
		}
	}
}

// searchingIPs is the set of addresses in the group that are not servers found by the probe.
func (g *playerGroup) searchingIPs() map[string]bool {
	in := map[string]bool{}
	for _, p := range g.Players {
		if !p.Server {
			in[p.IP] = true
		}
	}
	return in
}

// roleOf says what a sender on this rule does: only "announce" games host, only "query" games search,
// "both" (FlatOut) cannot be told apart from the packet alone.
func roleOf(ids []string, dirs map[string]map[string]bool) string {
	host, search, unclear := ruleDirections(ids, dirs)
	switch {
	case unclear || host == search:
		return roleActive
	case host:
		return roleHost
	}
	return roleSearch
}

// ruleDirections collects what the games ids do (dirs: the directions per game): host (announce), search
// (query), or unclear as soon as one game does both or is not known.
func ruleDirections(ids []string, dirs map[string]map[string]bool) (host, search, unclear bool) {
	for _, id := range ids {
		d := dirs[id]
		if d["both"] || d[""] {
			return false, false, true
		}
		host = host || d["announce"]
		search = search || d["query"]
	}
	return host, search, false
}
