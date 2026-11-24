package web

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/Netsession2024/Netsession-Relay/internal/store"
	"github.com/Netsession2024/Netsession-Relay/internal/uimsg"
)

// Known service hosts (admin, page System): hosts that send discovery packets without being players, such
// as a Discord game server notifier. The players page lists them separately and does not count them.

func (s *Server) serviceSet(w http.ResponseWriter, r *http.Request) {
	ip, name := r.PostFormValue("ip"), r.PostFormValue("name")
	T := s.T(r)
	if err := s.Store.SetService(r.Context(), s.actor(r), ip, name, r.PostFormValue("note")); err != nil {
		if errors.Is(err, store.ErrInvalid) {
			err = uimsg.Error("Bitte eine IPv4-Adresse und einen Namen (höchstens 60 Zeichen) angeben.")
		}
		back(w, r, "/system", "", s.userError(r, "service host: store", err))
		return
	}
	back(w, r, backPath(r, "/system"), T("%s (%s) ist als Dienst markiert und zählt nicht mehr als Spieler:in.", name, ip), nil)
}

func (s *Server) serviceDelete(w http.ResponseWriter, r *http.Request) {
	ip := r.PathValue("ip")
	if err := s.Store.DeleteService(r.Context(), s.actor(r), ip); err != nil {
		back(w, r, "/system", "", errors.New(s.T(r)("Diesen Dienst gibt es nicht.")))
		return
	}
	back(w, r, "/system", s.T(r)("%s zählt wieder als Spieler:in.", ip), nil)
}

// serviceFormURL links from a player row to the prefilled form on the system page.
func serviceFormURL(ip string) string { return "/system?dienst=" + url.QueryEscape(ip) + "#dienste" }
