package web

import (
	"cmp"
	"context"
	"slices"
	"sort"
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
