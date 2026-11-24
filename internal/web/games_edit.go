package web

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/uimsg"

	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

func (s *Server) gameNewForm(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := gameForm{New: true, From: q.Get("from"), Category: gamedb.CategoryGame, Confidence: "low", Enable: true}
	if q.Get("port") != "" {
		sp := specForm{Kind: q.Get("kind"), Ports: q.Get("port"), Group: q.Get("group"), Direction: "both"}
		if sp.Kind != gamedb.KindMulticast {
			sp.Kind, sp.Group = gamedb.KindBroadcast, ""
		}
		f.Specs = append(f.Specs, sp)
	}
	f.pad()
	s.render(w, r, "game-edit", "Spiel anlegen", "games", f)
}

// finderNewForm keeps the old port-finder link working.
func (s *Server) finderNewForm(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	q.Set("from", "finder")
	http.Redirect(w, r, "/spiele/neu?"+q.Encode(), http.StatusSeeOther) //nolint:gosec // G710: the target is checked by localPath or is a fixed path
}

func (s *Server) gameEditForm(w http.ResponseWriter, r *http.Request) {
	g, err := s.Store.Game(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "Dieses Spiel gibt es nicht.")
		return
	}
	f := gameForm{ID: g.ID, Name: g.Name, Aliases: strings.Join(g.Aliases, ", "), Category: g.Category,
		Confidence: g.Confidence, Notes: g.Notes, Enable: g.Enabled, Origin: g.Origin}
	for _, sp := range g.Discovery {
		f.Specs = append(f.Specs, specForm{Kind: sp.Kind, Ports: sp.Ports, Group: sp.Group, Direction: sp.Direction, Adv: advOf(sp)})
	}
	f.pad()
	s.render(w, r, "game-edit", s.T(r)("%s bearbeiten", g.Name), "games", f)
}

func (s *Server) gameCreate(w http.ResponseWriter, r *http.Request) {
	T := s.T(r)
	f := readGameForm(r, true)
	if strings.HasPrefix(r.URL.Path, "/finder/") {
		f.From = "finder"
	}
	if f.ID == "" {
		f.ID = slug(f.Name)
	}
	reject := func(msg string) {
		f.Err = msg
		f.pad()
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, r, "game-edit", "Spiel anlegen", "games", f)
	}
	if f.ID == "neu" {
		reject(T("Die Kennung „neu“ ist reserviert."))
		return
	}
	g, msg := f.game(gamedb.Game{}, s.actor(r), s.isAdmin(r), T)
	if msg != "" {
		reject(msg)
		return
	}
	if err := s.Store.AddGame(r.Context(), s.actor(r), f.origin(), g); err != nil {
		reject(s.gameError(r, err))
		return
	}
	msg = T("%s angelegt (aus).", f.Name)
	if f.Enable {
		msg = s.enableNewGame(r, f.Name, g.ID, msg)
	}
	http.Redirect(w, r, "/spiele/"+url.PathEscape(g.ID)+"?ok="+urlQuery(msg), http.StatusSeeOther)
}

// enableNewGame switches a just created game on and applies the config. It returns the message for the
// redirect; created is kept when switching it on fails.
func (s *Server) enableNewGame(r *http.Request, name, id, created string) string {
	T := s.T(r)
	if err := s.Store.SetGameEnabled(r.Context(), s.actor(r), id, true); err != nil {
		return created
	}
	a := s.applyNow(r, id+" angelegt und an")
	if !a.OK {
		return T("%s angelegt, Anwenden fehlgeschlagen: %s", name, a.Error)
	}
	return T("%s angelegt und eingeschaltet (Revision %d).", name, a.Revision)
}

func (s *Server) gameUpdate(w http.ResponseWriter, r *http.Request) {
	cur, err := s.Store.Game(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "Dieses Spiel gibt es nicht.")
		return
	}
	T := s.T(r)
	f := readGameForm(r, false)
	f.ID, f.Origin, f.Enable = cur.ID, cur.Origin, cur.Enabled
	reject := func(msg string) {
		f.Err = msg
		f.pad()
		w.WriteHeader(http.StatusUnprocessableEntity)
		s.render(w, r, "game-edit", T("%s bearbeiten", cur.Name), "games", f)
	}
	g, msg := f.game(cur.Game, s.actor(r), s.isAdmin(r), T)
	if msg != "" {
		reject(msg)
		return
	}
	if err := s.Store.UpdateGame(r.Context(), s.actor(r), g); err != nil {
		reject(s.gameError(r, err))
		return
	}
	msg = T("%s gespeichert.", g.Name)
	if cur.Enabled {
		if a := s.applyNow(r, g.ID+" geändert"); a.OK {
			msg = T("%s gespeichert und angewendet (Revision %d).", g.Name, a.Revision)
		} else {
			msg = T("%s gespeichert, Anwenden fehlgeschlagen: %s", g.Name, a.Error)
		}
	}
	http.Redirect(w, r, "/spiele/"+url.PathEscape(g.ID)+"?ok="+urlQuery(msg), http.StatusSeeOther)
}

func (s *Server) gameDelete(w http.ResponseWriter, r *http.Request) {
	T := s.T(r)
	id := r.PathValue("id")
	cur, err := s.Store.Game(r.Context(), id)
	if err != nil {
		back(w, r, "/spiele", "", errors.New(T("Dieses Spiel gibt es nicht.")))
		return
	}
	if err := s.Store.DeleteGame(r.Context(), s.actor(r), id); err != nil {
		if errors.Is(err, store.ErrSeedGame) {
			err = uimsg.Error("Spiele aus der Spiele-Datenbank lassen sich nicht löschen, nur ausschalten.")
		}
		back(w, r, "/spiele/"+url.PathEscape(id), "", s.userError(r, "game: delete", err))
		return
	}
	msg := T("%s gelöscht.", cur.Name)
	if cur.Enabled {
		if a := s.applyNow(r, id+" gelöscht"); !a.OK {
			msg = T("%s gelöscht, Anwenden fehlgeschlagen: %s", cur.Name, a.Error)
		}
	}
	back(w, r, "/spiele", msg, nil)
}
