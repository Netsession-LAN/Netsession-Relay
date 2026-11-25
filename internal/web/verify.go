package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
	"github.com/Netsession-LAN/Netsession-Relay/internal/uimsg"
)

// Verifications ("tested at event X"): after an event the checked games go back into games/games.yaml by commit
// (the YAML exports are in verify_yaml.go).

func resultName(r string) string {
	switch r {
	case store.ResultWorks:
		return "Geht"
	case store.ResultFails:
		return "Geht nicht"
	case store.ResultPartial:
		return "Teilweise"
	}
	return r
}

func resultTone(r string) string {
	switch r {
	case store.ResultWorks:
		return "ok"
	case store.ResultFails:
		return "danger"
	}
	return "warn"
}

// resultIcon is the sprite icon that goes with a result (colour never stands alone).
func resultIcon(r string) string {
	if r == store.ResultWorks {
		return "check"
	}
	return "alert"
}

// resultBadge is the word on the badge of a game whose newest test has this result.
func resultBadge(r string) string {
	switch r {
	case store.ResultWorks:
		return "verifiziert"
	case store.ResultFails:
		return "geht nicht"
	case store.ResultPartial:
		return "teilweise"
	}
	return r
}

func (s *Server) verifyAdd(w http.ResponseWriter, r *http.Request) {
	T := s.T(r)
	id := r.PathValue("id")
	path := "/spiele/" + url.PathEscape(id)
	v := store.Verification{GameID: id, Result: r.PostFormValue("result"), Variant: r.PostFormValue("variant"), Note: r.PostFormValue("note")}
	if err := s.Store.AddVerification(r.Context(), s.actor(r), v); err != nil {
		switch {
		case errors.Is(err, store.ErrInvalid):
			err = uimsg.Error("Bitte ein Ergebnis wählen.")
		case errors.Is(err, store.ErrNotFound):
			err = uimsg.Error("Dieses Spiel gibt es nicht.")
		}
		back(w, r, path, "", s.userError(r, "game: test result", err))
		return
	}
	back(w, r, path, T("Prüfung eingetragen: %s.", T(resultName(v.Result))), nil)
}

func (s *Server) verifyDelete(w http.ResponseWriter, r *http.Request) {
	T := s.T(r)
	path := "/spiele/" + url.PathEscape(r.PathValue("id"))
	vid, _ := strconv.ParseInt(r.PathValue("vid"), 10, 64)
	if err := s.Store.DeleteVerification(r.Context(), s.actor(r), vid); err != nil {
		back(w, r, path, "", errors.New(T("Diesen Eintrag gibt es nicht.")))
		return
	}
	back(w, r, path, T("Prüfung gelöscht."), nil)
}
