package otlp

import (
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/monitor"
	"github.com/Netsession-LAN/Netsession-Relay/internal/notify"
)

// Metric is one OTLP metric with integer data points.
type Metric struct {
	Name, Unit, Desc string
	Sum              bool      // monotonic cumulative counter; otherwise a gauge
	Start            time.Time // counters: start of the counting (engine start)
	Time             time.Time
	Points           []Point
}

// Point is one data point.
type Point struct {
	Attrs []KV
	Value int64
}

// KV is one attribute; Value is a string or an int.
type KV struct {
	Key   string
	Value any
}

// MetricNames is the stable list of exported metric names (a test keeps it in sync with FromReport).
var MetricNames = []string{
	"nsrelay.engine.up", "nsrelay.uptime", "nsrelay.info", "nsrelay.mode", "nsrelay.standby", "nsrelay.sending",
	"nsrelay.event.active", "nsrelay.vlans", "nsrelay.rules", "nsrelay.selftest.findings", "nsrelay.rx", "nsrelay.sent",
	"nsrelay.drops", "nsrelay.alarms.active", "nsrelay.alarms.acked", "nsrelay.alarm.occurrences", "nsrelay.vlan.in",
	"nsrelay.vlan.out", "nsrelay.vlan.silent.duration", "nsrelay.vlan.silent", "nsrelay.rule.rx", "nsrelay.rule.relayed",
	"nsrelay.output.up", "nsrelay.output.sent", "nsrelay.output.dropped",
}

// ModeNumber maps the mode to the number used by OTLP and SNMP: 0 unknown, 1 active, 2 observe, 3 paused.
func ModeNumber(mode string) int64 {
	switch mode {
	case "active":
		return 1
	case "observe":
		return 2
	case "paused":
		return 3
	}
	return 0
}

// FromReport converts the monitoring report into OTLP metrics. Counters start at the engine start, so an
// engine restart shows as a counter reset. Without the engine only engine.up, event.active and the outputs go
// out, never frozen counters.
func FromReport(rep monitor.Report, rules []engineapi.RuleStat, outputs []notify.Status, now, webStart time.Time, withRules bool) []Metric {
	b := metricSet{now: now}
	b.gauge("nsrelay.engine.up", "1", "1 if the engine answers the web part.", one(int64(rep.EngineUp)))
	b.gauge("nsrelay.event.active", "1", "1 if an event is active.", one(int64(rep.EventActive)))
	if rep.EngineUp == 1 {
		start := now.Add(-time.Duration(rep.UptimeS) * time.Second)
		b.engine(rep, start)
		b.alarms(rep, start)
		b.vlans(rep, start)
		if withRules {
			b.rules(rules, start)
		}
	}
	b.outputs(outputs, webStart)
	return b.ms
}

// metricSet collects the metrics of one export, all taken at now.
type metricSet struct {
	now time.Time
	ms  []Metric
}

// gauge adds a gauge.
func (b *metricSet) gauge(name, unit, desc string, pts []Point) {
	b.ms = append(b.ms, Metric{Name: name, Unit: unit, Desc: desc, Time: b.now, Points: pts})
}

// counter adds a monotonic counter that counts since start.
func (b *metricSet) counter(name, unit, desc string, start time.Time, pts []Point) {
	b.ms = append(b.ms, Metric{Name: name, Unit: unit, Desc: desc, Sum: true, Start: start, Time: b.now, Points: pts})
}

// one is a single data point.
func one(v int64, attrs ...KV) []Point { return []Point{{Attrs: attrs, Value: v}} }

// engine adds the state and the totals of the engine.
func (b *metricSet) engine(rep monitor.Report, start time.Time) {
	b.gauge("nsrelay.uptime", "s", "Seconds since the engine started.", one(rep.UptimeS))
	b.gauge("nsrelay.info", "1", "Mode, snapshot and engine version as attributes.", one(1,
		KV{"nsrelay.mode", rep.Mode}, KV{"nsrelay.snapshot", rep.Snapshot}, KV{"nsrelay.engine.version", rep.EngineVersion}))
	b.gauge("nsrelay.mode", "1", "0 unknown, 1 active, 2 observe, 3 paused.", one(ModeNumber(rep.Mode)))
	b.gauge("nsrelay.standby", "1", "1 if a second relay put this one in standby.", one(int64(rep.Standby)))
	b.gauge("nsrelay.sending", "1", "1 if the engine currently relays.", one(int64(rep.Sending)))
	b.gauge("nsrelay.vlans", "{vlan}", "VLANs in the active snapshot.", one(int64(rep.VLANs)))
	b.gauge("nsrelay.rules", "{rule}", "Rules in the active snapshot.", one(int64(rep.Rules)))
	b.gauge("nsrelay.selftest.findings", "{finding}", "Host self-test findings at engine start.", one(int64(rep.SelfTest)))
	b.counter("nsrelay.rx", "{frame}", "Frames received after the kernel filter.", start, one(int64(rep.RXTotal)))
	b.counter("nsrelay.sent", "{frame}", "Frames sent (all copies).", start, one(int64(rep.SentTotal)))
	var drops []Point
	for _, r := range monitor.DropReasons {
		drops = append(drops, Point{Attrs: []KV{{"reason", r}}, Value: int64(rep.Drops[r])})
	}
	b.counter("nsrelay.drops", "{frame}", "Frames not relayed, by reason.", start, drops)
}

