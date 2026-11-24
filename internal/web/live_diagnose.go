package web

import (
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// ---- diagnosis: why is a game not found? ----

type diagStep struct {
	Tone  string // ok | warn | danger | neutral
	Icon  string
	Title string
	Text  string
}

func step(tone, title, text string) diagStep {
	icon := map[string]string{"ok": "check", "warn": "alert", "danger": "alert", "neutral": "dash"}[tone]
	return diagStep{Tone: tone, Icon: icon, Title: title, Text: text}
}

// diagnose walks the chain a discovery packet takes, from the switch in the UI to the copy on the wire, in
// German. Pages use diagnoseT with the translator of the request.
func diagnose(g store.GameRow, rules []ruleView, v *liveView) (steps []diagStep, verdict diagStep) {
	return diagnoseT(trFor(langDE), g, rules, v)
}

// diagnoseT is diagnose with the texts in the language of T; v is the German live view (not a localized copy).
// The verdict is the first step that is a problem, or "not the relay" when there is none.
func diagnoseT(T translator, g store.GameRow, rules []ruleView, v *liveView) (steps []diagStep, verdict diagStep) {
	d := diagWalk{T: T, bad: -1}
	switch g.RelayNeeded {
	case "no":
		return nil, d.step("neutral", "Nichts zu relayen", "Dieses Spiel sucht nicht per Broadcast. Das Relay kann hier nicht helfen. Lies die Hinweise unten.")
	case "unknown": // a note, not the verdict
		d.steps = append(d.steps, d.step("warn", "Ports nicht belegt", "Welche Ports das Spiel für die LAN-Suche nutzt, ist nicht sicher. Schau unter Übersicht in „Unbekannte Ports“, während jemand den LAN-Browser öffnet."))
	}
	c := diagCountsOf(rules, v.EngineOK)
	d.add(d.switchStep(g.Enabled))
	d.add(d.ruleStep(g.Enabled, rules))
	d.add(d.stateStep(v.State))
	d.add(d.requestStep(c))
	d.add(d.copyStep(c, v.State.Relaying))
	if d.bad >= 0 {
		return d.steps, d.steps[d.bad]
	}
	return d.steps, d.step("ok", "Am Relay liegt es nicht", "Suchanfragen kommen an und werden kopiert. Findet trotzdem niemand das Spiel, liegt es am Spielverkehr danach (siehe unten) oder am PC.")
}

// diagWalk collects the steps of a diagnosis and remembers the first one that is a problem.
type diagWalk struct {
	T     translator
	steps []diagStep
	bad   int // index of the first warn or danger step added, -1 if none
}

// step is a translated step; args fill the verbs of text.
func (d *diagWalk) step(tone, title, text string, args ...any) diagStep {
	return step(tone, d.T(title), d.T(text, args...))
}

func (d *diagWalk) add(s diagStep) {
	d.steps = append(d.steps, s)
	if d.bad < 0 && (s.Tone == "danger" || s.Tone == "warn") {
		d.bad = len(d.steps) - 1
	}
}

// diagCounts is what the engine counted on the rules of a game.
type diagCounts struct {
	checkable   bool // the engine answers and the game has rules
	rx, relayed uint64
}

func diagCountsOf(rules []ruleView, engineOK bool) diagCounts {
	c := diagCounts{checkable: engineOK && len(rules) > 0}
	for _, r := range rules {
		c.rx += r.RX
		c.relayed += r.Relayed
	}
	return c
}

// switchStep: is the game switched on?
func (d *diagWalk) switchStep(enabled bool) diagStep {
	if enabled {
		return d.step("ok", "Spiel ist an", "Im aktiven Event eingeschaltet.")
	}
	return d.step("danger", "Spiel ist aus", "Schalte es oben ein. Das wirkt sofort.")
}

// ruleStep: does the game end up in a rule of the relay?
func (d *diagWalk) ruleStep(enabled bool, rules []ruleView) diagStep {
	switch {
	case !enabled:
		return d.step("neutral", "Regel im Relay", "Entsteht, sobald das Spiel an ist.")
	case len(rules) == 0:
		return d.step("danger", "Keine Regel im Relay", "Das Spiel ist an, aber es entsteht keine Regel. Es hat keine Broadcast-Ports hinterlegt.")
	}
	ports := make([]string, len(rules))
	for i, r := range rules {
		ports[i] = r.Ports
	}
	return d.step("ok", "Regel im Relay", "UDP %s wird zwischen den Netzen kopiert.", strings.Join(ports, ", "))
}

// stateStep: does the relay forward at all? A relay that is starting is a warning, anything else that does not
// forward a problem.
func (d *diagWalk) stateStep(st stateView) diagStep {
	switch {
	case st.Relaying:
		return d.step("ok", "Relay leitet weiter", st.Text)
	case st.Key == "holdoff":
		return d.step("warn", st.Headline, st.Text)
	}
	return d.step("danger", st.Headline, st.Text)
}

// requestStep: do search requests of the game arrive?
func (d *diagWalk) requestStep(c diagCounts) diagStep {
	switch {
	case !c.checkable:
		return d.step("neutral", "Suchanfragen kommen an", "Lässt sich gerade nicht prüfen.")
	case c.rx == 0:
		return d.step("warn", "Noch keine Suchanfrage gesehen", "Seit dem Start der Engine kam auf diesen Ports nichts an. Öffnet jemand im Spiel den LAN-Browser? Sonst: zweite Netzwerkkarte oder VPN am PC abschalten, Firewall prüfen.")
	}
	return d.step("ok", "Suchanfragen kommen an", "%s Pakete seit dem Start der Engine.", formatNum(c.rx))
}

// copyStep: do copies go out to the other networks?
func (d *diagWalk) copyStep(c diagCounts, relaying bool) diagStep {
	switch {
	case !c.checkable || c.rx == 0:
		return d.step("neutral", "Kopien gehen raus", "Erst prüfbar, wenn Suchanfragen ankommen.")
	case c.relayed == 0 && relaying:
		return d.step("danger", "Es geht keine Kopie raus", "Pakete kommen an, werden aber nicht kopiert. Prüfe unter Netz, ob die Ziel-Netze an sind, und unter System die verworfenen Pakete.")
	case c.relayed == 0:
		return d.step("neutral", "Kopien gehen raus", "Keine, weil das Relay gerade nicht weiterleitet.")
	}
	return d.step("ok", "Kopien gehen raus", "%s Pakete in andere Netze kopiert.", formatNum(c.relayed))
}
