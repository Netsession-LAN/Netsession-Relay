//go:build linux

package engine

import (
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/hostprep"
)

// everySecond runs the storm brake, the dual-active timeout, beacons, alarm logging and bucket GC. n counts the
// calls, that is the seconds since the start.
func (e *Engine) everySecond(n int) {
	now := time.Now()
	st := e.st.Load()
	if st == nil {
		return
	}
	e.sampleRates(st)
	if id, vid := e.guard.StormTick(st.table, &e.Counters, now); id != "" {
		e.Log.Error("storm brake: rule auto-paused for 60 s for frames from one VLAN", "rule", id, "vlan", vid)
	}
	if e.guard.PeerTick(now) {
		e.Log.Warn("dual-active guard: earlier relay silent, leaving standby")
	}
	e.sendBeacons()
	if n%int(ProbeInterval/time.Second) == 5 {
		e.probe(st)
	}
	e.logAlarms(now)
	e.minuteTasks(n, now)
}

// minuteTasks runs what is due once a minute: the host preparation at second 30, garbage collection at second 0.
func (e *Engine) minuteTasks(n int, now time.Time) {
	switch n % 60 {
	case 30:
		e.refreshHost()
	case 0:
		e.guard.GC(now)
		e.guard.Players.GC(now)
		e.guard.Probe.GC(now)
	}
}

// logAlarms logs new alarms, and alarms that went on, at most once a minute each.
func (e *Engine) logAlarms(now time.Time) {
	for _, a := range e.guard.Alarms.List() {
		k := a.Kind + "|" + a.Key
		if last, ok := e.alarmLog[k]; ok && !alarmDue(a.Last, last, now) {
			continue
		}
		e.Log.Warn("alarm", "kind", a.Kind, "key", a.Key, "count", a.Count, "detail", a.Detail)
		e.alarmLog[k] = now
	}
}

// alarmDue reports whether an alarm last seen at seen and last logged at logged is to be logged again.
func alarmDue(seen, logged, now time.Time) bool {
	return seen.After(logged) && now.Sub(logged) >= time.Minute
}

// refreshHost repeats the host preparation once a minute (hostprep.Refresh) and logs what is new.
func (e *Engine) refreshHost() {
	e.mu.Lock() // not while a snapshot is being applied
	defer e.mu.Unlock()
	st := e.st.Load()
	if st == nil {
		return
	}
	next := hostprep.Refresh(st.table.Snapshot)
	known := e.knownHostIssues()
	for _, is := range next {
		if !known[is.Code] {
			e.Log.Warn("host preparation incomplete (optional by hand: deploy/host/setup.sh)", "what", is.Code, "detail", is.Detail)
		}
	}
	e.hostIssues.Store(&next)
}

// knownHostIssues returns the codes of the host preparation issues reported so far.
func (e *Engine) knownHostIssues() map[string]bool {
	known := map[string]bool{}
	if cur := e.hostIssues.Load(); cur != nil {
		for _, x := range *cur {
			known[x.Code] = true
		}
	}
	return known
}

func (e *Engine) sampleRates(st *state) {
	now := time.Now()
	cur := rateSample{rx: e.Counters.RX.Load(), sent: e.Counters.Sent.Load(), vlanIn: map[int]uint64{}, vlanRate: map[int]uint64{},
		lastIn: map[int]time.Time{}}
	prev := e.prevRates
	if prev.vlanIn != nil {
		cur.rxRate, cur.sentRate = cur.rx-prev.rx, cur.sent-prev.sent
	}
	for _, v := range st.table.VLANs {
		cur.addVLAN(&prev, v.VID, e.Counters.VLANIn[v.VID].Load(), now)
	}
	e.prevRates = cur
	e.rates.Store(&cur)
}

// addVLAN records the in counter of VLAN vid, its rate since prev and when it last grew.
func (cur *rateSample) addVLAN(prev *rateSample, vid int, in uint64, now time.Time) {
	cur.vlanIn[vid] = in
	if t, ok := prev.lastIn[vid]; ok {
		cur.lastIn[vid] = t
	}
	if p, ok := prev.vlanIn[vid]; ok {
		cur.vlanRate[vid] = in - p
		if in > p {
			cur.lastIn[vid] = now
		}
	} else if in > 0 {
		cur.lastIn[vid] = now
	}
}

// sendBeacons announces this engine on every VLAN (dual-active guard).
func (e *Engine) sendBeacons() {
	st := e.st.Load()
	if st == nil {
		return
	}
	now := time.Now()
	for i, v := range st.table.VLANs {
		if err := st.out[i].write(e.guard.BeaconFrame(v.MAC, st.table.Snapshot, now)); err != nil {
			e.Log.Debug("beacon", "vlan", v.VID, "err", err)
		}
	}
}

func (e *Engine) logStats() {
	st := e.st.Load()
	if st == nil {
		return
	}
	attrs := []any{"rx", e.Counters.RX.Load(), "sent", e.Counters.Sent.Load(), "standby", e.guard.Standby()}
	for k, v := range e.Counters.ReasonCounts() {
		attrs = append(attrs, k, v)
	}
	e.Log.Info("stats", attrs...)
}
