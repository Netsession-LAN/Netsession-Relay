package web

import (
	"fmt"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
)

// One sender on the players page: its line, its role and how two lines of the same PC are merged.

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

// setServer marks the line as a server found by the probe: it hosts, with the port, name, map and slots it told.
func (p *playerRow) setServer(srv engineapi.Player) {
	p.Role, p.Server, p.Port, p.SrvName, p.SrvMap = roleHost, true, srv.Port, srv.Name, srv.Map
	if srv.Max > 0 {
		p.Slots = fmt.Sprintf("%d/%d", srv.Num, srv.Max)
	}
}
