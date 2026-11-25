// Package demo is a fake engine for working on the web UI without a relay host (nsrelay web -demo): it accepts
// snapshots like the real engine and invents traffic, players, alarms and port finder results. It never touches
// the network.
package demo

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
)

// Engine implements what the web part needs from an engine (web.Engine) with invented, changing data: busy and
// silent tables, counters per rule, unknown ports and alarms that come and go.
type Engine struct {
	mu      sync.Mutex
	started time.Time
	last    time.Time
	snap    *compile.Snapshot
	rx      uint64
	sent    uint64
	vlanIn  map[int]float64
	vlanOut map[int]float64
	ruleRX  map[string]float64
	ruleTX  map[string]float64
	reasons map[string]float64
	unknown map[uint16]float64
	acked   map[string]string // alarm kind -> who acknowledged
	finder  finderSession
}

// NewEngine returns a demo engine without a snapshot.
func NewEngine() *Engine {
	now := time.Now()
	return &Engine{started: now.Add(-3*time.Hour - 12*time.Minute), last: now, vlanIn: map[int]float64{}, vlanOut: map[int]float64{},
		ruleRX: map[string]float64{}, ruleTX: map[string]float64{}, reasons: map[string]float64{}, unknown: map[uint16]float64{}, acked: map[string]string{}}
}

// Validate accepts every snapshot.
func (d *Engine) Validate(_ context.Context, s *compile.Snapshot) (engineapi.Result, error) {
	return engineapi.Result{OK: true, Hash: s.Hash}, nil
}

// Apply stores the snapshot; mode, VLANs and rules of the status follow it.
func (d *Engine) Apply(_ context.Context, s *compile.Snapshot) (engineapi.Result, error) {
	d.mu.Lock()
	d.snap = s
	d.mu.Unlock()
	return engineapi.Result{OK: true, Hash: s.Hash}, nil
}

// Ack marks a demo alarm kind as seen (until it stops and comes back).
func (d *Engine) Ack(_ context.Context, kind, by string) error {
	d.mu.Lock()
	d.acked[kind] = by
	d.mu.Unlock()
	return nil
}

// wave is a slow, repeatable pseudo-random value in 0..1 for a slot (VLAN, rule) at a time.
func wave(slot int, t float64) float64 {
	v := math.Sin(t/17+float64(slot)*1.7)*0.5 + math.Sin(t/5.3+float64(slot)*0.9)*0.3 + math.Sin(t/1.9+float64(slot)*2.3)*0.2
	return (v + 1) / 2
}

// tick is one simulation step: the current time, seconds since the demo start and since the last step, and
// whether the applied snapshot relays.
type tick struct {
	now     time.Time
	t, dt   float64
	sending bool
}

// Status advances the simulation to now and reports it.
func (d *Engine) Status(context.Context) (engineapi.Status, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	k := tick{now: now, dt: now.Sub(d.last).Seconds(), t: now.Sub(d.started).Seconds()}
	d.last = now
	st := engineapi.Status{Version: "demo", Started: d.started, Reasons: map[string]uint64{}, BeaconAuth: "file", IfacePolicy: "auto"}
	if d.snap == nil {
		return st, nil
	}
	s := d.snap
	st.Mode, st.SnapshotHash, st.VLANs, st.Rules = s.Mode, s.Hash, len(s.VLANs), len(s.Rules)
	k.sending = s.Mode == compile.ModeActive
	st.Sending = k.sending

	rxRate, sentRate := d.simulateRules(&st, s, k)
	noise := 6 + 10*wave(99, k.t) // broadcasts without a rule
	d.simulateVLANs(&st, s, k, rxRate, noise)
	d.rx += uint64((rxRate + noise) * k.dt)
	d.sent += uint64(sentRate * k.dt)
	st.RX, st.Sent = d.rx, d.sent
	st.RXRate, st.SentRate = uint64(rxRate+noise+0.5), uint64(sentRate+0.5)
	d.countDrops(&st, k, rxRate, noise)
	d.countUnknownPorts(&st, k, noise)
	st.Alarms = demoAlarms(s, k)
	d.markAcked(st.Alarms, now)
	return st, nil
}

