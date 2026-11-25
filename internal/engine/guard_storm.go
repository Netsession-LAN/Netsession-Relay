package engine

import (
	"time"
)

// Step 8 of the pipeline (core.go): the storm brake pauses the loudest rule on one ingress VLAN while the engine
// sends more than storm_pps (checked before the buckets of step 9).

// stormKey names what the storm brake pauses: one rule for the frames arriving on one VLAN.
type stormKey struct {
	rule string
	vid  int
}

// StormPause is how long the storm brake pauses a rule for one ingress VLAN.
const StormPause = 60 * time.Second

// StormTick is called once per second. If the engine sent more than storm_pps for three consecutive seconds,
// the loudest pair (rule, ingress VLAN) - the one that caused most copies in the last second - is paused for
// 60 seconds: only frames of that rule arriving on that VLAN are dropped, every other VLAN keeps the game. If
// the engine stays over the limit, the next loudest pair follows three seconds later. It returns the paused
// rule ID and VLAN ID ("" and 0 when nothing was paused).
func (g *Guard) StormTick(t *Table, c *Counters, now time.Time) (string, int) {
	delta := g.sentSinceLastTick(c)
	top, topRate := g.loudestPair(t)
	g.expirePauses(now)
	if delta <= uint64(t.limits.StormPPS) {
		g.stormOverSecs = 0
		return "", 0
	}
	g.stormOverSecs++
	if g.stormOverSecs < 3 || topRate == 0 {
		return "", 0
	}
	g.stormOverSecs = 0
	g.pauseMu.Lock()
	g.paused[top] = now.Add(StormPause).UnixNano()
	g.pausedN.Store(int32(len(g.paused)))
	g.pauseMu.Unlock()
	g.Alarms.Raise(AlarmStorm, top.rule, now, "paused for 60 s for frames from VLAN %d only: %d frames/s sent in total, %d/s of them by this rule from that VLAN",
		top.vid, delta, topRate)
	return top.rule, top.vid
}

// sentSinceLastTick returns the frames the engine sent since the last StormTick.
func (g *Guard) sentSinceLastTick(c *Counters) uint64 {
	sent := c.Sent.Load()
	delta := sent - g.lastSent
	if sent < g.lastSent {
		delta = sent
	}
	g.lastSent = sent
	return delta
}

// loudestPair returns the pair (rule, ingress VLAN) that caused most copies since the last tick, and that number.
func (g *Guard) loudestPair(t *Table) (top stormKey, topRate uint64) {
	last := g.lastCopies
	g.lastCopies = make(map[stormKey]uint64, len(last))
	for _, r := range t.rules {
		for in := range r.copies {
			k := stormKey{r.ID, t.VLANs[in].VID}
			if d := g.copiesSince(last, k, r.copies[in].Load()); d > topRate {
				top, topRate = k, d
			}
		}
	}
	return top, topRate
}

// copiesSince returns the copies of pair k since the last tick (last: the counters then, v: the counter now) and
// remembers v for the next tick.
func (g *Guard) copiesSince(last map[stormKey]uint64, k stormKey, v uint64) uint64 {
	if v == 0 {
		return 0
	}
	g.lastCopies[k] = v
	if p, ok := last[k]; ok && p <= v { // counters start at 0 with every new table
		return v - p
	}
	return v
}

// Paused reports whether the storm brake currently drops frames of the rule arriving on the VLAN.
func (g *Guard) Paused(rule string, vid int, now time.Time) bool {
	if g.pausedN.Load() == 0 {
		return false
	}
	g.pauseMu.RLock()
	until := g.paused[stormKey{rule, vid}]
	g.pauseMu.RUnlock()
	return until > now.UnixNano()
}

func (g *Guard) expirePauses(now time.Time) {
	if g.pausedN.Load() == 0 {
		return
	}
	g.pauseMu.Lock()
	for k, until := range g.paused {
		if until <= now.UnixNano() {
			delete(g.paused, k)
		}
	}
	g.pausedN.Store(int32(len(g.paused)))
	g.pauseMu.Unlock()
}
