package web

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// players shows who searches and hosts which game ("Wer spielt was"), refreshed live via a fragment.
func (s *Server) players(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	groups, services, err := s.playerGroupsT(ctx, s.T(r))
	n := 0
	for _, g := range groups {
		n += g.PCs
	}
	admin := store.RoleRank(sessionFrom(r).User.Role) >= store.RoleRank(store.RoleAdmin)
	d := map[string]any{"Groups": groups, "Services": services, "Err": err != nil, "Players": n, "Admin": admin}
	if r.URL.Query().Get("teil") == "tabelle" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := s.page(r, "players").ExecuteTemplate(w, "players-table", d); err != nil {
			s.Log.Error("render players fragment", "err", err)
		}
		return
	}
	s.render(w, r, "players", "Spieler", "players", d)
}

// playerNames is what the players page looks senders up in: rules, games, networks and known service hosts.
type playerNames struct {
	idx       *ruleIndex
	games     []store.GameRow
	byID      map[string]store.GameRow
	dirs      map[string]map[string]bool // game id -> directions of its discovery specs
	vlanName  map[int]string
	vlanGroup map[int]string
	hosts     map[string]store.ServiceHost // by address
}

// playerNames reads the lookups. A part that cannot be read is left empty: the page then shows rule ids and
// VLAN numbers instead of names.
func (s *Server) playerNames(ctx context.Context) playerNames {
	n := playerNames{byID: map[string]store.GameRow{}, dirs: map[string]map[string]bool{}, vlanName: map[int]string{},
		vlanGroup: map[int]string{}, hosts: map[string]store.ServiceHost{}}
	if hs, err := s.Store.Services(ctx); err == nil {
		for _, h := range hs {
			n.hosts[h.IP] = h
		}
	}
	st, _ := s.Engine.Status(ctx) // the engine answered Players just now; without a status the rules have no names
	n.idx = s.rules(ctx, st.SnapshotHash)
	games, err := s.Store.Games(ctx)
	s.degraded("players: games", err)
	n.addGames(games)
	if c, err := s.Store.ExportConfig(ctx); err == nil {
		for _, v := range c.VLANs {
			n.vlanName[v.VID], n.vlanGroup[v.VID] = v.Name, v.Group
		}
	}
	return n
}

// addGames notes the games by ID with the directions of their discovery specs.
func (n *playerNames) addGames(games []store.GameRow) {
	n.games = games
	for _, g := range games {
		n.byID[g.ID] = g
		n.dirs[g.ID] = map[string]bool{}
		for _, d := range g.Discovery {
			n.dirs[g.ID][d.Direction] = true
		}
	}
}

// playerGroupsT builds the groups of /spieler; T words the ages ("vor 4 s") in the page language.
func (s *Server) playerGroupsT(ctx context.Context, T translator) ([]playerGroup, []serviceView, error) {
	list, err := s.Engine.Players(ctx)
	if err != nil {
		return nil, nil, err
	}
	pc := playerCollector{names: s.playerNames(ctx), svcByIP: map[string]*serviceView{}, byKey: map[string]*playerGroup{}}
	now := time.Now()
	for _, p := range list {
		pc.add(p, ago(T, now.Sub(p.Last)))
	}
	return pc.groups(), pc.services(), nil
}

// ago puts a short age into words ("gerade", "vor 40 s", "vor 12 min") in the language of T.
func ago(T translator, d time.Duration) string {
	switch {
	case d < 3*time.Second:
		return T("gerade")
	case d < 90*time.Second:
		return T("vor %d s", int(d.Seconds()))
	}
	return T("vor %d min", int(d.Minutes()))
}

// gameByName finds the id of a game by its name or an alias (case-insensitive); "" if unknown.
func gameByName(games []store.GameRow, name string) string {
	same := func(s string) bool { return strings.EqualFold(s, name) }
	for _, g := range games {
		if same(g.Name) || slices.ContainsFunc(g.Aliases, same) {
			return g.ID
		}
	}
	return ""
}
