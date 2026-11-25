package web

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"github.com/Netsession-LAN/Netsession-Relay/internal/uimsg"
)

// ---- networks and flow profiles ----

type vlanRow struct {
	store.VLANConfig
	Key   string
	Iface string
	Live  vlanView
}

type matrixRow struct {
	Src   string
	Cells []bool
}

type profileView struct {
	Name    string
	Default bool
	Groups  []string
	Rows    []matrixRow
	Games   []string // names of the switched-on games using this profile
	Reading string   // one row put into words, as a reading aid
}

// vlanGroupView is the networks of one group, in the order of the configuration.
type vlanGroupView struct {
	Name string
	Rows []vlanRow
}

// matrix turns the edges of a flow profile into a table source group x destination group.
func matrix(p store.ProfileConfig, groups []string, T translator) profileView {
	groups = matrixGroups(p.Edges, groups)
	v := profileView{Name: p.Name, Default: p.Default, Groups: groups}
	for _, src := range groups {
		row := matrixRow{Src: src}
		for _, dst := range groups {
			row.Cells = append(row.Cells, slices.Contains(p.Edges, compile.Edge{Src: src, Dst: dst}))
		}
		v.Rows = append(v.Rows, row)
		if v.Reading == "" {
			v.Reading = matrixReading(row, groups, T)
		}
	}
	return v
}

// matrixGroups adds the groups the edges use that no network has to the groups of the networks.
func matrixGroups(edges []compile.Edge, groups []string) []string {
	for _, e := range edges {
		for _, g := range []string{e.Src, e.Dst} {
			if !slices.Contains(groups, g) {
				groups = append(groups, g)
			}
		}
	}
	return groups
}

// matrixReading puts one row of the matrix into words; "" for a row without any destination.
func matrixReading(row matrixRow, groups []string, T translator) string {
	var to []string
	for i, on := range row.Cells {
		if on {
			to = append(to, T(groupName(groups[i])))
		}
	}
	if len(to) == 0 {
		return ""
	}
	return T("Zeile „%s“: Rufe von dort gehen nach %s.", T(groupName(row.Src)), strings.Join(to, ", "))
}

func (s *Server) network(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.ExportConfig(r.Context())
	if err != nil {
		s.failInternal(w, r, "networks: configuration", err)
		return
	}
	live := s.liveNow(r.Context())
	n := networkList{groupN: map[string]int{}}
	for _, v := range c.VLANs {
		n.add(networkVLANRow(v, live.VLANs))
	}
	names := s.networkGameNames(r.Context())
	var profiles []profileView
	for _, p := range c.Profiles {
		v := matrix(p, slices.Clone(n.groups), s.T(r))
		v.Games = networkProfileGames(p, c.Games, names)
		profiles = append(profiles, v)
	}
	s.renderLive(w, r, "network", "Netz", "network", map[string]any{"VLANs": n.rows, "Groups": n.byGroup, "GroupN": n.groupN,
		"Profiles": profiles, "On": n.on, "Total": len(n.rows)}, live)
}

// networkGameNames gives the names of the games by ID (empty when the games cannot be read: the page then shows
// the IDs).
func (s *Server) networkGameNames(ctx context.Context) map[string]string {
	names := map[string]string{}
	if games, err := s.Store.Games(ctx); err == nil {
		for _, g := range games {
			names[g.ID] = g.Name
		}
	}
	return names
}

// networkList is the networks page's list of networks: all rows, the rows by group (groups in the order of
// the configuration) and the counts.
type networkList struct {
	rows    []vlanRow
	groups  []string
	byGroup []vlanGroupView
	groupN  map[string]int
	on      int
}

func (n *networkList) add(row vlanRow) {
	if row.Enabled {
		n.on++
	}
	gi := slices.Index(n.groups, row.Group)
	if gi < 0 {
		gi = len(n.groups)
		n.groups = append(n.groups, row.Group)
		n.byGroup = append(n.byGroup, vlanGroupView{Name: row.Group})
	}
	n.byGroup[gi].Rows = append(n.byGroup[gi].Rows, row)
	n.groupN[row.Group]++
	n.rows = append(n.rows, row)
}

// networkVLANRow is a configured network with its interface name and the live tile of the engine.
func networkVLANRow(v store.VLANConfig, tiles []vlanView) vlanRow {
	row := vlanRow{VLANConfig: v, Key: tableKey(v.Name, v.Group), Iface: v.Device}
	if v.Attach == compile.AttachTrunk {
		row.Iface = fmt.Sprintf("%s.%d", v.Parent, v.VID)
	}
	if i := slices.IndexFunc(tiles, func(x vlanView) bool { return x.VID == v.VID }); i >= 0 {
		row.Live = tiles[i]
	}
	return row
}

// networkProfileGames names the games of the event that use a profile (the default profile also has the games without
// one), sorted; a game the database does not know shows its id.
func networkProfileGames(p store.ProfileConfig, games []store.EventGameConfig, names map[string]string) []string {
	var out []string
	for _, g := range games {
		if g.FlowProfile == p.Name || (g.FlowProfile == "" && p.Default) {
			out = append(out, cmp.Or(names[g.ID], g.ID))
		}
	}
	slices.Sort(out)
	return out
}

func (s *Server) toggleVLAN(w http.ResponseWriter, r *http.Request) {
	T := s.T(r)
	vid, err := strconv.Atoi(r.PathValue("vid"))
	if err != nil {
		s.answer(w, r, "/netz", applyAnswer{Error: T("VLAN-ID ungültig.")})
		return
	}
	on := r.PostFormValue("on") == "1"
	if err := s.Store.SetVLANEnabled(r.Context(), s.actor(r), vid, on); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			err = uimsg.Error("Dieses Netz gibt es nicht.")
		}
		s.answer(w, r, "/netz", applyAnswer{Error: string(s.userError(r, "network: switch", err))})
		return
	}
	a := s.applyNow(r, fmt.Sprintf("VLAN %d %s", vid, onOff(on)))
	a.Enabled = on
	if a.OK {
		a.Message = T("VLAN %d ist %s.", vid, T(onOff(on)))
	}
	s.answer(w, r, "/netz", a)
}
