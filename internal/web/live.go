package web

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// The live view is the single source for everything that changes while a page is open: the templates render
// it on page load and GET /api/v1/events sends the same structure as JSON every LiveInterval. All wording is
// made here, so the JavaScript only copies text into the page. The view is built in German (the source language);
// the templates translate the texts with t, the JSON for the browser is a translated copy (localized).

// stateView is the operating state in words.
type stateView struct {
	Key      string `json:"key"`   // active | observe | paused | standby | holdoff | down | none
	Tone     string `json:"tone"`  // ok | info | warn | danger | neutral
	Icon     string `json:"icon"`  // sprite name
	Label    string `json:"label"` // short, for the header chip
	Headline string `json:"headline"`
	Text     string `json:"text"`
	// Relaying: copies are being sent right now.
	Relaying bool `json:"relaying"`
	// CanStop: an emergency stop makes sense (something is configured and it is not paused already).
	CanStop bool `json:"can_stop"`
}

func stateOf(st engineapi.Status, engineOK bool) stateView {
	switch {
	case !engineOK:
		return stateView{Key: "down", Tone: "danger", Icon: "alert", Label: "Engine weg", Headline: "Engine antwortet nicht",
			Text: "Die Weboberfläche erreicht die Engine nicht. Ob gerade weitergeleitet wird, ist unbekannt. Prüfe den Container der Engine (docker compose logs engine)."}
	case st.Mode == "":
		return stateView{Key: "none", Tone: "neutral", Icon: "info", Label: "Leer", Headline: "Noch keine Konfiguration",
			Text: "Die Engine hat noch keinen Stand bekommen. Schalte ein Spiel oder wähle unter System eine Betriebsart."}
	case st.Mode == compile.ModePaused:
		return stateView{Key: "paused", Tone: "warn", Icon: "pause", Label: "Pausiert", Headline: "Relay ist pausiert",
			Text: "Es wird nichts weitergeleitet. Spiele aus anderen Netzen werden nicht gefunden."}
	case st.Mode == compile.ModeObserve:
		return stateView{Key: "observe", Tone: "info", Icon: "eye", Label: "Beobachten", Headline: "Relay beobachtet nur", CanStop: true,
			Text: "Es zählt mit und sendet nichts. Über dieses Relay findet niemand ein Spiel."}
	case st.Standby:
		return stateView{Key: "standby", Tone: "warn", Icon: "pause", Label: "Standby", Headline: "Zweites Relay aktiv, dieses hält still", CanStop: true,
			Text: "Ein anderes Relay war zuerst da und sendet. Dieses übernimmt, sobald das andere 10 Sekunden schweigt."}
	case !st.Sending:
		return stateView{Key: "holdoff", Tone: "warn", Icon: "pause", Label: "Startet", Headline: "Relay startet", CanStop: true,
			Text: "Es prüft kurz, ob schon ein anderes Relay sendet, und legt dann los."}
	}
	return stateView{Key: "active", Tone: "ok", Icon: "check", Label: "Aktiv", Headline: "Relay leitet weiter", Relaying: true, CanStop: true,
		Text: "Spiele-Broadcasts werden zwischen den Netzen kopiert."}
}

// States of a network tile.
const (
	tileTraffic   = "traffic"
	tileQuiet     = "quiet"
	tileOff       = "off"
	tileAlarm     = "alarm"
	tileUnmanaged = "unmanaged" // switched on, but the engine does not run it (not applied yet)
)

// tileLabels are the words for the states (German source texts; an alarm shows its own name instead).
var tileLabels = map[string]string{
	tileTraffic:   "Verkehr",
	tileQuiet:     "still",
	tileOff:       "aus",
	tileUnmanaged: "nicht im Relay",
}

type vlanView struct {
	VID     int    `json:"vid"`
	Name    string `json:"name"`
	Group   string `json:"group"`
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"` // in the configuration
	Running bool   `json:"running"` // known to the engine
	Alarm   bool   `json:"alarm"`
	InRate  uint64 `json:"in_per_s"`
	In      uint64 `json:"in"`
	Out     uint64 `json:"out"`
	Status  string `json:"status"` // state of the tile (tileTraffic ...), also the data-status of the styles
	Label   string `json:"label"`  // word shown on the tile: the status in words, or the name of the alarm
	// AlarmKind is the alarm this network is part of ("" = none); the tile links to that alarm card.
	AlarmKind string `json:"alarm_kind,omitempty"`
	Interface string `json:"-"`
}

