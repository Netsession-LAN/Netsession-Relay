package web

import (
	"cmp"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
)

// Alarms on the live view: the engine's alarm instances grouped by kind, sorted into open and acknowledged
// ones, with the networks they concern, and the "Gesehen" action.

// alarmGroup aggregates alarms of one kind (e.g. a foreign relay seen in 16 VLANs) into one entry.
type alarmGroup struct {
	Kind   string     `json:"kind"`
	Title  string     `json:"title"`
	Hint   string     `json:"hint,omitempty"`
	Keys   []string   `json:"-"`
	Nets   []alarmNet `json:"nets"`  // affected networks (keys that are VLAN ids)
	Where  string     `json:"where"` // the keys that are not networks (rules), "" if there are none
	Count  uint64     `json:"count"`
	Last   time.Time  `json:"last"`
	Detail string     `json:"detail"`
	Acked  bool       `json:"acked"`
	By     string     `json:"acked_by,omitempty"`
	Quiet  bool       `json:"quiet"` // nothing new for alarmQuietAfter: over, but not yet acknowledged
}

// alarmQuietAfter is how long an alarm has to stay without new events to count as over. An acknowledged
// alarm that is over disappears from the overview, and only an alarm that is open or still going on marks
// its network tile.
const alarmQuietAfter = 5 * time.Minute

// alarmOver reports whether an alarm has had no new events for alarmQuietAfter.
func alarmOver(last, now time.Time) bool { return now.Sub(last) > alarmQuietAfter }

// alarmNet is one network an alarm concerns; the alarm card links to its tile.
type alarmNet struct {
	VID  int    `json:"vid"`
	Name string `json:"name"`
	Key  string `json:"key"`
}

// alarmShort is the alarm name as it fits on a network tile.
var alarmShort = map[string]string{
	"foreign_relay":  "Fremdes Relay",
	"loop":           "Schleife",
	"storm":          "Sturm-Bremse",
	"dual_active":    "Zweites Relay",
	"unknown_beacon": "Relay-Signal",
}

// AlarmNets is the number of networks the open alarms concern (for the key figure on the overview).
func (v *liveView) AlarmNets() int { return alarmNetCount(v.Alarms) }

var alarmTitles = map[string]string{
	"foreign_relay":  "Fremdes Relay aktiv",
	"loop":           "Schleife erkannt",
	"storm":          "Sturm-Bremse hat eine Regel pausiert",
	"dual_active":    "Zweites Relay entdeckt",
	"unknown_beacon": "Unbekanntes Relay-Signal",
}

var alarmHints = map[string]string{
	"loop":           "Pakete kreisen. Die betroffene Regel ist gebremst. Suche ein zweites Relay oder eine Brücke zwischen den Netzen.",
	"storm":          "Zu viele Pakete auf einem Port. Die Regel pausiert nur für das Netz, aus dem sie kommen, und läuft von selbst wieder an.",
	"dual_active":    "Zwei Relays für dieselben Netze. Eins von beiden muss aus.",
	"unknown_beacon": "Ein Relay ohne denselben Schlüssel oder ein gefälschtes Signal. Dieses Relay beachtet es nicht und bleibt aktiv. Gehört das andere Relay dazu, gib beiden denselben NSRELAY_BEACON_KEY.",
}

