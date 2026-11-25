package web

import (
	"cmp"
	"slices"
	"strconv"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// The VLAN tiles of the live view: traffic, quiet, off, alarm or unmanaged, with the alarms that colour them.

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
	t.Status = v.tileStatus(&t)
	t.Label = tileLabels[t.Status]
	if t.Status == tileAlarm {
		t.Label = cmp.Or(alarmShort[t.AlarmKind], alarmTitles[t.AlarmKind], t.AlarmKind)
	} else {
		t.AlarmKind = ""
	}
	return t
}

// tileStatus is the state a network tile shows, the first that applies: off, alarm, not run by the engine,
// traffic, quiet.
func (v *liveView) tileStatus(t *vlanView) string {
	switch {
	case !t.Enabled:
		return tileOff
	case t.Alarm:
		return tileAlarm
	case !t.Running && v.EngineOK:
		return tileUnmanaged
	case t.InRate > 0:
		return tileTraffic
	}
	return tileQuiet
}
