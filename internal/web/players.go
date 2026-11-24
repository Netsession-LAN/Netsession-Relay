package web

import (
	"context"
	"fmt"
	"github.com/Netsession2024/Netsession-Relay/internal/compile"
	"github.com/Netsession2024/Netsession-Relay/internal/engineapi"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/Netsession2024/Netsession-Relay/internal/store"
)

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
	names := s.playerNames(ctx)
	now := time.Now()
	svcByIP := map[string]*serviceView{}
	var svcOrder []string
	byKey := map[string]*playerGroup{}
	var order []string
	for _, p := range list {
		key, title, gameID := p.Rule, p.Rule, ""
		var refs []gameRef
		rule, known := names.idx.byRule[p.Rule]
		if known {
			if refs = gameRefs(rule.Specs, names.byID); len(refs) > 0 {
				key, title, gameID = "game:"+refs[0].ID, shortName(refs[0].Name), refs[0].ID
			}
		}
		// a Steam/Source server says which game it runs; many games share its port, so that beats the rule
		if p.Server && p.Game != "" {
			key, title, gameID, refs = "a2s:"+strings.ToLower(p.Game), p.Game, gameByName(names.games, p.Game), nil
		}
		age := ago(T, now.Sub(p.Last))
		if h, ok := names.hosts[p.Src.String()]; ok && !p.Server {
			sv := svcByIP[h.IP]
			if sv == nil {
				sv = &serviceView{IP: h.IP, Name: h.Name, Note: h.Note, VLAN: names.vlanName[p.VID],
					Key: tableKey(names.vlanName[p.VID], names.vlanGroup[p.VID])}
				svcByIP[h.IP] = sv
				svcOrder = append(svcOrder, h.IP)
			}
			sv.add(p, title, age)
			continue
		}
		g := byKey[key]
		if g == nil {
			g = &playerGroup{Rule: key, Title: title, GameID: gameID}
			byKey[key] = g
			order = append(order, key)
		}
		row := playerRow{VID: p.VID, IP: p.Src.String(), First: p.First, Last: p.Last, Count: p.Count,
			VLAN: names.vlanName[p.VID], Key: tableKey(names.vlanName[p.VID], names.vlanGroup[p.VID]), Role: roleActive, Ago: age}
		if row.VLAN == "" {
			row.VLAN = fmt.Sprintf("VLAN %d", p.VID)
			row.Key = "?"
		}
		if known {
			g.addRule(rule, refs)
			row.Role = roleOf(rule.Specs, names.dirs)
		}
		if p.Server {
			row.Role, row.Server, row.Port, row.SrvName, row.SrvMap = roleHost, true, p.Port, p.Name, p.Map
			if p.Max > 0 {
				row.Slots = fmt.Sprintf("%d/%d", p.Num, p.Max)
			}
		}
		g.addPlayer(row)
	}
	out := make([]playerGroup, 0, len(order))
	for _, k := range order {
		g := byKey[k]
		g.finish()
		out = append(out, *g)
	}
	// groups with an open server first, then the ones with the most players
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].HostAt != "") != (out[j].HostAt != "") {
			return out[i].HostAt != ""
		}
		if len(out[i].Players) != len(out[j].Players) {
			return len(out[i].Players) > len(out[j].Players)
		}
		return strings.ToLower(out[i].Title) < strings.ToLower(out[j].Title)
	})
	markScanners(out)
	services := make([]serviceView, 0, len(svcOrder))
	for _, ip := range svcOrder {
		sv := svcByIP[ip]
		slices.SortFunc(sv.Games, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
		services = append(services, *sv)
	}
	return out, services, nil
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

// addRule notes the ports of a rule the group was seen on and the other games that share them.
func (g *playerGroup) addRule(rule compile.Rule, refs []gameRef) {
	if g.Ports != "" && !strings.Contains(g.Ports, rule.Ports) {
		g.Ports += ", " + rule.Ports
	} else if g.Ports == "" {
		g.Ports = rule.Ports
	}
	for _, r := range refs {
		if r.ID != g.GameID && !slices.Contains(g.More, r) {
			g.More = append(g.More, r)
		}
	}
}

// addPlayer adds a sender to the group. The same PC on another port of the same game is one line with the
// widest time span; servers on different ports of one host (four CS2 servers on 27015-27018) stay separate.
func (g *playerGroup) addPlayer(row playerRow) {
	i := slices.IndexFunc(g.Players, func(x playerRow) bool {
		return x.IP == row.IP && x.VID == row.VID && (!x.Server || !row.Server || x.Port == row.Port)
	})
	if i < 0 {
		g.Players = append(g.Players, row)
		return
	}
	old := &g.Players[i]
	if row.First.Before(old.First) {
		old.First = row.First
	}
	if row.Last.After(old.Last) {
		old.Last, old.Ago = row.Last, row.Ago
	}
	old.Count += row.Count
	if row.Role == roleHost || old.Role == roleActive {
		old.Role = row.Role
	}
	if row.Server {
		old.Server, old.Port, old.SrvName, old.SrvMap, old.Slots = true, row.Port, row.SrvName, row.SrvMap, row.Slots
	}
}

// finish sorts the players (hosts first, then by network) and fills in the figures of the group.
func (g *playerGroup) finish() {
	sort.SliceStable(g.Players, func(i, j int) bool {
		a, b := g.Players[i], g.Players[j]
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
	})
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

// markScanners counts the games every address searches in and marks the ones that look like a scanner. Servers
// found by the probe do not count: one host with several servers is not a scanner.
func markScanners(groups []playerGroup) {
	spread := map[string]int{}
	for _, g := range groups {
		in := map[string]bool{}
		for _, p := range g.Players {
			if !p.Server && !in[p.IP] {
				in[p.IP] = true
				spread[p.IP]++
			}
		}
	}
	for gi := range groups {
		for pi := range groups[gi].Players {
			if p := &groups[gi].Players[pi]; !p.Server {
				p.Spread = spread[p.IP]
				p.Scanner = p.Spread >= scannerSpread
			}
		}
	}
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

// roleOf says what a sender on this rule does: only "announce" games host, only "query" games search,
// "both" (FlatOut) cannot be told apart from the packet alone.
func roleOf(ids []string, dirs map[string]map[string]bool) string {
	host, search := false, false
	for _, id := range ids {
		d := dirs[id]
		host = host || d["announce"]
		search = search || d["query"]
		if d["both"] || d[""] {
			return roleActive
		}
	}
	switch {
	case host && !search:
		return roleHost
	case search && !host:
		return roleSearch
	}
	return roleActive
}

// ackAlarm acknowledges all alarms of a kind ("Gesehen").
func (s *Server) ackAlarm(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	T := s.T(r)
	if _, known := alarmTitles[kind]; !known {
		s.answer(w, r, "/", applyAnswer{Error: T("Diesen Alarm gibt es nicht.")})
		return
	}
	if err := s.Engine.Ack(r.Context(), kind, s.actor(r)); err != nil {
		s.Log.Error("alarm not acknowledged: engine not reachable", "kind", kind, "err", err)
		s.answer(w, r, "/", applyAnswer{Error: T("Die Engine antwortet nicht. Der Alarm ist nicht als gesehen markiert.")})
		return
	}
	s.audit(r.Context(), s.actor(r), "alarm.ack", kind, "", "")
	s.answer(w, r, "/", applyAnswer{OK: true, Message: T("%s: als gesehen markiert.", T(alarmTitles[kind]))})
}

// gameByName finds the id of a game by its name or an alias (case-insensitive); "" if unknown.
func gameByName(games []store.GameRow, name string) string {
	for _, g := range games {
		if strings.EqualFold(g.Name, name) {
			return g.ID
		}
		for _, a := range g.Aliases {
			if strings.EqualFold(a, name) {
				return g.ID
			}
		}
	}
	return ""
}
