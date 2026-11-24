package web

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// serviceView is a known service host (e.g. a game server notifier) with the games it queried.
type serviceView struct {
	IP, Name, Note string
	VLAN, Key      string
	Games          []string // short names, in the order seen
	Last           time.Time
	Ago            string
	Count          uint64
}

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
	var err error
	n.games, err = s.Store.Games(ctx)
	s.degraded("players: games", err)
	for _, g := range n.games {
		n.byID[g.ID] = g
		n.dirs[g.ID] = map[string]bool{}
		for _, d := range g.Discovery {
			n.dirs[g.ID][d.Direction] = true
		}
	}
	if c, err := s.Store.ExportConfig(ctx); err == nil {
		for _, v := range c.VLANs {
			n.vlanName[v.VID], n.vlanGroup[v.VID] = v.Name, v.Group
		}
	}
	return n
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

// playerCollector sorts the senders the engine reports into game groups and known service hosts, both in the
// order first seen.
type playerCollector struct {
	names    playerNames
	svcByIP  map[string]*serviceView
	svcOrder []string
	byKey    map[string]*playerGroup
	order    []string
}

// playerGame is the game a sender is counted for: the group key and title, and the rule it was seen on.
type playerGame struct {
	key, title, gameID string
	refs               []gameRef
	rule               compile.Rule
	known              bool // the rule is in the active snapshot
}

// gameOf works out which game a sender belongs to.
func (pc *playerCollector) gameOf(p engineapi.Player) playerGame {
	pg := playerGame{key: p.Rule, title: p.Rule}
	pg.rule, pg.known = pc.names.idx.byRule[p.Rule]
	if pg.known {
		if pg.refs = gameRefs(pg.rule.Specs, pc.names.byID); len(pg.refs) > 0 {
			pg.key, pg.title, pg.gameID = "game:"+pg.refs[0].ID, shortName(pg.refs[0].Name), pg.refs[0].ID
		}
	}
	// a Steam/Source server says which game it runs; many games share its port, so that beats the rule
	if p.Server && p.Game != "" {
		pg.key, pg.title, pg.gameID, pg.refs = "a2s:"+strings.ToLower(p.Game), p.Game, gameByName(pc.names.games, p.Game), nil
	}
	return pg
}

// add files one sender, with its age already in words, under its service host or its game group.
func (pc *playerCollector) add(p engineapi.Player, age string) {
	pg := pc.gameOf(p)
	if h, ok := pc.names.hosts[p.Src.String()]; ok && !p.Server {
		pc.service(h, p.VID).add(p, pg.title, age)
		return
	}
	g := pc.group(pg)
	row := pc.row(p, age)
	if pg.known {
		g.addRule(pg.rule, pg.refs)
		row.Role = roleOf(pg.rule.Specs, pc.names.dirs)
	}
	if p.Server {
		row.Role, row.Server, row.Port, row.SrvName, row.SrvMap = roleHost, true, p.Port, p.Name, p.Map
		if p.Max > 0 {
			row.Slots = fmt.Sprintf("%d/%d", p.Num, p.Max)
		}
	}
	g.addPlayer(row)
}

// service gives the entry of a known service host, created on its first sighting (in the network vid).
func (pc *playerCollector) service(h store.ServiceHost, vid int) *serviceView {
	if sv := pc.svcByIP[h.IP]; sv != nil {
		return sv
	}
	sv := &serviceView{IP: h.IP, Name: h.Name, Note: h.Note, VLAN: pc.names.vlanName[vid],
		Key: tableKey(pc.names.vlanName[vid], pc.names.vlanGroup[vid])}
	pc.svcByIP[h.IP] = sv
	pc.svcOrder = append(pc.svcOrder, h.IP)
	return sv
}

// group gives the group of a game, created on its first sender.
func (pc *playerCollector) group(pg playerGame) *playerGroup {
	if g := pc.byKey[pg.key]; g != nil {
		return g
	}
	g := &playerGroup{Rule: pg.key, Title: pg.title, GameID: pg.gameID}
	pc.byKey[pg.key] = g
	pc.order = append(pc.order, pg.key)
	return g
}

// row is the line of a sender before its role is known; a network without a name shows as "VLAN <id>".
func (pc *playerCollector) row(p engineapi.Player, age string) playerRow {
	vlan := pc.names.vlanName[p.VID]
	row := playerRow{VID: p.VID, IP: p.Src.String(), First: p.First, Last: p.Last, Count: p.Count,
		VLAN: vlan, Key: tableKey(vlan, pc.names.vlanGroup[p.VID]), Role: roleActive, Ago: age}
	if row.VLAN == "" {
		row.VLAN = fmt.Sprintf("VLAN %d", p.VID)
		row.Key = "?"
	}
	return row
}

// groups gives the finished groups: the ones with an open server first, then the ones with the most players,
// then by title; scanners are marked.
func (pc *playerCollector) groups() []playerGroup {
	out := make([]playerGroup, 0, len(pc.order))
	for _, k := range pc.order {
		g := pc.byKey[k]
		g.finish()
		out = append(out, *g)
	}
	sort.SliceStable(out, func(i, j int) bool { return playerGroupBefore(out[i], out[j]) })
	markScanners(out)
	return out
}

// playerGroupBefore orders the groups of /spieler: an open server first, then more players, then by title.
func playerGroupBefore(a, b playerGroup) bool {
	if (a.HostAt != "") != (b.HostAt != "") {
		return a.HostAt != ""
	}
	if len(a.Players) != len(b.Players) {
		return len(a.Players) > len(b.Players)
	}
	return strings.ToLower(a.Title) < strings.ToLower(b.Title)
}

// services gives the known service hosts seen, each with its games sorted by name.
func (pc *playerCollector) services() []serviceView {
	out := make([]serviceView, 0, len(pc.svcOrder))
	for _, ip := range pc.svcOrder {
		sv := pc.svcByIP[ip]
		slices.SortFunc(sv.Games, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
		out = append(out, *sv)
	}
	return out
}

// add counts one sighting of a known service host for the game with this title.
func (sv *serviceView) add(p engineapi.Player, title, age string) {
	if !slices.Contains(sv.Games, title) {
		sv.Games = append(sv.Games, title)
	}
	if p.Last.After(sv.Last) {
		sv.Last, sv.Ago = p.Last, age
	}
	sv.Count += p.Count
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
