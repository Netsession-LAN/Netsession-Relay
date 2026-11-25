package compile

import (
	"net/netip"
	"slices"

	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/portspec"
)

// What the discovery specs of the games claim per port, collected before the rules are built from it.

type portKey struct {
	kind  string
	group string
	port  uint16
}

type portAcc struct {
	games      []string
	sourceMode map[string][]string // mode -> games
	dstMode    map[string][]string
	ttlMode    map[string][]string
	flows      map[Edge]bool
	rate       int
	burst      int
	payloadMax int
	notQuery   bool                      // some game on this port announces (or both), so no probing
	sources    map[string][]netip.Prefix // game -> source filter (only games that have one)
}

// claim records what one discovery spec of a game wants on each of its ports.
func (c *compiler) claim(gameID string, s gamedb.Spec, edges []Edge, sources []netip.Prefix) {
	ps, err := portspec.Parse(s.Ports)
	if err != nil { // impossible after gamedb validation; keep the guard
		c.fail("game %q: %w", gameID, err)
		return
	}
	for _, p := range ps.Ports() {
		k := portKey{s.Kind, s.Group, p}
		a := c.acc[k]
		if a == nil {
			a = newPortAcc()
			c.acc[k] = a
		}
		a.add(gameID, s, edges, sources)
	}
}

func newPortAcc() *portAcc {
	return &portAcc{sourceMode: map[string][]string{}, dstMode: map[string][]string{},
		ttlMode: map[string][]string{}, flows: map[Edge]bool{}, sources: map[string][]netip.Prefix{}}
}

// add records the claim of one game's spec on this port.
func (a *portAcc) add(gameID string, s gamedb.Spec, edges []Edge, sources []netip.Prefix) {
	a.games = appendUnique(a.games, gameID)
	a.sourceMode[s.SourceMode] = appendUnique(a.sourceMode[s.SourceMode], gameID)
	a.dstMode[s.DstMode] = appendUnique(a.dstMode[s.DstMode], gameID)
	a.ttlMode[s.TTLMode] = appendUnique(a.ttlMode[s.TTLMode], gameID)
	for _, e := range edges {
		a.flows[e] = true
	}
	a.rate = max(a.rate, s.RatePPS)
	a.burst = max(a.burst, s.RateBurst)
	a.payloadMax = max(a.payloadMax, s.PayloadMax)
	if s.Direction != "query" {
		a.notQuery = true
	}
	if len(sources) > 0 {
		a.sources[gameID] = sources
	}
}

func appendUnique(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}
	return append(s, v)
}
