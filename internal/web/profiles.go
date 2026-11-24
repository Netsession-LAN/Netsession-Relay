package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"github.com/Netsession-LAN/Netsession-Relay/internal/uimsg"
)

// Flow profiles: which VLAN group sees the games of which group (admin, page "Netz"). A game uses the default
// profile unless one is picked on its page.

// profileError words a store error about a flow profile in the language of r.
func (s *Server) profileError(r *http.Request, err error) uimsg.Error {
	switch {
	case errors.Is(err, store.ErrExists):
		err = uimsg.Error("Ein Profil mit diesem Namen gibt es schon.")
	case errors.Is(err, store.ErrInvalid):
		err = uimsg.Error("Profilname: Kleinbuchstaben, Ziffern, _ und -, höchstens 32 Zeichen.")
	case errors.Is(err, store.ErrDefaultProfile):
		err = uimsg.Error("Das Standardprofil lässt sich nicht löschen. Mach erst ein anderes zum Standard.")
	case errors.Is(err, store.ErrNotFound):
		err = uimsg.Error("Dieses Profil gibt es nicht.")
	}
	return s.userError(r, "flow profile", err)
}

// msgProfileReserved: "neu" is part of the address of the form that creates a profile.
const msgProfileReserved = uimsg.Error("Der Name „neu“ ist reserviert.")

// profileSave stores the checked cells of the matrix form (fields e = "src>dst").
func (s *Server) profileSave(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var edges []compile.Edge
	for _, v := range r.PostForm["e"] {
		src, dst, ok := strings.Cut(v, ">")
		if !ok || src == "" || dst == "" {
			continue
		}
		edges = append(edges, compile.Edge{Src: src, Dst: dst})
	}
	if err := s.Store.SetProfileEdges(r.Context(), s.actor(r), name, edges); err != nil {
		back(w, r, "/netz", "", s.profileError(r, err))
		return
	}
	a := s.applyNow(r, "Profil "+name+" geändert")
	if a.OK {
		a.Message = s.T(r)("Profil %s gespeichert und angewendet.", name)
	}
	s.answer(w, r, "/netz", a)
}

func (s *Server) profileCreate(w http.ResponseWriter, r *http.Request) {
	name := strings.ToLower(strings.TrimSpace(r.PostFormValue("name")))
	if name == "neu" {
		back(w, r, "/netz", "", s.userError(r, "flow profile", msgProfileReserved))
		return
	}
	if err := s.Store.AddProfile(r.Context(), s.actor(r), name, r.PostFormValue("copy")); err != nil {
		back(w, r, "/netz", "", s.profileError(r, err))
		return
	}
	back(w, r, "/netz", s.T(r)("Profil %s angelegt. Noch nutzt es kein Spiel.", name), nil)
}

func (s *Server) profileDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.Store.DeleteProfile(r.Context(), s.actor(r), name); err != nil {
		back(w, r, "/netz", "", s.profileError(r, err))
		return
	}
	a := s.applyNow(r, "Profil "+name+" gelöscht")
	if a.OK {
		a.Message = s.T(r)("Profil %s gelöscht. Spiele damit nutzen jetzt das Standardprofil.", name)
	}
	s.answer(w, r, "/netz", a)
}

func (s *Server) profileDefault(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.Store.SetDefaultProfile(r.Context(), s.actor(r), name); err != nil {
		back(w, r, "/netz", "", s.profileError(r, err))
		return
	}
	a := s.applyNow(r, "Standardprofil "+name)
	if a.OK {
		a.Message = s.T(r)("Profil %s ist jetzt Standard.", name)
	}
	s.answer(w, r, "/netz", a)
}

// gameProfile sets the profile of a switched-on game ("" = default).
func (s *Server) gameProfile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	path := "/spiele/" + url.PathEscape(id)
	if err := s.Store.SetGameProfile(r.Context(), s.actor(r), id, r.PostFormValue("profile")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			err = uimsg.Error("Das Profil lässt sich nur für ein eingeschaltetes Spiel wählen.")
		}
		back(w, r, path, "", s.userError(r, "game: flow profile", err))
		return
	}
	a := s.applyNow(r, id+" Profil")
	if a.OK {
		a.Message = s.T(r)("Profil geändert und angewendet.")
	}
	s.answer(w, r, path, a)
}