// alarms adds the alarm summaries by kind.
func (b *metricSet) alarms(rep monitor.Report, start time.Time) {
	var act, acked, occ []Point
	for _, k := range monitor.Kinds {
		a := rep.Alarms[k]
		act = append(act, Point{Attrs: []KV{{"kind", k}}, Value: int64(a.Active)})
		acked = append(acked, Point{Attrs: []KV{{"kind", k}}, Value: int64(a.Acked)})
		occ = append(occ, Point{Attrs: []KV{{"kind", k}}, Value: int64(a.Count)})
	}
	b.gauge("nsrelay.alarms.active", "{alarm}", "Unacknowledged alarms seen in the last 5 minutes, by kind.", act)
	b.gauge("nsrelay.alarms.acked", "{alarm}", "Acknowledged alarms still known, by kind.", acked)
	b.counter("nsrelay.alarm.occurrences", "{occurrence}", "Alarm occurrences since engine start, by kind.", start, occ)
}

// vlans adds the counters and the silence per VLAN.
func (b *metricSet) vlans(rep monitor.Report, start time.Time) {
	var in, out, silentS, silent []Point
	for _, v := range rep.VLANList {
		attrs := []KV{{"vlan", int64(v.VID)}, {"vlan.name", v.Name}, {"vlan.group", v.Group}}
		in = append(in, Point{Attrs: attrs, Value: int64(v.In)})
		out = append(out, Point{Attrs: attrs, Value: int64(v.Out)})
		silentS = append(silentS, Point{Attrs: attrs, Value: v.SilentS})
		silent = append(silent, Point{Attrs: attrs, Value: int64(v.Silent)})
	}
	b.counter("nsrelay.vlan.in", "{frame}", "Frames received per VLAN.", start, in)
	b.counter("nsrelay.vlan.out", "{frame}", "Frames sent per VLAN.", start, out)
	b.gauge("nsrelay.vlan.silent.duration", "s", "Seconds without received frames per VLAN.", silentS)
	b.gauge("nsrelay.vlan.silent", "1", "1 if the VLAN counts as silent (vlan_silent).", silent)
}

// rules adds the counters per rule.
func (b *metricSet) rules(rules []engineapi.RuleStat, start time.Time) {
	var rx, rel []Point
	for _, r := range rules {
		rx = append(rx, Point{Attrs: []KV{{"rule", r.ID}}, Value: int64(r.RX)})
		rel = append(rel, Point{Attrs: []KV{{"rule", r.ID}}, Value: int64(r.Relayed)})
	}
	b.counter("nsrelay.rule.rx", "{frame}", "Frames matched per rule.", start, rx)
	b.counter("nsrelay.rule.relayed", "{frame}", "Frames relayed per rule.", start, rel)
}

// outputs adds the state of the enabled monitoring targets; their counters run since the web part started.
func (b *metricSet) outputs(outputs []notify.Status, webStart time.Time) {
	var up, sent, dropped []Point
	for _, o := range outputs {
		if o.State == "off" {
			continue
		}
		attrs := []KV{{"target", o.Name}, {"kind", o.Kind}}
		v := int64(1)
		if o.Failing() {
			v = 0
		}
		up = append(up, Point{Attrs: attrs, Value: v})
		sent = append(sent, Point{Attrs: attrs, Value: int64(o.Sent)})
		dropped = append(dropped, Point{Attrs: attrs, Value: int64(o.Dropped)})
	}
	if len(up) == 0 {
		return
	}
	b.gauge("nsrelay.output.up", "1", "1 unless the monitoring target is failing.", up)
	b.counter("nsrelay.output.sent", "{event}", "Events delivered per monitoring target.", webStart, sent)
	b.counter("nsrelay.output.dropped", "{event}", "Events dropped per monitoring target.", webStart, dropped)
}