// ruleRate is the invented packet rate of the i-th rule: FlatOut is the busiest, the first two rules follow,
// every third rule sees a little traffic.
func ruleRate(i int, r compile.Rule, t float64) float64 {
	flatout := slices.ContainsFunc(r.Specs, func(id string) bool { return strings.HasPrefix(id, "flatout") })
	switch {
	case flatout:
		return 12 + 40*wave(i, t)
	case i < 2:
		return 4 + 20*wave(i, t)
	case i%3 == 0:
		if rate := 9 * wave(i, t) * wave(i+40, t); rate >= 1.5 {
			return rate
		}
	}
	return 0
}

// simulateRules advances the counters per rule and returns the total received and sent rate.
func (d *Engine) simulateRules(st *engineapi.Status, s *compile.Snapshot, k tick) (rxRate, sentRate float64) {
	for i, r := range s.Rules {
		rate := ruleRate(i, r, k.t)
		d.ruleRX[r.ID] += rate * k.dt
		rxRate += rate
		if k.sending {
			copies := rate * float64(max(len(s.VLANs)-1, 1))
			d.ruleTX[r.ID] += copies * k.dt
			sentRate += copies
		}
		st.RuleStats = append(st.RuleStats, engineapi.RuleStat{ID: r.ID, RX: uint64(d.ruleRX[r.ID]), Relayed: uint64(d.ruleTX[r.ID])})
	}
	return rxRate, sentRate
}

// vlanShares is the relative loudness per table: some loud, some silent now and then, every seventh (starting
// with the fifth) silent all the time. It also returns the sum.
func vlanShares(vlans []compile.VLAN, t float64) ([]float64, float64) {
	var sum float64
	rates := make([]float64, len(vlans))
	for i, v := range vlans {
		w := wave(v.VID, t)
		if i%7 == 4 || w < 0.18 {
			continue
		}
		rates[i] = 40 * w * w
		sum += rates[i]
	}
	return rates, sum
}

// simulateVLANs spreads the received traffic (rules plus noise) over the tables and advances their counters.
func (d *Engine) simulateVLANs(st *engineapi.Status, s *compile.Snapshot, k tick, rxRate, noise float64) {
	rates, sum := vlanShares(s.VLANs, k.t)
	for i, v := range s.VLANs {
		if sum > 0 {
			rates[i] = rates[i] / sum * (rxRate + noise)
		}
		d.vlanIn[v.VID] += rates[i] * k.dt
		if k.sending {
			d.vlanOut[v.VID] += (rxRate - rates[i]*rxRate/(rxRate+noise)) * k.dt
		}
		st.VLANStats = append(st.VLANStats, engineapi.VLANStat{VID: v.VID, Name: v.Name, Group: v.Group, Interface: fmt.Sprintf("%s.%d", v.Parent, v.VID),
			In: uint64(d.vlanIn[v.VID]), Out: uint64(d.vlanOut[v.VID]), InRate: uint64(rates[i] + 0.5), LastIn: time.Now()})
	}
}

// countDrops advances the drop reasons and reports those with at least one packet.
func (d *Engine) countDrops(st *engineapi.Status, k tick, rxRate, noise float64) {
	d.reasons["unknown_port"] += noise * 0.7 * k.dt
	d.reasons["not_broadcast"] += noise * 0.3 * k.dt
	if !k.sending {
		d.reasons["mode"] += rxRate * k.dt
	} else {
		d.reasons["rate"] += 0.05 * rxRate * k.dt
	}
	for r, v := range d.reasons {
		if v >= 1 {
			st.Reasons[r] = uint64(v)
		}
	}
}

// countUnknownPorts advances a few well-known ports without a rule and reports those seen.
func (d *Engine) countUnknownPorts(st *engineapi.Status, k tick, noise float64) {
	for i, p := range []uint16{25601, 17500, 54915, 3702} {
		d.unknown[p] += noise * k.dt / float64(2+i*3)
		if n := uint32(d.unknown[p]); n > 0 {
			st.TopUnknown = append(st.TopUnknown, engineapi.PortCount{Port: p, Count: n})
		}
	}
}
