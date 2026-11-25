package web

import (
	"fmt"
	"net/http"
	"slices"
	"strconv"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// defaultGroups are offered when no VLAN exists yet (same short names as the presets).
var defaultGroups = []string{compile.GroupTables, compile.GroupGameserver, compile.GroupWiFi, compile.GroupOrga}

func (s *Server) vlanGroups(c store.Config) []string {
	var gs []string
	for _, v := range c.VLANs {
		if !slices.Contains(gs, v.Group) {
			gs = append(gs, v.Group)
		}
	}
	if len(gs) == 0 {
		gs = slices.Clone(defaultGroups)
	}
	return gs
}

func (s *Server) vlanNewForm(w http.ResponseWriter, r *http.Request) {
	c, err := s.Store.ExportConfig(r.Context())
	if err != nil {
		s.failInternal(w, r, "network form: configuration", err)
		return
	}
	f := vlanForm{New: true, Attach: compile.AttachTrunk, Enable: true, Groups: s.vlanGroups(c), Parent: knownTrunk(c.VLANs)}
	if slices.Contains(f.Groups, compile.GroupTables) {
		f.Group = compile.GroupTables
	}
	s.render(w, r, "network-edit", "Netz anlegen", "network", f)
}

// knownTrunk is the trunk interface of the first network on a trunk ("" without one). The trunk is the same for
// all networks in practice, so a new network starts with it.
func knownTrunk(vlans []store.VLANConfig) string {
	for _, v := range vlans {
		if v.Attach == compile.AttachTrunk && v.Parent != "" {
			return v.Parent
		}
	}
	return ""
}

func (s *Server) vlanEditForm(w http.ResponseWriter, r *http.Request) {
	vid, _ := strconv.Atoi(r.PathValue("vid"))
	v, err := s.Store.VLAN(r.Context(), vid)
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "Dieses Netz gibt es nicht.")
		return
	}
	c, err := s.Store.ExportConfig(r.Context())
	s.degraded("network form: groups", err)
	f := vlanForm{VID: strconv.Itoa(v.VID), Name: v.Name, Group: v.Group, CIDR: v.CIDR, RelayIP: v.RelayIP,
		Attach: v.Attach, Parent: v.Parent, Device: v.Device, Enable: v.Enabled, Groups: s.vlanGroups(c)}
	if v.RatePPS > 0 {
		f.Rate = strconv.Itoa(v.RatePPS)
	}
	if v.Burst > 0 {
		f.Burst = strconv.Itoa(v.Burst)
	}
	s.render(w, r, "network-edit", s.T(r)("%s bearbeiten", v.Name), "network", f)
}

func (s *Server) vlanCreate(w http.ResponseWriter, r *http.Request) { s.vlanSave(w, r, true) }
func (s *Server) vlanUpdate(w http.ResponseWriter, r *http.Request) { s.vlanSave(w, r, false) }

// vlanEditTitle is the title of the network form.
func vlanEditTitle(name string, isNew bool, T translator) string {
	if isNew {
		return "Netz anlegen"
	}
	return T("%s bearbeiten", name)
}

func (s *Server) vlanDelete(w http.ResponseWriter, r *http.Request) {
	T := s.T(r)
	vid, err := strconv.Atoi(r.PathValue("vid"))
	if err != nil {
		s.answer(w, r, "/netz", applyAnswer{Error: T("VLAN-ID ungültig.")})
		return
	}
	v, err := s.Store.VLAN(r.Context(), vid)
	if err != nil {
		s.answer(w, r, "/netz", applyAnswer{Error: T("Dieses Netz gibt es nicht.")})
		return
	}
	if err := s.Store.DeleteVLAN(r.Context(), s.actor(r), vid); err != nil {
		s.answer(w, r, "/netz", applyAnswer{Error: string(s.userError(r, "network: delete", err))})
		return
	}
	a := s.applyNow(r, fmt.Sprintf("VLAN %d gelöscht", vid))
	if a.OK {
		a.Message = T("%s (VLAN %d) gelöscht.", v.Name, vid)
	}
	s.answer(w, r, "/netz", a)
}