// groupAlarms aggregates the engine's alarms by kind, the most recent group first; mode words the hint of a
// foreign relay.
func groupAlarms(alarms []engineapi.Alarm, mode string) []alarmGroup {
	byKind := map[string]*alarmGroup{}
	var order []string
	for _, a := range alarms {
		g := byKind[a.Kind]
		if g == nil {
			g = newAlarmGroup(a.Kind, mode)
			byKind[a.Kind] = g
			order = append(order, a.Kind)
		}
		g.add(a)
	}
	out := make([]alarmGroup, 0, len(order))
	for _, k := range order {
		g := byKind[k]
		sort.Strings(g.Keys)
		g.Where = strings.Join(g.Keys, " · ")
		g.Nets = []alarmNet{}
		out = append(out, *g)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	return out
}

// localizedAlarms translates titles and hints of alarm groups; a kind without a title shows its key, which stays
// as it is. nil stays nil (null in the JSON, not an empty list).
func localizedAlarms(T translator, in []alarmGroup) []alarmGroup {
	if in == nil {
		return nil
	}
	out := slices.Clone(in)
	for i := range out {
		if out[i].Title != out[i].Kind { // an alarm kind without a title shows its key
			out[i].Title = T(out[i].Title)
		}
		out[i].Hint = T(out[i].Hint)
	}
	return out
}

// newAlarmGroup is the empty group of an alarm kind with its title and hint; a kind without a title shows its
// name.
func newAlarmGroup(kind, mode string) *alarmGroup {
	g := &alarmGroup{Kind: kind, Title: cmp.Or(alarmTitles[kind], kind), Hint: alarmHints[kind]}
	if kind == "foreign_relay" {
		g.Hint = foreignRelayHint(mode)
	}
	return g
}

// foreignRelayHint explains a foreign relay: expected while this relay only observes, a problem otherwise.
func foreignRelayHint(mode string) string {
	if mode == compile.ModeObserve {
		return "Erwartet, solange ein anderes Relay parallel läuft."
	}
	return "Ein anderes Gerät leitet dieselben Broadcasts weiter. Läuft es weiter, darf dieses Relay nicht aktiv sein."
}

// add merges one alarm instance into its group. The group counts as acknowledged only while every instance is;
// By names the first one who acknowledged. Last and Detail come from the most recent instance.
func (g *alarmGroup) add(a engineapi.Alarm) {
	g.addAck(a)
	g.Keys = append(g.Keys, a.Key)
	g.Count += a.Count
	if a.Last.After(g.Last) || g.Detail == "" {
		g.Last, g.Detail = a.Last, a.Detail
	}
}

// addAck merges the acknowledgement of one instance (before its key is added): one open alarm keeps the whole
// group open, and By stays the first one who acknowledged.
func (g *alarmGroup) addAck(a engineapi.Alarm) {
	if len(g.Keys) == 0 {
		g.Acked, g.By = true, a.AckedBy
	}
	if a.AckedAt.IsZero() {
		g.Acked = false
	} else if g.By == "" {
		g.By = a.AckedBy
	}
}

// addAlarms sorts the alarm groups into open and seen ones. Keys that are networks become links to the tiles
// (addVLANs runs first), the rest stays as text.
func (v *liveView) addAlarms(groups []alarmGroup) {
	for _, g := range groups {
		nets, rest := v.alarmNets(g.Keys)
		g.Nets = append(g.Nets, nets...)
		sort.Slice(g.Nets, func(i, j int) bool { return g.Nets[i].VID < g.Nets[j].VID })
		g.Where = strings.Join(rest, " · ")
		g.Quiet = alarmOver(g.Last, v.TS)
		if g.Acked {
			if g.Quiet {
				continue // acknowledged and over: nothing left to show
			}
			v.Seen = append(v.Seen, g)
		} else {
			v.Alarms = append(v.Alarms, g)
		}
	}
}

// alarmNets splits the keys of an alarm into the networks that have a tile and the rest (rules, unknown VLANs).
func (v *liveView) alarmNets(keys []string) (nets []alarmNet, rest []string) {
	for _, k := range keys {
		vid, err := strconv.Atoi(k)
		i := slices.IndexFunc(v.VLANs, func(x vlanView) bool { return err == nil && x.VID == vid })
		if i < 0 {
			rest = append(rest, k)
			continue
		}
		nets = append(nets, alarmNet{VID: vid, Name: v.VLANs[i].Name, Key: v.VLANs[i].Key})
	}
	return nets, rest
}

// alarmNetCount counts the different networks a list of alarms concerns.
func alarmNetCount(list []alarmGroup) int {
	seen := map[int]bool{}
	for _, g := range list {
		for _, n := range g.Nets {
			seen[n.VID] = true
		}
	}
	return len(seen)
}

// ackAlarm acknowledges all alarms of a kind ("Gesehen").
func (s *Server) ackAlarm(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	T := s.T(r)
	if _, known := alarmTitles[kind]; !known {
		s.answer(w, r, "/", applyAnswer{Error: T("Diesen Alarm gibt es nicht.")})
		return
	}
	if err := s.Engine.Ack(r.Context(), kind, s.actor(r)); err != nil {
		s.Log.Error("alarm not acknowledged: engine not reachable", "kind", kind, "err", err)
		s.answer(w, r, "/", applyAnswer{Error: T("Die Engine antwortet nicht. Der Alarm ist nicht als gesehen markiert.")})
		return
	}
	s.audit(r.Context(), s.actor(r), "alarm.ack", kind, "", "")
	s.answer(w, r, "/", applyAnswer{OK: true, Message: T("%s: als gesehen markiert.", T(alarmTitles[kind]))})
}
