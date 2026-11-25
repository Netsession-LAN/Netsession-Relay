package web

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
)

// The games with traffic in the live view, busiest first.

type busyRow struct {
	Rule    string `json:"rule"`
	Names   string `json:"names"`          // short: first game, "+N" for the others on the same port
	Game    string `json:"game,omitempty"` // id of the first game, for the link
	All     string `json:"all,omitempty"`  // every game on this rule
	Ports   string `json:"ports"`
	Rate    uint64 `json:"per_s"`
	RX      uint64 `json:"rx"`
	Relayed uint64 `json:"relayed"`
}

// busyRows is the number of games with traffic the overview lists.
const busyRows = 6

// addBusy lists the games with traffic: rules that have received something, busiest first (addGames runs first).
func (v *liveView) addBusy(stats []engineapi.RuleStat, idx *ruleIndex, rates map[string]uint64) {
	for _, rs := range stats {
		v.ruleRX[rs.ID] = rs.RX
		if rs.RX == 0 {
			continue
		}
		row := busyRow{Rule: rs.ID, Names: rs.ID, RX: rs.RX, Relayed: rs.Relayed, Rate: rates[rs.ID]}
		if r, ok := idx.byRule[rs.ID]; ok {
			row.Ports = r.Ports
			row.nameGames(gameRefs(r.Specs, v.games))
		}
		v.Busy = append(v.Busy, row)
	}
	sort.SliceStable(v.Busy, func(i, j int) bool { return busyBefore(v.Busy[i], v.Busy[j]) })
	if len(v.Busy) > busyRows {
		v.Busy = v.Busy[:busyRows]
	}
}

// nameGames names the row after the games of its rule: the first one with "+N" for the others, and all of them
// for the title. Without games the row keeps the rule id.
func (row *busyRow) nameGames(refs []gameRef) {
	if len(refs) == 0 {
		return
	}
	row.Game, row.Names = refs[0].ID, shortName(refs[0].Name)
	if n := len(refs) - 1; n > 0 {
		row.Names += fmt.Sprintf(" +%d", n)
	}
	all := make([]string, len(refs))
	for i, g := range refs {
		all[i] = g.Name
	}
	row.All = strings.Join(all, " · ")
}

// busyBefore orders the busy games: highest rate first, then most received.
func busyBefore(a, b busyRow) bool {
	if a.Rate != b.Rate {
		return a.Rate > b.Rate
	}
	return a.RX > b.RX
}
