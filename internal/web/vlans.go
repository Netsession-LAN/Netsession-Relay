package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
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

func (s *Server) vlanSave(w http.ResponseWriter, r *http.Request, isNew bool) {
	ctx := r.Context()
	T := s.T(r)
	f := readVLANForm(r, isNew)
	c, err := s.Store.ExportConfig(ctx)
	if err != nil {
		s.failInternal(w, r, "network: configuration", err)
		return
	}
	f.Groups = s.vlanGroups(c)
	title := "Netz anlegen"
	if !isNew {
		title = T("%s bearbeiten", f.Name)
	}
	reject := func(msg string) {
		f.Err = msg
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, r, "network-edit", title, "network", f)
	}

	v, msg := f.parse(T)
	if msg != "" {
		reject(msg)
		return
	}
	exists := slices.ContainsFunc(c.VLANs, func(x store.VLANConfig) bool { return x.VID == v.VID })
	switch {
	case isNew && exists:
		reject(T("VLAN %d gibt es schon.", v.VID))
		return
	case !isNew && !exists:
		s.fail(w, r, http.StatusNotFound, "Dieses Netz gibt es nicht.")
		return
	}
	if msg := s.vlanProblem(r, f.Groups, v); msg != "" {
		reject(msg)
		return
	}
	if err := s.storeVLAN(r, v, isNew); err != nil {
		reject(string(s.userError(r, "network: store", err)))
		return
	}
	verb := "geändert"
	if isNew {
		verb = "angelegt"
	}
	a := s.applyNow(r, fmt.Sprintf("VLAN %d %s", v.VID, verb))
	vlanSavedAnswer(&a, v, verb, T)
	s.answer(w, r, "/netz", a)
}

// vlanProblem checks a parsed network before it is stored: its group must be one of groups, and it must fit
// the whole configuration (overlapping networks, a relay IP outside the network and the like are rejected here,
// not by a failing apply afterwards). It returns "" or the error text in the language of r.
func (s *Server) vlanProblem(r *http.Request, groups []string, v store.VLANConfig) string {
	if !slices.Contains(groups, v.Group) {
		return s.T(r)("Bitte eine der Gruppen wählen.")
	}
	return s.vlanCheck(r, v)
}

// storeVLAN adds a new network or replaces the stored one.
func (s *Server) storeVLAN(r *http.Request, v store.VLANConfig, isNew bool) error {
	if isNew {
		return s.Store.AddVLAN(r.Context(), s.actor(r), v)
	}
	return s.Store.UpdateVLAN(r.Context(), s.actor(r), v)
}

// vlanSavedAnswer words the answer after a network was stored (verb "angelegt" or "geändert") and applied.
func vlanSavedAnswer(a *applyAnswer, v store.VLANConfig, verb string, T translator) {
	switch {
	case a.OK:
		a.Message = T("%s (VLAN %d) %s.", v.Name, v.VID, T(verb))
	case a.Error != "":
		a.Error = T("%s (VLAN %d) %s, Anwenden fehlgeschlagen: %s", v.Name, v.VID, T(verb), a.Error)
	}
}

// vlanCheck compiles the configuration with v added or replaced (and switched on, so all address
// checks run) and returns an error text in the language of r or "".
func (s *Server) vlanCheck(r *http.Request, v store.VLANConfig) string {
	T := s.T(r)
	in, err := s.Store.BuildInput(r.Context())
	if err != nil {
		return string(s.internal(r, "network: read the configuration", err))
	}
	cv := compile.VLAN{VID: v.VID, Name: v.Name, Group: v.Group, CIDR: netip.MustParsePrefix(v.CIDR),
		RelayIP: netip.MustParseAddr(v.RelayIP), Attach: v.Attach, Parent: v.Parent, Device: v.Device,
		Enabled: true, RatePPS: v.RatePPS, Burst: v.Burst}
	if i := slices.IndexFunc(in.VLANs, func(x compile.VLAN) bool { return x.VID == v.VID }); i >= 0 {
		in.VLANs[i] = cv
	} else {
		in.VLANs = append(in.VLANs, cv)
	}
	// Other VLANs keep their state; overlaps with switched-off networks matter too, because they can be
	// switched on later.
	for i := range in.VLANs {
		in.VLANs[i].Enabled = true
	}
	if _, _, err := compile.Compile(in); err != nil {
		if errors.Is(err, compile.ErrOverlap) {
			return T("Das Netz überschneidet sich mit einem anderen Netz: %s", err.Error())
		}
		return T("Das Netz passt nicht zur Konfiguration: %s", err.Error())
	}
	return ""
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
