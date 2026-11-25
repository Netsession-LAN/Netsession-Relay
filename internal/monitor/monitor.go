package monitor

import (
	"fmt"
	"sort"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
)

// Kinds are the alarm kinds always present in the report, so Zabbix items and triggers exist before the first incident.
// New kinds go to the end: the SNMP alarm table is indexed by position (snmp/mib.go).
var Kinds = []string{"foreign_relay", "loop", "storm", "dual_active", "vlan_silent", "unknown_beacon"}

// DropReasons are always present in Report.Drops (0 until the engine reports them), see engine.Reason. New
// reasons go to the end: the SNMP drop table is indexed by position (snmp/mib.go).
var DropReasons = []string{"parse", "own_mac", "blocked", "foreign_relay", "src_subnet", "not_broadcast", "unknown_port",
	"no_target", "size", "ttl", "mode", "loop", "rate", "auto_paused", "src_filter", "send_error", "panic"}

// ActiveWindow is how long an unacknowledged alarm counts as active after it was last seen.
const ActiveWindow = 5 * time.Minute

// SilentAfter is how long a VLAN may stay without any received broadcast during an active event in mode
// "active" before it raises vlan_silent.
const SilentAfter = 10 * time.Minute

// Output is one monitoring target in the Prometheus export.
type Output struct {
	Name, Kind  string
	Up          bool
	Sent        uint64
	Dropped     uint64
	LastSuccess int64 // unix seconds, 0 = never
}

// Report is the JSON answer of GET /api/v1/monitoring. Flat numbers for Zabbix dependent items (JSONPath),
// vlan_list for low-level discovery. Booleans are 0/1 so Zabbix can store them as numbers.
type Report struct {
	Time          int64                   `json:"time"`
	WebVersion    string                  `json:"web_version"`
	EngineUp      int                     `json:"engine_up"`
	EngineError   string                  `json:"engine_error,omitempty"`
	EngineVersion string                  `json:"engine_version"`
	UptimeS       int64                   `json:"uptime_s"`
	Mode          string                  `json:"mode"`
	Standby       int                     `json:"standby"`
	Sending       int                     `json:"sending"`
	EventActive   int                     `json:"event_active"`
	Snapshot      string                  `json:"snapshot"`
	VLANs         int                     `json:"vlans"`
	Rules         int                     `json:"rules"`
	RXTotal       uint64                  `json:"rx_total"`
	SentTotal     uint64                  `json:"sent_total"`
	RXPerS        uint64                  `json:"rx_per_s"`
	SentPerS      uint64                  `json:"sent_per_s"`
	Drops         map[string]uint64       `json:"drops"`
	AlarmsActive  int                     `json:"alarms_active"`
	Alarms        map[string]AlarmSummary `json:"alarms"`
	SelfTest      int                     `json:"self_test_findings"`
	VLANList      []VLAN                  `json:"vlan_list"`
	// monitoring targets of the web role (syslog, OTLP, SNMP traps): enabled ones, failing ones, events dropped
	OutputsTotal   int      `json:"outputs_total"`
	OutputsFailing int      `json:"outputs_failing"`
	OutputsDropped uint64   `json:"outputs_dropped"`
	Outputs        []Output `json:"-"` // per target, for Prometheus only
}

// AlarmSummary aggregates all alarms of one kind.
type AlarmSummary struct {
	Active int      `json:"active"` // unacknowledged and seen within ActiveWindow
	Acked  int      `json:"acked"`  // acknowledged ("Gesehen") but still known
	Count  uint64   `json:"count"`  // occurrences since engine start
	Last   int64    `json:"last"`   // unix seconds, 0 = never
	Keys   []string `json:"keys"`   // affected rules/VLANs of the active ones
}

// VLAN is one entry for Zabbix low-level discovery ({#VID}, {#NAME}, {#GROUP}).
type VLAN struct {
	VID       int    `json:"vid"`
	Name      string `json:"name"`
	Group     string `json:"group"`
	Interface string `json:"interface"`
	In        uint64 `json:"in"`
	Out       uint64 `json:"out"`
	InPerS    uint64 `json:"in_per_s"`
	LastIn    int64  `json:"last_in"`  // unix seconds, 0 = nothing since engine start
	SilentS   int64  `json:"silent_s"` // seconds without received frames (since engine start if never)
	Silent    int    `json:"silent"`   // 1 = counts for vlan_silent
}

