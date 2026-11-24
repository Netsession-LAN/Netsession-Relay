package web

import (
	"context"
	"strconv"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
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

// WatchAlarms turns alarm instances (kind, key) into "open" and "clear" events until ctx ends. The open ones are
// kept in the database, so a restart repeats nothing and loses no "clear".
func (s *Server) WatchAlarms(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	var downSince time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.watchOnce(ctx, &downSince, time.Now())
	}
}

// watchOnce runs one round of the alarm watcher: it works out which alarm instances are active now and publishes
// the difference to the ones stored as open.
func (s *Server) watchOnce(ctx context.Context, downSince *time.Time, now time.Time) {
	open, err := s.Store.OpenAlarms(ctx)
	if err != nil {
		s.Log.Error("alarm watcher: open alarms not readable, round skipped", "err", err)
		return
	}
	T := s.outT()
	rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	st, engErr := s.Engine.Status(rctx)
	cancel()
	_, evErr := s.Store.ActiveEvent(ctx)
	var active map[store.AlarmKey]string // instance → text
	if engErr != nil {
		active = watchEngineDownAlarms(T, open, engErr, downSince, now)
	} else {
		*downSince = time.Time{}
		active = watchEngineAlarms(T, monitor.Build(st, nil, s.Version, evErr == nil, now), st.Alarms)
	}
	// An alarm is published only after its new state is stored; if storing fails, the next round tries again
	// and nothing is reported twice.
	s.publishOpenedAlarms(ctx, T, open, active, now)
	s.publishClearedAlarms(ctx, T, open, active, now)
}

// watchEngineDownKey is the alarm instance of an engine that does not answer.
var watchEngineDownKey = store.AlarmKey{Kind: alarmEngineDown, Key: "engine"}

// watchEngineDownAlarms gives the active alarms of a round in which the engine did not answer. engine_down opens once
// the engine has been silent for engineDownAfter; an empty text keeps an alarm open without publishing anything.
func watchEngineDownAlarms(T translator, open map[store.AlarmKey]string, engErr error, downSince *time.Time, now time.Time) map[store.AlarmKey]string {
	active := map[store.AlarmKey]string{}
	if downSince.IsZero() {
		*downSince = now
	}
	if now.Sub(*downSince) >= engineDownAfter {
		active[watchEngineDownKey] = T("Engine antwortet nicht") + ": " + engErr.Error()
	} else if _, ok := open[watchEngineDownKey]; ok {
		active[watchEngineDownKey] = ""
	}
	// without the engine nothing is known about the other alarms: keep them as they are
	for k := range open {
		if k != watchEngineDownKey {
			active[k] = ""
		}
	}
	return active
}

// watchEngineAlarms gives the active alarms of the monitoring report with the text each one is published with.
func watchEngineAlarms(T translator, rep monitor.Report, alarms []engineapi.Alarm) map[store.AlarmKey]string {
	detail := map[store.AlarmKey]string{}
	for _, a := range alarms {
		detail[store.AlarmKey{Kind: a.Kind, Key: a.Key}] = a.Detail
	}
	active := map[store.AlarmKey]string{}
	for kind, sum := range rep.Alarms {
		for _, key := range sum.Keys {
			k := store.AlarmKey{Kind: kind, Key: key}
			active[k] = alarmOpenText(T, k, detail[k])
		}
	}
	return active
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

// publishOpenedAlarms stores and publishes the active alarms that are not open yet.
func (s *Server) publishOpenedAlarms(ctx context.Context, T translator, open map[store.AlarmKey]string, active map[store.AlarmKey]string, now time.Time) {
	for k, text := range active {
		if _, ok := open[k]; ok {
			continue
		}
		if err := s.Store.SetAlarmOpen(ctx, k, true); err != nil {
			s.Log.Error("alarm watcher: alarm not stored as open, retrying", "kind", k.Kind, "key", k.Key, "err", err)
			continue
		}
		s.Outputs.Publish(notify.Event{Time: now, Type: notify.TypeAlarm, State: "open", Severity: notify.Warn,
			Kind: k.Kind, Key: k.Key, VLAN: vlanOf(k.Key), Text: text})
	}
}

// publishClearedAlarms stores and publishes the open alarms that are no longer active.
func (s *Server) publishClearedAlarms(ctx context.Context, T translator, open map[store.AlarmKey]string, active map[store.AlarmKey]string, now time.Time) {
	for k := range open {
		if _, still := active[k]; still {
			continue
		}
		if err := s.Store.SetAlarmOpen(ctx, k, false); err != nil {
			s.Log.Error("alarm watcher: alarm not stored as cleared, retrying", "kind", k.Kind, "key", k.Key, "err", err)
			continue
		}
		s.Outputs.Publish(notify.Event{Time: now, Type: notify.TypeAlarm, State: "clear", Severity: notify.Notice,
			Kind: k.Kind, Key: k.Key, VLAN: vlanOf(k.Key), Text: alarmClearText(T, k)})
	}
}
