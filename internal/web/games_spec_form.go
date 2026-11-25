package web

import (
	"encoding/json"

	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
)

// The rows of the port list in the game form: reading them and turning them into the specs of the game.

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

// advOf is the hidden form value for the fields of a spec the form does not show ("" when all are defaults).
func advOf(sp gamedb.Spec) string {
	sp.Kind, sp.Ports, sp.Group, sp.Direction = "", "", "", ""
	if sp == (gamedb.Spec{}) {
		return ""
	}
	b, _ := json.Marshal(sp)
	return string(b)
}

// storedAdv is the set of hidden advanced values (advOf) the discovery rows of the stored game carry.
func storedAdv(base gamedb.Game) map[string]bool {
	stored := map[string]bool{}
	for _, sp := range base.Discovery {
		stored[advOf(sp)] = true
	}
	return stored
}

// spec is the discovery spec of one form row; ok is false when its hidden advanced value is not acceptable.
func (sp specForm) spec(stored map[string]bool, admin bool) (gamedb.Spec, bool) {
	spec, ok := sp.advanced(stored, admin)
	if !ok {
		return spec, false
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

// advanced decodes the row's hidden advanced value (empty: none). Only an admin may send one that is not stored
// for the game already.
func (sp specForm) advanced(stored map[string]bool, admin bool) (spec gamedb.Spec, ok bool) {
	if sp.Adv == "" {
		return spec, true
	}
	if err := json.Unmarshal([]byte(sp.Adv), &spec); err != nil || (!admin && !stored[advOf(spec)]) {
		return spec, false
	}
	return spec, true
}
