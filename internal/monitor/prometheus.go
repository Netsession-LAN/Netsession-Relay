package monitor

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
)

// The Prometheus text format (0.0.4) of the report: engine, drops, alarms, VLANs, outputs and the counters per
// rule.

func b2f(b bool) float64 { return float64(b2i(b)) }

// WritePrometheus writes the report (plus per-rule counters) in the Prometheus text format 0.0.4.
func WritePrometheus(w io.Writer, r Report, rules []engineapi.RuleStat) error {
	p := &promWriter{w: w}
	p.engine(r)
	p.drops(r.Drops)
	p.alarms(r.Alarms)
	p.vlans(r.VLANList)
	p.outputs(r.Outputs)
	p.rules(rules)
	return p.err
}

// engine writes the facts and totals of the engine.
func (p *promWriter) engine(r Report) {
	p.metric("nsrelay_engine_up", "gauge", "1 if the engine answered the web part.", nil, float64(r.EngineUp))
	p.metric("nsrelay_info", "gauge", "Versions, mode and active snapshot.",
		[]string{"web_version", r.WebVersion, "engine_version", r.EngineVersion, "mode", r.Mode, "snapshot", r.Snapshot}, 1)
	p.metric("nsrelay_uptime_seconds", "gauge", "Seconds since the engine started.", nil, float64(r.UptimeS))
	p.metric("nsrelay_standby", "gauge", "1 if the dual-active guard put this engine in standby.", nil, float64(r.Standby))
	p.metric("nsrelay_sending", "gauge", "1 if the engine currently relays.", nil, float64(r.Sending))
	p.metric("nsrelay_event_active", "gauge", "1 if an event is active in the web part.", nil, float64(r.EventActive))
	p.metric("nsrelay_vlans", "gauge", "VLANs in the active snapshot.", nil, float64(r.VLANs))
	p.metric("nsrelay_rules", "gauge", "Rules in the active snapshot.", nil, float64(r.Rules))
	p.metric("nsrelay_received_frames_total", "counter", "Frames received after the kernel filter.", nil, float64(r.RXTotal))
	p.metric("nsrelay_sent_frames_total", "counter", "Frames sent (all copies).", nil, float64(r.SentTotal))
	p.metric("nsrelay_self_test_findings", "gauge", "Host self-test findings at engine start.", nil, float64(r.SelfTest))
}

// drops writes the dropped frames by reason, sorted by reason.
func (p *promWriter) drops(drops map[string]uint64) {
	reasons := make([]string, 0, len(drops))
	for k := range drops {
		reasons = append(reasons, k)
	}
	sort.Strings(reasons)
	p.head("nsrelay_dropped_frames_total", "counter", "Frames not relayed, by reason.")
	for _, k := range reasons {
		p.sample("nsrelay_dropped_frames_total", []string{"reason", k}, float64(drops[k]))
	}
}

// alarms writes the active alarms and the occurrences of every alarm kind.
func (p *promWriter) alarms(alarms map[string]AlarmSummary) {
	p.head("nsrelay_alarms_active", "gauge", "Unacknowledged alarms seen in the last 5 minutes, by kind.")
	for _, k := range Kinds {
		p.sample("nsrelay_alarms_active", []string{"kind", k}, float64(alarms[k].Active))
	}
	p.head("nsrelay_alarm_occurrences_total", "counter", "Alarm occurrences since engine start, by kind.")
	for _, k := range Kinds {
		p.sample("nsrelay_alarm_occurrences_total", []string{"kind", k}, float64(alarms[k].Count))
	}
}

// vlans writes the counters per VLAN.
func (p *promWriter) vlans(list []VLAN) {
	type vm struct {
		name, typ, help string
		val             func(VLAN) float64
	}
	for _, m := range []vm{
		{"nsrelay_vlan_received_frames_total", "counter", "Frames received per VLAN.", func(v VLAN) float64 { return float64(v.In) }},
		{"nsrelay_vlan_sent_frames_total", "counter", "Frames sent per VLAN.", func(v VLAN) float64 { return float64(v.Out) }},
		{"nsrelay_vlan_last_received_timestamp_seconds", "gauge", "Unix time of the last received frame (0 = none).", func(v VLAN) float64 { return float64(v.LastIn) }},
	} {
		p.head(m.name, m.typ, m.help)
		for _, v := range list {
			p.sample(m.name, []string{"vid", fmt.Sprint(v.VID), "name", v.Name, "group", v.Group}, m.val(v))
		}
	}
}

// outputs writes the state and counters of every monitoring target.
func (p *promWriter) outputs(outs []Output) {
	type om struct {
		name, typ, help string
		val             func(Output) float64
	}
	for _, m := range []om{
		{"nsrelay_output_up", "gauge", "1 unless the monitoring target is failing.", func(o Output) float64 { return b2f(o.Up) }},
		{"nsrelay_output_sent_total", "counter", "Events delivered to the monitoring target since the web part started.", func(o Output) float64 { return float64(o.Sent) }},
		{"nsrelay_output_dropped_total", "counter", "Events dropped for the monitoring target (queue full, too old, send failed).", func(o Output) float64 { return float64(o.Dropped) }},
		{"nsrelay_output_last_success_timestamp_seconds", "gauge", "Unix time of the last successful send (0 = never).", func(o Output) float64 { return float64(o.LastSuccess) }},
	} {
		p.head(m.name, m.typ, m.help)
		for _, o := range outs {
			p.sample(m.name, []string{"target", o.Name, "kind", o.Kind}, m.val(o))
		}
	}
}

// rules writes the counters per rule.
func (p *promWriter) rules(rules []engineapi.RuleStat) {
	p.head("nsrelay_rule_received_frames_total", "counter", "Frames matched per rule.")
	for _, x := range rules {
		p.sample("nsrelay_rule_received_frames_total", []string{"rule", x.ID}, float64(x.RX))
	}
	p.head("nsrelay_rule_relayed_frames_total", "counter", "Frames relayed per rule (before fan-out).")
	for _, x := range rules {
		p.sample("nsrelay_rule_relayed_frames_total", []string{"rule", x.ID}, float64(x.Relayed))
	}
}

type promWriter struct {
	w   io.Writer
	err error
}

func (p *promWriter) printf(format string, a ...any) {
	if p.err == nil {
		_, p.err = fmt.Fprintf(p.w, format, a...)
	}
}

func (p *promWriter) head(name, typ, help string) {
	p.printf("# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

func (p *promWriter) sample(name string, labels []string, v float64) {
	var b strings.Builder
	b.WriteString(name)
	if len(labels) > 0 {
		b.WriteByte('{')
		for i := 0; i+1 < len(labels); i += 2 {
			if i > 0 {
				b.WriteByte(',')
			}
			b.WriteString(labels[i])
			b.WriteString(`="`)
			b.WriteString(escapeLabel(labels[i+1]))
			b.WriteByte('"')
		}
		b.WriteByte('}')
	}
	p.printf("%s %v\n", b.String(), v)
}

func (p *promWriter) metric(name, typ, help string, labels []string, v float64) {
	p.head(name, typ, help)
	p.sample(name, labels, v)
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escapeLabel(s string) string { return labelEscaper.Replace(s) }
