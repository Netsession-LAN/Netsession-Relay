package web

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/control"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// Rules and games: which compiled rules a game ends up in and which games share a rule, for the names on the
// live view, the games pages, the port finder and the players page.

// ruleIndex maps compiled rules to games. It is cached per engine snapshot hash.
type ruleIndex struct {
	hash   string
	byRule map[string]compile.Rule
	byGame map[string][]compile.Rule
}

func (s *Server) rules(ctx context.Context, hash string) *ruleIndex {
	s.ruleMu.Lock()
	defer s.ruleMu.Unlock()
	if s.ruleIdx != nil && hash != "" && s.ruleIdx.hash == hash {
		return s.ruleIdx
	}
	idx := &ruleIndex{hash: hash, byRule: map[string]compile.Rule{}, byGame: map[string][]compile.Rule{}}
	// dry run: compiles the current configuration, sends nothing
	if res, err := control.Apply(ctx, s.Store, s.Engine, "", "", true); err == nil && res.Snapshot != nil {
		for _, r := range res.Snapshot.Rules {
			idx.byRule[r.ID] = r
			for _, g := range r.Specs {
				idx.byGame[g] = append(idx.byGame[g], r)
			}
		}
	}
	s.ruleIdx = idx
	return idx
}

func (s *Server) dropRuleIndex() {
	s.ruleMu.Lock()
	s.ruleIdx = nil
	s.ruleMu.Unlock()
}

// rulesFor returns the rules a game ends up in, with counters.
func rulesFor(idx *ruleIndex, id string, v *liveView) []ruleView {
	var out []ruleView
	for _, r := range idx.byGame[id] {
		rv := ruleView{Rule: r}
		if i := slices.IndexFunc(v.Rules, func(s engineapi.RuleStat) bool { return s.ID == r.ID }); i >= 0 {
			rv.RX, rv.Relayed = v.Rules[i].RX, v.Rules[i].Relayed
		}
		out = append(out, rv)
	}
	return out
}

// gameRef names a game for a link.
type gameRef struct {
	ID   string
	Name string
}

// gameRefs lists the games that share a rule (same port): switched-on games first, then by name.
func gameRefs(ids []string, games map[string]store.GameRow) []gameRef {
	out := make([]gameRef, 0, len(ids))
	on := map[string]bool{}
	for _, id := range ids {
		g, ok := games[id]
		if !ok {
			out = append(out, gameRef{ID: id, Name: id})
			continue
		}
		on[id] = g.Enabled
		out = append(out, gameRef{ID: id, Name: g.Name})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if on[out[i].ID] != on[out[j].ID] {
			return on[out[i].ID]
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// shortName cuts a database name down to the first title: "ARMA 2 / Operation Arrowhead / DayZ Mod" becomes
// "ARMA 2", "TrackMania 2 (Canyon/Stadium)" becomes "TrackMania 2". The full name stays one tap away.
func shortName(name string) string {
	for _, sep := range []string{" / ", " ("} {
		if i := strings.Index(name, sep); i > 2 {
			name = name[:i]
		}
	}
	return name
}
