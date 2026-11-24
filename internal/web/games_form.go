package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
)

// The create/edit form of a game: reading it and building the game from it (the handlers are in games_edit.go).

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

// readGameForm reads the posted form. The port finder used to post a single spec without index (kind, ports,
// group, direction); that is still accepted as row 0.
func readGameForm(r *http.Request, isNew bool) gameForm {
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
	g := base
	g.ID, g.Name, g.Notes = f.ID, f.Name, f.Notes
	g.Category = firstNonEmpty(f.Category, g.Category, gamedb.CategoryGame)
	g.Confidence = firstNonEmpty(f.Confidence, g.Confidence, "low")
	g.SourceIP = firstNonEmpty(g.SourceIP, gamedb.SourceOriginal)
	g.Aliases = splitAliases(f.Aliases)
	discovery, msg := f.discovery(storedAdv(base), admin, T)
	g.Discovery = discovery
	if msg != "" {
		return g, msg
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
	g.Sources = append(g.Sources, fmt.Sprintf("%s %s (%s)", f.sourceName(), time.Now().Format("2006-01-02"), actor))
	return g, ""
}

// firstNonEmpty returns the first of values that is not "".
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// splitAliases turns the comma separated alias field into a list without blanks (nil when empty).
func splitAliases(field string) []string {
	var aliases []string
	for _, a := range strings.Split(field, ",") {
		if a = strings.TrimSpace(a); a != "" {
			aliases = append(aliases, a)
		}
	}
	return aliases
}

// storedAdv is the set of hidden advanced values (advOf) the discovery rows of the stored game carry.
func storedAdv(base gamedb.Game) map[string]bool {
	stored := map[string]bool{}
	for _, sp := range base.Discovery {
		stored[advOf(sp)] = true
	}
	return stored
}

// discovery builds the discovery specs from the filled form rows (rows without ports are skipped). A row whose
// hidden advanced value is corrupt, or not one of stored while the user is no admin, fails the whole form.
func (f gameForm) discovery(stored map[string]bool, admin bool, T translator) ([]gamedb.Spec, string) {
	var specs []gamedb.Spec
	for i, sp := range f.Specs {
		if sp.Ports == "" {
			continue
		}
		spec, ok := sp.spec(stored, admin)
		if !ok {
			return specs, T("Zeile %d: interne Felder beschädigt, bitte die Seite neu laden.", i+1)
		}
		specs = append(specs, spec)
	}
	return specs, ""
}

// spec is the discovery spec of one form row; ok is false when its hidden advanced value is not acceptable.
func (sp specForm) spec(stored map[string]bool, admin bool) (spec gamedb.Spec, ok bool) {
	if sp.Adv != "" {
		if err := json.Unmarshal([]byte(sp.Adv), &spec); err != nil || (!admin && !stored[advOf(spec)]) {
			return spec, false
		}
	}
	spec.Kind, spec.Ports, spec.Direction, spec.Group = sp.Kind, sp.Ports, sp.Direction, ""
	if sp.Kind == gamedb.KindMulticast {
		spec.Group = sp.Group
	}
	if spec.Direction == "" {
		spec.Direction = "both"
	}
	return spec, true
}

// sourceName is the origin written into the game's sources note.
func (f gameForm) sourceName() string {
	if f.fromFinder() {
		return "Port-Finder"
	}
	return "Web-UI"
}

// fromFinder reports whether the form was opened from the port finder.
func (f gameForm) fromFinder() bool { return f.From == "finder" }

// origin is the store origin of a game created from this form.
func (f gameForm) origin() string {
	if f.fromFinder() {
		return "sniffer"
	}
	return "custom"
}
