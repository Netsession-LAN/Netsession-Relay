package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Netsession2024/Netsession-Relay/internal/gamedb"
	"github.com/Netsession2024/Netsession-Relay/internal/store"
)

// gameForm is the create/edit form of a game (operator and up). Values stay strings so a rejected form is
// shown again as typed.
type gameForm struct {
	New        bool
	From       string // "finder" when opened from the port finder (origin sniffer)
	ID, Name   string
	Aliases    string // comma separated
	Category   string
	Confidence string
	Notes      string
	Enable     bool
	Specs      []specForm
	Origin     string // edit: seed | custom | sniffer | import
	Err        string
}

// specForm is one discovery row. Adv carries the fields the form does not show (source/dst/TTL mode, rate,
// payload limit) as JSON, so editing a seed game keeps them.
type specForm struct {
	Kind, Ports, Group, Direction string
	Adv                           string
}

// emptySpecRows is the number of blank discovery rows offered below the existing ones.
const emptySpecRows = 2

func blankSpec() specForm { return specForm{Kind: gamedb.KindBroadcast, Direction: "both"} }

func (f *gameForm) pad() {
	for range emptySpecRows {
		f.Specs = append(f.Specs, blankSpec())
	}
}

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
	http.Redirect(w, r, "/spiele/neu?"+q.Encode(), http.StatusSeeOther)
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

// readGameForm reads the posted form. The port finder used to post a single spec without index (kind, ports,
// group, direction); that is still accepted as row 0.
func readGameForm(r *http.Request, isNew bool) gameForm {
	_ = r.ParseForm()
	v := func(k string) string { return strings.TrimSpace(r.PostFormValue(k)) }
	f := gameForm{New: isNew, From: v("from"), ID: v("id"), Name: v("name"), Aliases: v("aliases"),
		Category: v("category"), Confidence: v("confidence"), Notes: v("notes"), Enable: v("enable") == "1"}
	if v("ports") != "" {
		f.Specs = append(f.Specs, specForm{Kind: v("kind"), Ports: v("ports"), Group: v("group"), Direction: v("direction")})
	}
	for i := 0; i < 32; i++ {
		k := strconv.Itoa(i)
		if _, ok := r.PostForm["kind"+k]; !ok {
			break
		}
		f.Specs = append(f.Specs, specForm{Kind: v("kind" + k), Ports: v("ports" + k), Group: v("group" + k),
			Direction: v("direction" + k), Adv: v("adv" + k)})
	}
	return f
}

// advOf is the hidden form value for the fields of a spec the form does not show ("" when all are defaults).
func advOf(sp gamedb.Spec) string {
	sp.Kind, sp.Ports, sp.Group, sp.Direction = "", "", "", ""
	if sp == (gamedb.Spec{}) {
		return ""
	}
	b, _ := json.Marshal(sp)
	return string(b)
}

// game builds the game from the form; base is the stored game when editing (keeps game ports, sources …). The
// error text comes back in the language of T. The hidden advanced fields (source/destination/TTL mode, rate,
// payload limit) are an admin matter: from anybody else they are only taken when they are exactly what a row
// of the stored game already has, so an operator can edit ports without losing or changing them.
func (f gameForm) game(base gamedb.Game, actor string, admin bool, T translator) (gamedb.Game, string) {
	stored := map[string]bool{}
	for _, sp := range base.Discovery {
		stored[advOf(sp)] = true
	}
	g := base
	g.ID, g.Name, g.Notes = f.ID, f.Name, f.Notes
	if f.Category != "" {
		g.Category = f.Category
	}
	if g.Category == "" {
		g.Category = gamedb.CategoryGame
	}
	if f.Confidence != "" {
		g.Confidence = f.Confidence
	}
	if g.Confidence == "" {
		g.Confidence = "low"
	}
	if g.SourceIP == "" {
		g.SourceIP = gamedb.SourceOriginal
	}
	g.Aliases = nil
	for _, a := range strings.Split(f.Aliases, ",") {
		if a = strings.TrimSpace(a); a != "" {
			g.Aliases = append(g.Aliases, a)
		}
	}
	g.Discovery = nil
	for i, sp := range f.Specs {
		if sp.Ports == "" {
			continue
		}
		var spec gamedb.Spec
		if sp.Adv != "" {
			if err := json.Unmarshal([]byte(sp.Adv), &spec); err != nil || (!admin && !stored[advOf(spec)]) {
				return g, T("Zeile %d: interne Felder beschädigt, bitte die Seite neu laden.", i+1)
			}
		}
		spec.Kind, spec.Ports, spec.Direction, spec.Group = sp.Kind, sp.Ports, sp.Direction, ""
		if sp.Kind == gamedb.KindMulticast {
			spec.Group = sp.Group
		}
		if spec.Direction == "" {
			spec.Direction = "both"
		}
		g.Discovery = append(g.Discovery, spec)
	}
	switch {
	case len(g.Discovery) > 0:
		g.RelayNeeded = gamedb.RelayYes
	case base.RelayNeeded == gamedb.RelayNo:
		g.RelayNeeded = gamedb.RelayNo
	default:
		return g, T("Bitte mindestens einen UDP-Port eintragen.")
	}
	if g.RelayNeeded != gamedb.RelayYes {
		g.DefaultEnabled = false
	}
	src := "Web-UI"
	if f.From == "finder" {
		src = "Port-Finder"
	}
	g.Sources = append(g.Sources, fmt.Sprintf("%s %s (%s)", src, time.Now().Format("2006-01-02"), actor))
	return g, ""
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
	origin := "custom"
	if f.From == "finder" {
		origin = "sniffer"
	}
	if err := s.Store.AddGame(r.Context(), s.actor(r), origin, g); err != nil {
		reject(gameErrorText(T, err))
		return
	}
	msg = T("%s angelegt (aus).", f.Name)
	if f.Enable {
		if err := s.Store.SetGameEnabled(r.Context(), s.actor(r), g.ID, true); err == nil {
			if a := s.applyNow(r, g.ID+" angelegt und an"); a.OK {
				msg = T("%s angelegt und eingeschaltet (Revision %d).", f.Name, a.Revision)
			} else {
				msg = T("%s angelegt, Anwenden fehlgeschlagen: %s", f.Name, a.Error)
			}
		}
	}
	http.Redirect(w, r, "/spiele/"+url.PathEscape(g.ID)+"?ok="+urlQuery(msg), http.StatusSeeOther)
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
		reject(gameErrorText(T, err))
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
			err = errors.New(T("Spiele aus der Spiele-Datenbank lassen sich nicht löschen, nur ausschalten."))
		}
		back(w, r, "/spiele/"+url.PathEscape(id), "", err)
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
