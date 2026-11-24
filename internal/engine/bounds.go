package engine

import (
	"fmt"

	"github.com/Netsession2024/Netsession-Relay/internal/compile"
	"github.com/Netsession2024/Netsession-Relay/internal/gamedb"
)

// Hard bounds of the engine. The snapshot comes from the web part; whatever it says, the protections stay on:
// a limit of 0 (which used to mean "off") becomes the default, absurd values are cut to the bound. So a
// compromised or buggy web part cannot switch off rate limits, loop detection or the storm brake.
const (
	minVLANPPS, maxVLANPPS     = 1, 5000
	minVLANBurst, maxVLANBurst = 1, 10000
	minStormPPS, maxStormPPS   = 100, 50000
	minDedupMS, maxDedupMS     = 20, 5000
	maxRulePPS, maxRuleBurst   = 1000, 2000
)

// bound returns v inside [lo, hi]; 0 or less becomes def. Changes are noted in t.Clamped.
func (t *Table) bound(what string, v, def, lo, hi int) int {
	out := v
	switch {
	case v <= 0:
		out = def
	case v < lo:
		out = lo
	case v > hi:
		out = hi
	}
	if out != v {
		t.Clamped = append(t.Clamped, fmt.Sprintf("%s %d -> %d", what, v, out))
	}
	return out
}

func (t *Table) boundLimits(l compile.Limits) compile.Limits {
	d := compile.DefaultLimits
	return compile.Limits{
		VLANPPS:   t.bound("limits.vlan_pps", l.VLANPPS, d.VLANPPS, minVLANPPS, maxVLANPPS),
		VLANBurst: t.bound("limits.vlan_burst", l.VLANBurst, d.VLANBurst, minVLANBurst, maxVLANBurst),
		StormPPS:  t.bound("limits.storm_pps", l.StormPPS, d.StormPPS, minStormPPS, maxStormPPS),
		DedupMS:   t.bound("limits.dedup_ms", l.DedupMS, d.DedupMS, minDedupMS, maxDedupMS),
	}
}

// boundVLAN bounds the per-VLAN overrides; 0 stays 0 (use the global limit).
func (t *Table) boundVLAN(v compile.VLAN) (pps, burst int) {
	pps, burst = v.RatePPS, v.Burst
	if pps != 0 {
		pps = t.bound(fmt.Sprintf("vlan %d rate_pps", v.VID), pps, 0, minVLANPPS, maxVLANPPS)
	}
	if burst != 0 {
		burst = t.bound(fmt.Sprintf("vlan %d burst", v.VID), burst, 0, minVLANBurst, maxVLANBurst)
	}
	return pps, burst
}

func (t *Table) boundRule(r compile.Rule) compile.Rule {
	r.RatePPS = t.bound("rule "+r.ID+" rate_pps", r.RatePPS, gamedb.DefaultRatePPS, 1, maxRulePPS)
	r.RateBurst = t.bound("rule "+r.ID+" rate_burst", r.RateBurst, gamedb.DefaultRateBurst, 1, maxRuleBurst)
	r.PayloadMax = t.bound("rule "+r.ID+" payload_max", r.PayloadMax, gamedb.DefaultPayloadMax, 1, gamedb.DefaultPayloadMax)
	return r
}