// Build turns an engine status into a report. engineErr != nil means the engine did not answer.
func Build(st engineapi.Status, engineErr error, webVersion string, eventActive bool, now time.Time) Report {
	r := newReport(webVersion, eventActive, now)
	if engineErr != nil {
		r.EngineError = engineErr.Error()
		return r
	}
	r.setEngine(st, now)
	for _, a := range st.Alarms {
		r.Alarms[a.Kind] = addAlarm(r.Alarms[a.Kind], a, now)
	}
	r.addVLANs(st, eventActive && st.Mode == "active" && !st.Standby, now)
	r.sumAlarms()
	return r
}

// newReport is the report before the engine's part: every alarm kind is listed, also without alarms.
func newReport(webVersion string, eventActive bool, now time.Time) Report {
	r := Report{Time: now.Unix(), WebVersion: webVersion, Drops: map[string]uint64{}, Alarms: map[string]AlarmSummary{},
		VLANList: []VLAN{}, EventActive: b2i(eventActive)}
	for _, k := range Kinds {
		r.Alarms[k] = AlarmSummary{Keys: []string{}}
	}
	return r
}

// sumAlarms sorts the keys of every alarm kind and counts the active alarms.
func (r *Report) sumAlarms() {
	for _, s := range r.Alarms {
		sort.Strings(s.Keys)
		r.AlarmsActive += s.Active
	}
}

// setEngine copies the facts and counters of an engine that answered; every known drop reason is reported,
// "relayed" is not a drop.
func (r *Report) setEngine(st engineapi.Status, now time.Time) {
	for _, k := range DropReasons {
		r.Drops[k] = 0
	}
	r.EngineUp = 1
	r.EngineVersion, r.Mode, r.Snapshot = st.Version, st.Mode, st.SnapshotHash
	if !st.Started.IsZero() {
		r.UptimeS = int64(now.Sub(st.Started) / time.Second)
	}
	r.Standby, r.Sending = b2i(st.Standby), b2i(st.Sending)
	r.VLANs, r.Rules = st.VLANs, st.Rules
	r.RXTotal, r.SentTotal, r.RXPerS, r.SentPerS = st.RX, st.Sent, st.RXRate, st.SentRate
	for k, v := range st.Reasons {
		if k != "relayed" {
			r.Drops[k] = v
		}
	}
	r.SelfTest = len(st.SelfTest)
}

// addAlarm counts one engine alarm into the summary of its kind: acknowledged ones as acked, unacknowledged ones
// seen within ActiveWindow as active with their key.
func addAlarm(s AlarmSummary, a engineapi.Alarm, now time.Time) AlarmSummary {
	if s.Keys == nil {
		s.Keys = []string{}
	}
	s.Count += a.Count
	if u := a.Last.Unix(); !a.Last.IsZero() && u > s.Last {
		s.Last = u
	}
	switch {
	case !a.AckedAt.IsZero():
		s.Acked++
	case now.Sub(a.Last) <= ActiveWindow:
		s.Active++
		s.Keys = append(s.Keys, a.Key)
	}
	return s
}

// addVLANs lists the VLANs; while watch is set (event active, relay active and not in standby) a VLAN without
// received frames for SilentAfter counts as an active vlan_silent alarm.
func (r *Report) addVLANs(st engineapi.Status, watch bool, now time.Time) {
	silent := r.Alarms["vlan_silent"]
	for _, v := range st.VLANStats {
		x, quiet := vlanReport(v, st.Started, now)
		if watch && quiet {
			x.Silent = 1
			silent.Active++
			silent.Keys = append(silent.Keys, fmt.Sprint(v.VID))
		}
		r.VLANList = append(r.VLANList, x)
	}
	r.Alarms["vlan_silent"] = silent
}

// vlanReport converts the counters of one VLAN and tells whether it has been silent for SilentAfter, counted
// from its last received frame or, without one, from the engine start.
func vlanReport(v engineapi.VLANStat, started, now time.Time) (VLAN, bool) {
	x := VLAN{VID: v.VID, Name: v.Name, Group: v.Group, Interface: v.Interface, In: v.In, Out: v.Out, InPerS: v.InRate}
	since := started
	if !v.LastIn.IsZero() {
		x.LastIn, since = v.LastIn.Unix(), v.LastIn
	}
	if since.IsZero() {
		return x, false
	}
	x.SilentS = int64(now.Sub(since) / time.Second)
	return x, now.Sub(since) >= SilentAfter
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
