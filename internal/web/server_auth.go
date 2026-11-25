package web

import (
	"context"
	"net/http"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// ---- sessions, roles, CSRF ----

type ctxKey struct{}

func sessionFrom(r *http.Request) store.Session {
	se, _ := r.Context().Value(ctxKey{}).(store.Session)
	return se
}

// need wraps a handler: valid session, minimum role, CSRF token on writes.
func (s *Server) need(role string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store") // nothing behind the login belongs into a cache
		T := s.T(r)
		se, err := s.Store.SessionByToken(r.Context(), s.sessionToken(r))
		if err != nil {
			s.noSession(w, r, T)
			return
		}
		// the handler that renders the refusal needs the session too (name and menu on the error page)
		r = r.WithContext(context.WithValue(r.Context(), ctxKey{}, se))
		if store.RoleRank(se.User.Role) < store.RoleRank(role) {
			s.fail(w, r, http.StatusForbidden, T("Dafür fehlt dir die Rolle %s.", T(roleName(role))))
			return
		}
		if r.Method == http.MethodPost && !sameToken(csrfToken(r), se.CSRF) {
			s.fail(w, r, http.StatusForbidden, msgCSRF)
			return
		}
		h(w, r)
	})
}

// noSession answers a request that needs a session but has none: scripts and the live stream get a JSON 401,
// a browser the setup on a fresh installation, else the login page.
func (s *Server) noSession(w http.ResponseWriter, r *http.Request, T translator) {
	switch {
	case wantsJSON(r) || strings.Contains(r.Header.Get("Accept"), "text/event-stream"):
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": T("Nicht angemeldet.")})
	case s.needsAccount(r.Context()): // a fresh installation: every page leads to the setup
		http.Redirect(w, r, "/einrichten", http.StatusSeeOther)
	default:
		http.Redirect(w, r, "/login", http.StatusSeeOther)
	}
}

// msgCSRF is shown when a form comes without the token of the session (an old tab, or another site).
const msgCSRF = "Sitzung abgelaufen oder Formular veraltet. Lade die Seite neu."

// csrfToken is the token a write request carries: the header set by the UI scripts, else the hidden form field.
func csrfToken(r *http.Request) string {
	if tok := r.Header.Get("X-CSRF-Token"); tok != "" {
		return tok
	}
	return r.PostFormValue("csrf")
}

func (s *Server) actor(r *http.Request) string { return sessionFrom(r).User.Name }
