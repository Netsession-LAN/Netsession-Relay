package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// Saving a network (create and edit): the checks before it is stored and the answer after.

func (s *Server) vlanSave(w http.ResponseWriter, r *http.Request, isNew bool) {
	T := s.T(r)
	f := readVLANForm(r, isNew)
	c, err := s.Store.ExportConfig(r.Context())
	if err != nil {
		s.failInternal(w, r, "network: configuration", err)
		return
	}
	f.Groups = s.vlanGroups(c)
	reject := func(msg string) {
		f.Err = msg
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, r, "network-edit", vlanEditTitle(f.Name, isNew, T), "network", f)
	}

	v, msg := f.parse(T)
	if msg != "" {
		reject(msg)
		return
	}
	if !isNew && !vlanIDTaken(c.VLANs, v.VID) {
		s.fail(w, r, http.StatusNotFound, "Dieses Netz gibt es nicht.")
		return
	}
	if msg := s.vlanProblem(r, c.VLANs, f.Groups, v, isNew); msg != "" {
		reject(msg)
		return
	}
	if err := s.storeVLAN(r, v, isNew); err != nil {
		reject(string(s.userError(r, "network: store", err)))
		return
	}
	s.answerVLANSaved(w, r, v, isNew)
}

// vlanIDTaken reports whether one of the networks has the VLAN ID vid.
func vlanIDTaken(vlans []store.VLANConfig, vid int) bool {
	return slices.ContainsFunc(vlans, func(x store.VLANConfig) bool { return x.VID == vid })
}

// vlanProblem checks a parsed network before it is stored: a new network must not take a VLAN ID of the stored
// networks (vlans), its group must be one of groups, and it must fit the whole configuration (overlapping networks, a
// relay IP outside the network and the like are rejected here, not by a failing apply afterwards). It returns ""
// or the error text in the language of r.
func (s *Server) vlanProblem(r *http.Request, vlans []store.VLANConfig, groups []string, v store.VLANConfig, isNew bool) string {
	if isNew && vlanIDTaken(vlans, v.VID) {
		return s.T(r)("VLAN %d gibt es schon.", v.VID)
	}
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

// answerVLANSaved applies the configuration after a network was stored and words the answer.
func (s *Server) answerVLANSaved(w http.ResponseWriter, r *http.Request, v store.VLANConfig, isNew bool) {
	T := s.T(r)
	verb := "geändert"
	if isNew {
		verb = "angelegt"
	}
	a := s.applyNow(r, fmt.Sprintf("VLAN %d %s", v.VID, verb))
	switch {
	case a.OK:
		a.Message = T("%s (VLAN %d) %s.", v.Name, v.VID, T(verb))
	case a.Error != "":
		a.Error = T("%s (VLAN %d) %s, Anwenden fehlgeschlagen: %s", v.Name, v.VID, T(verb), a.Error)
	}
	s.answer(w, r, "/netz", a)
}

// vlanCheck compiles the configuration with v added or replaced (and switched on, so all address
// checks run) and returns an error text in the language of r or "".
func (s *Server) vlanCheck(r *http.Request, v store.VLANConfig) string {
	T := s.T(r)
	in, err := s.Store.BuildInput(r.Context())
	if err != nil {
		return string(s.internal(r, "network: read the configuration", err))
	}
	in.VLANs = vlansWithCandidate(in.VLANs, v)
	if _, _, err := compile.Compile(in); err != nil {
		if errors.Is(err, compile.ErrOverlap) {
			return T("Das Netz überschneidet sich mit einem anderen Netz: %s", err.Error())
		}
		return T("Das Netz passt nicht zur Konfiguration: %s", err.Error())
	}
	return ""
}

// vlansWithCandidate is vlans with v added or replaced, all switched on: the other networks keep their state
// elsewhere, but overlaps with switched-off networks matter too, because they can be switched on later.
func vlansWithCandidate(vlans []compile.VLAN, v store.VLANConfig) []compile.VLAN {
	cv := compile.VLAN{VID: v.VID, Name: v.Name, Group: v.Group, CIDR: netip.MustParsePrefix(v.CIDR),
		RelayIP: netip.MustParseAddr(v.RelayIP), Attach: v.Attach, Parent: v.Parent, Device: v.Device,
		Enabled: true, RatePPS: v.RatePPS, Burst: v.Burst}
	if i := slices.IndexFunc(vlans, func(x compile.VLAN) bool { return x.VID == v.VID }); i >= 0 {
		vlans[i] = cv
	} else {
		vlans = append(vlans, cv)
	}
	for i := range vlans {
		vlans[i].Enabled = true
	}
	return vlans
}
