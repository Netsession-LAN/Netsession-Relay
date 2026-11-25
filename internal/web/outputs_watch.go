package web

import (
	"context"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/monitor"
	"github.com/Netsession-LAN/Netsession-Relay/internal/notify"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// The alarm watcher: alarm instances become "open" and "clear" events for the monitoring outputs.

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
	// without the engine nothing is known about the other alarms: keep them as they are (engine_down too, until
	// it opens below)
	active := make(map[store.AlarmKey]string, len(open))
	for k := range open {
		active[k] = ""
	}
	if downSince.IsZero() {
		*downSince = now
	}
	if now.Sub(*downSince) >= engineDownAfter {
		active[watchEngineDownKey] = T("Engine antwortet nicht") + ": " + engErr.Error()
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