type busyRow struct {
	Rule    string `json:"rule"`
	Names   string `json:"names"`          // short: first game, "+N" for the others on the same port
	Game    string `json:"game,omitempty"` // id of the first game, for the link
	All     string `json:"all,omitempty"`  // every game on this rule
	Ports   string `json:"ports"`
	Rate    uint64 `json:"per_s"`
	RX      uint64 `json:"rx"`
	Relayed uint64 `json:"relayed"`
}

type reasonView struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count uint64 `json:"count"`
}

type liveView struct {
	TS         time.Time             `json:"ts"`
	Demo       bool                  `json:"demo,omitempty"`
	EngineOK   bool                  `json:"engine_ok"`
	State      stateView             `json:"state"`
	Mode       string                `json:"mode"`
	Hash       string                `json:"hash"`
	NeedsApply bool                  `json:"needs_apply"`
	RXRate     uint64                `json:"rx_per_s"`
	SentRate   uint64                `json:"sent_per_s"`
	RX         uint64                `json:"rx"`
	Sent       uint64                `json:"sent"`
	RuleCount  int                   `json:"rule_count"`
	VLANCount  int                   `json:"vlan_count"`
	GamesOn    int                   `json:"games_on"`
	GamesTotal int                   `json:"games_total"`
	Enabled    []string              `json:"enabled"` // ids of games switched on
	Alarms     []alarmGroup          `json:"alarms"`  // open
	Seen       []alarmGroup          `json:"seen"`    // acknowledged, still occurring
	VLANs      []vlanView            `json:"vlans"`
	Rules      []engineapi.RuleStat  `json:"rules"`
	Busy       []busyRow             `json:"busy"`
	Unknown    []engineapi.PortCount `json:"unknown"`
	Reasons    []reasonView          `json:"reasons"`
	Engine     engineapi.Status      `json:"-"`
	ruleRX     map[string]uint64     // for the hub: counters of this sample
	games      map[string]store.GameRow
}

// localized returns the view with all texts in the given language, for the JSON the browser gets. The German
// view is returned as it is. Keys the page works with stay untouched (state.key, vlans.status);
// the alarm detail comes from the engine and is not translated.
func (v *liveView) localized(lang string) *liveView {
	if lang == langDE || lang == "" {
		return v
	}
	T := trFor(lang)
	c := *v
	c.State.Label, c.State.Headline, c.State.Text = T(v.State.Label), T(v.State.Headline), T(v.State.Text)
	c.Alarms, c.Seen = localizedAlarms(T, v.Alarms), localizedAlarms(T, v.Seen)
	c.VLANs = localizedTiles(T, v.VLANs)
	c.Reasons = localizedReasons(T, v.Reasons)
	return &c
}

// localizedTiles translates the tile labels; a label that is an alarm kind (no short name) stays as it is.
func localizedTiles(T translator, in []vlanView) []vlanView {
	out := slices.Clone(in)
	for i := range out {
		if out[i].Label != out[i].AlarmKind {
			out[i].Label = T(out[i].Label)
		}
	}
	return out
}

// localizedReasons translates the reason labels; a reason without a label shows its key, which stays as it is.
func localizedReasons(T translator, in []reasonView) []reasonView {
	out := slices.Clone(in)
	for i := range out {
		if out[i].Label != out[i].Key {
			out[i].Label = T(out[i].Label)
		}
	}
	return out
}

var reasonLabels = map[string]string{
	"relayed":       "Weitergeleitet",
	"parse":         "Kein lesbares UDP-Paket",
	"own_mac":       "Eigene Kopie (ignoriert)",
	"blocked":       "Absender gesperrt",
	"foreign_relay": "Kopie eines fremden Relays",
	"src_subnet":    "Absender passt nicht zum Netz",
	"not_broadcast": "Kein Broadcast",
	"unknown_port":  "Port ohne Spiel",
	"no_target":     "Kein Ziel-Netz",
	"size":          "Paket zu groß",
	"ttl":           "TTL abgelaufen",
	"mode":          "Nicht gesendet (Betriebsart)",
	"loop":          "Schleifenschutz",
	"rate":          "Limit erreicht",
	"auto_paused":   "Regel pausiert (Sturm-Bremse)",
	"src_filter":    "Absender nicht freigegeben",
	"send_error":    "Senden fehlgeschlagen",
	"panic":         "Interner Fehler der Engine",
}

