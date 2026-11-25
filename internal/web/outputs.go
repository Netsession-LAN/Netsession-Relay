package web

import (
	"strconv"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/monitor"
	"github.com/Netsession-LAN/Netsession-Relay/internal/notify"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// The web part feeds the monitoring outputs (internal/outputs): audit entries and alarms become notify.Events in
// the language of the server, the outputs service pushes them to every target.

// engineDownAfter: the engine must not answer for this long before engine_down opens.
const engineDownAfter = 30 * time.Second

// outT translates the texts sent to the monitoring targets: there is no reader with a language choice, so the
// server default (NSRELAY_LANG) decides.
func (s *Server) outT() translator { return trFor(s.DefaultLang) }

// auditToOutputs is the store's audit hook. Publish never blocks.
func (s *Server) auditToOutputs(e store.AuditEntry) {
	v := auditOf(e, s.outT())
	text := v.Text
	if e.User != "" {
		text = e.User + ": " + text
	}
	if v.Object != "" {
		text += " " + v.Object
	}
	sev := notify.Notice
	if v.Tone == "danger" || v.Tone == "warn" {
		sev = notify.Warn
	}
	s.Outputs.Publish(notify.Event{Type: notify.TypeAudit, Severity: sev, Action: e.Action, User: e.User, Object: e.Object, Text: text})
}

// Alarm kinds the web part raises itself, next to the ones of the engine.
const (
	alarmVLANSilent = "vlan_silent"
	alarmEngineDown = "engine_down"
)

// alarmTitle covers the engine alarms and the ones the web part raises itself.
func alarmTitle(kind string) string {
	switch kind {
	case alarmVLANSilent:
		return "Netz schweigt"
	case alarmEngineDown:
		return "Engine antwortet nicht"
	}
	if t := alarmTitles[kind]; t != "" {
		return t
	}
	return kind
}

func vlanOf(key string) int {
	if n, err := strconv.Atoi(key); err == nil && n >= 1 && n <= 4094 {
		return n
	}
	return 0
}

// alarmOpenText is the text of an alarm that opens: the title with the engine's detail, or the key when there
// is no detail.
func alarmOpenText(T translator, k store.AlarmKey, detail string) string {
	title := T(alarmTitle(k.Kind))
	switch {
	case k.Kind == alarmVLANSilent:
		return T("Netz schweigt: VLAN %s bekommt seit %d Minuten keine Broadcasts", k.Key, int(monitor.SilentAfter.Minutes()))
	case detail != "":
		return title + ": " + detail
	case k.Key != "":
		return title + " (" + k.Key + ")"
	}
	return title
}

// alarmClearText is the text of an alarm that ends.
func alarmClearText(T translator, k store.AlarmKey) string {
	why := T("seit 5 Minuten ruhig oder als gesehen markiert")
	if k.Kind == alarmEngineDown {
		why = T("Engine antwortet wieder")
	}
	text := T(alarmTitle(k.Kind))
	if k.Key != "" && k.Kind != alarmEngineDown {
		text += " (" + k.Key + ")"
	}
	return T("%s beendet: %s", text, why)
}
