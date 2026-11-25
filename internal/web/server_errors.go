package web

import (
	"context"
	"errors"
	"net/http"

	"github.com/Netsession-LAN/Netsession-Relay/internal/control"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"github.com/Netsession-LAN/Netsession-Relay/internal/uimsg"
)

// Errors for the user: what failed, worded for the page, with the cause in the log.

// msgInternal is what the user reads when something failed that cannot be fixed from the page (database, file
// system, a bug). The cause is in the log, not on the page: texts of the database driver or of the operating
// system help nobody at the browser and do not belong into a URL.
const msgInternal = "Das hat wegen eines internen Fehlers nicht geklappt. Die Ursache steht im Log des Relays."

// internal logs err with what was being done and returns the sentence shown instead, in the language of r.
func (s *Server) internal(r *http.Request, what string, err error) uimsg.Error {
	if !errors.Is(err, context.Canceled) {
		s.Log.Error("request failed", "what", what, "method", r.Method, "path", r.URL.Path, "user", s.actor(r), "err", err)
	}
	return uimsg.Error(s.T(r)(msgInternal))
}

// failInternal answers a request with the page for an internal error (HTTP 500).
func (s *Server) failInternal(w http.ResponseWriter, r *http.Request, what string, err error) {
	s.fail(w, r, http.StatusInternalServerError, string(s.internal(r, what, err)))
}

// userError turns an error from below the web part into what the user reads, in the language of r. A message
// that is already worded (uimsg.Error) is translated; the errors with an identity that can reach any handler
// get a general sentence (handlers that know better map them before they call this); everything else is an
// internal error: logged, and shown without its text.
func (s *Server) userError(r *http.Request, what string, err error) uimsg.Error {
	T := s.T(r)
	var worded uimsg.Error
	switch {
	case errors.As(err, &worded):
		return uimsg.Error(lookupQuiet(s.langOf(r), string(worded)))
	case errors.Is(err, store.ErrNotFound):
		return uimsg.Error(T("Das gibt es nicht (mehr). Lade die Seite neu."))
	case errors.Is(err, store.ErrExists):
		return uimsg.Error(T("Das gibt es schon."))
	case errors.Is(err, store.ErrInvalid):
		return uimsg.Error(T("Die Eingabe ist ungültig."))
	}
	return s.internal(r, what, err)
}

// applyError words a failed apply or rollback (control.Apply, control.Rollback). The mistakes of a configuration
// and the reason of the engine are shown as they are: they name networks, games and interfaces, and the admin
// needs them to fix the configuration.
func (s *Server) applyError(r *http.Request, res control.ApplyResult, err error) uimsg.Error {
	T := s.T(r)
	switch {
	case errors.Is(err, control.ErrConfigInvalid):
		return uimsg.Error(T("Die Konfiguration passt nicht zusammen: %s", res.Invalid))
	case errors.Is(err, control.ErrEngineRejected):
		return uimsg.Error(T("Die Engine hat den Stand abgelehnt und läuft mit dem alten weiter: %s", res.Engine.Error))
	case errors.Is(err, control.ErrRevisionNotFound):
		return uimsg.Error(T("Diese Revision gibt es nicht."))
	case errors.Is(err, control.ErrRevisionRestore):
		s.Log.Error("rollback: revision cannot be restored", "user", s.actor(r), "err", err)
		return uimsg.Error(T("Dieser Stand lässt sich nicht wiederherstellen, meist weil er ein Spiel enthält, das es nicht mehr gibt. Details stehen im Log."))
	case errors.Is(err, control.ErrEngineUnreachable):
		s.Log.Error("apply: engine not reachable", "user", s.actor(r), "err", err)
		return uimsg.Error(T("Die Engine antwortet nicht. Die Änderung ist gespeichert, aber nicht angewendet."))
	}
	return s.userError(r, "apply", err)
}
