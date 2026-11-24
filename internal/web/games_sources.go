package web

import (
	"errors"
	"net/http"
	"net/netip"
	"net/url"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"github.com/Netsession-LAN/Netsession-Relay/internal/uimsg"
)

// The source filter of a game: which senders of a switched-on game are relayed (empty = every sender).

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
		a.Message = s.sourcesAppliedMessage(r, sources)
	}
	s.answer(w, r, path, a)
}

// sourcesAppliedMessage is the message after a source filter was stored and applied. It warns about entries
// that lie in no enabled network and so have no effect.
func (s *Server) sourcesAppliedMessage(r *http.Request, sources []netip.Prefix) string {
	T := s.T(r)
	msg := T("Absender-Filter entfernt und angewendet.")
	if len(sources) > 0 {
		msg = T("Absender-Filter gespeichert und angewendet.")
	}
	c, err := s.Store.ExportConfig(r.Context())
	if err != nil {
		return msg
	}
	if off := sourcesOutside(compile.FormatSources(sources), c.VLANs); len(off) > 0 {
		msg += " " + T("Trifft kein Netz und wirkt nicht: %s.", strings.Join(off, ", "))
	}
	return msg
}

// sourcesOutside lists the entries of a source filter that lie in no enabled network.
func sourcesOutside(sources []string, vlans []store.VLANConfig) []string {
	var off []string
	for _, v := range sourceViews(sources, vlans) {
		if v.Net == "" {
			off = append(off, v.Source)
		}
	}
	return off
}

// sourceView is one entry of a source filter with the network it restricts ("" = lies in no enabled network).
type sourceView struct {
	Source, Net string
}

func sourceViews(sources []string, vlans []store.VLANConfig) []sourceView {
	out := make([]sourceView, 0, len(sources))
	for _, src := range sources {
		out = append(out, sourceView{Source: src, Net: sourceNet(src, vlans)})
	}
	return out
}

// sourceNet is the name of the first enabled network that contains the source filter entry src ("" when none
// does or src is not a single address or network).
func sourceNet(src string, vlans []store.VLANConfig) string {
	ps, err := compile.ParseSources(src)
	if err != nil || len(ps) != 1 {
		return ""
	}
	for _, vl := range vlans {
		if vlanContains(vl, ps[0]) {
			return vl.Name
		}
	}
	return ""
}

// vlanContains reports whether p lies within the network of vl and vl is enabled.
func vlanContains(vl store.VLANConfig, p netip.Prefix) bool {
	if !vl.Enabled {
		return false
	}
	n, err := netip.ParsePrefix(vl.CIDR)
	return err == nil && compile.PrefixWithin(p, n.Masked())
}