// gameSources sets the source filter of a switched-on game (field sources, empty = every sender).
func (s *Server) gameSources(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	path := "/spiele/" + url.PathEscape(id) + "#absender"
	T := s.T(r)
	sources, err := compile.ParseSources(r.PostFormValue("sources"))
	if err != nil {
		back(w, r, path, "", errors.New(T("Nur IPv4-Adressen oder Netze, z. B. 10.10.5.20 oder 10.10.5.0/28.")))
		return
	}
	if err := s.Store.SetGameSources(r.Context(), s.actor(r), id, sources); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			err = uimsg.Error("Absender lassen sich nur für ein eingeschaltetes Spiel festlegen.")
		}
		back(w, r, path, "", s.userError(r, "game: source filter", err))
		return
	}
	a := s.applyNow(r, id+" Absender")
	if a.OK {
		a.Message = T("Absender-Filter entfernt und angewendet.")
		if len(sources) > 0 {
			a.Message = T("Absender-Filter gespeichert und angewendet.")
		}
		if c, err := s.Store.ExportConfig(r.Context()); err == nil {
			var off []string
			for _, v := range sourceViews(compile.FormatSources(sources), c.VLANs) {
				if v.Net == "" {
					off = append(off, v.Source)
				}
			}
			if len(off) > 0 {
				a.Message += " " + T("Trifft kein Netz und wirkt nicht: %s.", strings.Join(off, ", "))
			}
		}
	}
	s.answer(w, r, path, a)
}

// sourceView is one entry of a source filter with the network it restricts ("" = lies in no enabled network).
type sourceView struct {
	Source, Net string
}

func sourceViews(sources []string, vlans []store.VLANConfig) []sourceView {
	out := make([]sourceView, 0, len(sources))
	for _, src := range sources {
		v := sourceView{Source: src}
		ps, err := compile.ParseSources(src)
		for _, vl := range vlans {
			n, perr := netip.ParsePrefix(vl.CIDR)
			if err == nil && perr == nil && len(ps) == 1 && vl.Enabled && compile.PrefixWithin(ps[0], n.Masked()) {
				v.Net = vl.Name
				break
			}
		}
		out = append(out, v)
	}
	return out
}

// profileEdge switches one cell of the matrix (fields src, dst, on=1|0).
func (s *Server) profileEdge(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	e := compile.Edge{Src: r.PostFormValue("src"), Dst: r.PostFormValue("dst")}
	on := r.PostFormValue("on") == "1"
	if e.Src == "" || e.Dst == "" {
		s.answer(w, r, "/netz", applyAnswer{Error: s.T(r)("Zelle ungültig.")})
		return
	}
	if err := s.Store.SetProfileEdge(r.Context(), s.actor(r), name, e, on); err != nil {
		s.answer(w, r, "/netz", applyAnswer{Error: string(s.profileError(r, err))})
		return
	}
	a := s.applyNow(r, fmt.Sprintf("Profil %s: %s → %s %s", name, e.Src, e.Dst, onOff(on)))
	a.Enabled = on
	if a.OK {
		T := s.T(r)
		if on {
			a.Message = T("Spiele aus %s gehen jetzt nach %s (Profil %s).", T(groupName(e.Src)), T(groupName(e.Dst)), name)
		} else {
			a.Message = T("Spiele aus %s gehen nicht mehr nach %s (Profil %s).", T(groupName(e.Src)), T(groupName(e.Dst)), name)
		}
	}
	s.answer(w, r, "/netz", a)
}

func (s *Server) profileRename(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	to := strings.ToLower(strings.TrimSpace(r.PostFormValue("to")))
	if to == "neu" {
		back(w, r, "/netz", "", s.userError(r, "flow profile", msgProfileReserved))
		return
	}
	if err := s.Store.RenameProfile(r.Context(), s.actor(r), name, to); err != nil {
		back(w, r, "/netz", "", s.profileError(r, err))
		return
	}
	a := s.applyNow(r, "Profil "+name+" → "+to)
	if a.OK {
		a.Message = s.T(r)("Profil heißt jetzt %s.", to)
	}
	s.answer(w, r, "/netz", a)
}
