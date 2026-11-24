package web

import (
	"context"
	"strconv"
	"time"

	"github.com/Netsession2024/Netsession-Relay/internal/monitor"
	"github.com/Netsession2024/Netsession-Relay/internal/notify"
	"github.com/Netsession2024/Netsession-Relay/internal/store"
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
	active := map[store.AlarmKey]string{} // instance → text
	engineKey := store.AlarmKey{Kind: alarmEngineDown, Key: "engine"}
	if engErr != nil {
		if downSince.IsZero() {
			*downSince = now
		}
		if now.Sub(*downSince) >= engineDownAfter {
			active[engineKey] = T("Engine antwortet nicht") + ": " + engErr.Error()
		} else if _, ok := open[engineKey]; ok {
			active[engineKey] = ""
		}
		// without the engine nothing is known about the other alarms: keep them as they are
		for k := range open {
			if k != engineKey {
				active[k] = ""
			}
		}
	} else {
		*downSince = time.Time{}
		rep := monitor.Build(st, nil, s.Version, evErr == nil, now)
		detail := map[store.AlarmKey]string{}
		for _, a := range st.Alarms {
			detail[store.AlarmKey{Kind: a.Kind, Key: a.Key}] = a.Detail
		}
		for kind, sum := range rep.Alarms {
			for _, key := range sum.Keys {
				k := store.AlarmKey{Kind: kind, Key: key}
				text := T(alarmTitle(kind))
				if kind == alarmVLANSilent {
					text = T("Netz schweigt: VLAN %s bekommt seit %d Minuten keine Broadcasts", key, int(monitor.SilentAfter.Minutes()))
				} else if d := detail[k]; d != "" {
					text += ": " + d
				} else if key != "" {
					text += " (" + key + ")"
				}
				active[k] = text
			}
		}
	}
	// An alarm is published only after its new state is stored; if storing fails, the next round tries again
	// and nothing is reported twice.
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
	for k := range open {
		if _, still := active[k]; still {
			continue
		}
		if err := s.Store.SetAlarmOpen(ctx, k, false); err != nil {
			s.Log.Error("alarm watcher: alarm not stored as cleared, retrying", "kind", k.Kind, "key", k.Key, "err", err)
			continue
		}
		why := T("seit 5 Minuten ruhig oder als gesehen markiert")
		if k.Kind == alarmEngineDown {
			why = T("Engine antwortet wieder")
		}
		text := T(alarmTitle(k.Kind))
		if k.Key != "" && k.Kind != alarmEngineDown {
			text += " (" + k.Key + ")"
		}
		s.Outputs.Publish(notify.Event{Time: now, Type: notify.TypeAlarm, State: "clear", Severity: notify.Notice,
			Kind: k.Kind, Key: k.Key, VLAN: vlanOf(k.Key), Text: T("%s beendet: %s", text, why)})
	}
}