// liveNow builds the live view from the engine status and the database.
func (s *Server) liveNow(ctx context.Context) *liveView {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	st, err := s.Engine.Status(ctx)
	v := &liveView{TS: time.Now(), Demo: s.Demo, EngineOK: err == nil, Engine: st, Mode: st.Mode, Hash: st.SnapshotHash,
		RXRate: st.RXRate, SentRate: st.SentRate, RX: st.RX, Sent: st.Sent, RuleCount: st.Rules, VLANCount: st.VLANs,
		Enabled: []string{}, Alarms: []alarmGroup{}, VLANs: []vlanView{}, Busy: []busyRow{},
		Rules: st.RuleStats, Unknown: st.TopUnknown, Reasons: []reasonView{},
		ruleRX: map[string]uint64{}, games: map[string]store.GameRow{}}
	v.State = stateOf(st, v.EngineOK)
	if v.Rules == nil {
		v.Rules = []engineapi.RuleStat{}
	}
	if v.Unknown == nil {
		v.Unknown = []engineapi.PortCount{}
	}

	// This runs every few seconds for the live stream. Without the games the counters still show; the pages that
	// list games report a database that cannot be read.
	games, _ := s.Store.Games(ctx)
	v.addGames(games)
	// the networks of the configuration first; without a readable configuration only what the engine runs
	cfg, _ := s.Store.ExportConfig(ctx)
	v.addVLANs(cfg.VLANs, st)
	v.addAlarms(groupAlarms(st.Alarms, st.Mode))

	// no readable revision: no "apply needed" hint, nothing else depends on it
	if revs, _ := s.Store.Revisions(ctx, 1); len(revs) > 0 && v.EngineOK {
		v.NeedsApply = liveEngineBehind(revs[0], st.SnapshotHash)
	}

	v.addBusy(st.RuleStats, s.rules(ctx, st.SnapshotHash), s.hub.rates())
	v.addReasons(st.Reasons)
	return v
}

// liveEngineBehind reports whether the engine runs another snapshot than the last revision, when that revision was
// applied successfully. An engine without a snapshot is not behind: it has not got one yet.
func liveEngineBehind(last store.Revision, engineHash string) bool {
	return engineHash != "" && last.AppliedOK && last.Hash != engineHash
}

// addGames counts the games and remembers them for the names of the busy rules.
func (v *liveView) addGames(games []store.GameRow) {
	for _, g := range games {
		v.games[g.ID] = g
		if g.RelayNeeded != "no" { // games without LAN broadcast cannot be switched
			v.GamesTotal++
		}
		if g.Enabled {
			v.GamesOn++
			v.Enabled = append(v.Enabled, g.ID)
		}
	}
}

// addVLANs builds the network tiles: the configured networks first (so switched-off tables stay visible) with
// the engine counters merged in, then networks only the engine knows.
func (v *liveView) addVLANs(configured []store.VLANConfig, st engineapi.Status) {
	alarmOf := tileAlarmKinds(st.Alarms, v.TS)
	stats := map[int]engineapi.VLANStat{}
	for _, x := range st.VLANStats {
		stats[x.VID] = x
	}
	seen := map[int]bool{}
	add := func(vid int, name, group string, enabled bool) {
		x, running := stats[vid]
		seen[vid] = true
		v.VLANs = append(v.VLANs, v.tile(vid, name, group, enabled, running, x, alarmOf[strconv.Itoa(vid)]))
	}
	for _, x := range configured {
		add(x.VID, x.Name, x.Group, x.Enabled)
	}
	for _, x := range st.VLANStats {
		if !seen[x.VID] {
			add(x.VID, x.Name, x.Group, true)
		}
	}
}

