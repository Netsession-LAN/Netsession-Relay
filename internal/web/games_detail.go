package web

import (
	"net/http"
	"slices"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// The detail page of a game: its rules, the event config and the diagnosis.

type gameDetail struct {
	Game          gameRow
	Rules         []ruleView
	Steps         []diagStep
	Verdict       diagStep
	CanSwitch     bool
	CanAdmin      bool
	Profile       string       // flow profile of the game in the active event ("" = default)
	Profiles      []string     // all profile names
	Default       string       // name of the default profile
	Sources       []sourceView // source filter of the game in the active event (empty = every sender)
	Verifications []store.Verification
}

type ruleView struct {
	compile.Rule
	RX, Relayed uint64
}

func (s *Server) game(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	games, err := s.Store.Games(ctx)
	if err != nil {
		s.failInternal(w, r, "game: list", err)
		return
	}
	i := slices.IndexFunc(games, func(g store.GameRow) bool { return g.ID == id })
	if i < 0 {
		s.fail(w, r, http.StatusNotFound, "Dieses Spiel gibt es nicht.")
		return
	}
	live := s.liveNow(ctx)
	idx := s.rules(ctx, live.Hash)
	d := &gameDetail{Game: gameRowOf(games[i], idx, live), Rules: rulesFor(idx, id, live),
		CanSwitch: store.RoleRank(sessionFrom(r).User.Role) >= store.RoleRank(store.RoleOperator)}
	d.Steps, d.Verdict = diagnoseT(s.T(r), games[i], d.Rules, live)
	d.CanAdmin = store.RoleRank(sessionFrom(r).User.Role) >= store.RoleRank(store.RoleAdmin)
	d.Verifications, err = s.Store.Verifications(ctx, id)
	s.degraded("game: test results", err)
	if c, err := s.Store.ExportConfig(ctx); err == nil {
		d.addEventConfig(c, id)
	}
	if r.URL.Query().Get("teil") == "diagnose" { // fragment for the live refresh
		s.renderGameDiagnosis(w, r, d)
		return
	}
	s.renderLive(w, r, "game", d.Game.Name, "games", d, live)
}

// renderGameDiagnosis writes only the diagnosis part of the game page.
func (s *Server) renderGameDiagnosis(w http.ResponseWriter, r *http.Request, d *gameDetail) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := s.page(r, "game").ExecuteTemplate(w, "diagnose", d); err != nil {
		s.Log.Error("render", "page", "game/diagnose", "err", err)
	}
}

// addEventConfig fills in what the active event sets for the game: its flow profile (with the names to choose
// from) and its source filter.
func (d *gameDetail) addEventConfig(c store.Config, id string) {
	for _, p := range c.Profiles {
		d.Profiles = append(d.Profiles, p.Name)
		if p.Default {
			d.Default = p.Name
		}
	}
	for _, g := range c.Games {
		if g.ID == id {
			d.Profile = g.FlowProfile
			d.Sources = sourceViews(g.Sources, c.VLANs)
		}
	}
}