// tileAlarmKinds maps alarm keys to the alarm that marks their tile: an open alarm, or an acknowledged one
// that is still going on. An open one wins over an acknowledged one.
func tileAlarmKinds(alarms []engineapi.Alarm, now time.Time) map[string]string {
	alarmOf := map[string]string{}
	for _, a := range alarms { // acknowledged first, so that open ones overwrite them
		if !a.AckedAt.IsZero() && !alarmOver(a.Last, now) {
			alarmOf[a.Key] = a.Kind
		}
	}
	for _, a := range alarms {
		if a.AckedAt.IsZero() {
			alarmOf[a.Key] = a.Kind
		}
	}
	return alarmOf
}

// tile is one network with its state in words.
func (v *liveView) tile(vid int, name, group string, enabled, running bool, x engineapi.VLANStat, alarmKind string) vlanView {
	t := vlanView{VID: vid, Name: name, Group: group, Key: tableKey(name, group), Enabled: enabled, Running: running,
		InRate: x.InRate, In: x.In, Out: x.Out, Interface: x.Interface, AlarmKind: alarmKind}
	t.Alarm = t.AlarmKind != ""
	switch {
	case !enabled:
		t.Status = tileOff
	case t.Alarm:
		t.Status = tileAlarm
	case !running && v.EngineOK:
		t.Status = tileUnmanaged
	case t.InRate > 0:
		t.Status = tileTraffic
	default:
		t.Status = tileQuiet
	}
	t.Label = tileLabels[t.Status]
	if t.Status == tileAlarm {
		t.Label = cmp.Or(alarmShort[t.AlarmKind], alarmTitles[t.AlarmKind], t.AlarmKind)
	} else {
		t.AlarmKind = ""
	}
	return t
}

// busyRows is the number of games with traffic the overview lists.
const busyRows = 6

// addBusy lists the games with traffic: rules that have received something, busiest first (addGames runs first).
func (v *liveView) addBusy(stats []engineapi.RuleStat, idx *ruleIndex, rates map[string]uint64) {
	for _, rs := range stats {
		v.ruleRX[rs.ID] = rs.RX
		if rs.RX == 0 {
			continue
		}
		row := busyRow{Rule: rs.ID, Names: rs.ID, RX: rs.RX, Relayed: rs.Relayed, Rate: rates[rs.ID]}
		if r, ok := idx.byRule[rs.ID]; ok {
			row.Ports = r.Ports
			row.nameGames(gameRefs(r.Specs, v.games))
		}
		v.Busy = append(v.Busy, row)
	}
	sort.SliceStable(v.Busy, func(i, j int) bool { return busyBefore(v.Busy[i], v.Busy[j]) })
	if len(v.Busy) > busyRows {
		v.Busy = v.Busy[:busyRows]
	}
}

// nameGames names the row after the games of its rule: the first one with "+N" for the others, and all of them
// for the title. Without games the row keeps the rule id.
func (row *busyRow) nameGames(refs []gameRef) {
	if len(refs) == 0 {
		return
	}
	row.Game, row.Names = refs[0].ID, shortName(refs[0].Name)
	if n := len(refs) - 1; n > 0 {
		row.Names += fmt.Sprintf(" +%d", n)
	}
	all := make([]string, len(refs))
	for i, g := range refs {
		all[i] = g.Name
	}
	row.All = strings.Join(all, " · ")
}

// busyBefore orders the busy games: highest rate first, then most received.
func busyBefore(a, b busyRow) bool {
	if a.Rate != b.Rate {
		return a.Rate > b.Rate
	}
	return a.RX > b.RX
}

// addReasons lists why frames were not relayed, most frequent first.
func (v *liveView) addReasons(reasons map[string]uint64) {
	for k, n := range reasons {
		if k == "relayed" {
			continue
		}
		v.Reasons = append(v.Reasons, reasonView{Key: k, Label: cmp.Or(reasonLabels[k], k), Count: n})
	}
	sort.Slice(v.Reasons, func(i, j int) bool {
		if v.Reasons[i].Count != v.Reasons[j].Count {
			return v.Reasons[i].Count > v.Reasons[j].Count
		}
		return v.Reasons[i].Key < v.Reasons[j].Key
	})
}
