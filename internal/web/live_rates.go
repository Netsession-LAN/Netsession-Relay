package web

import (
	"time"
)

// The per-rule rates of the live stream: frames per second from two consecutive polls of the hub.

func (h *liveHub) rates() map[string]uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if time.Since(h.rateAt) > 10*time.Second {
		return nil
	}
	return h.rate
}

// sample stores the per-rule rates derived from two consecutive polls. A poll without the engine is skipped.
func (h *liveHub) sample(v *liveView) {
	if !v.EngineOK {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := v.TS
	if dt := now.Sub(h.prevAt).Seconds(); h.prevRX != nil && rateInterval(dt) {
		h.rate, h.rateAt = ruleRates(h.prevRX, v.ruleRX, dt), now
	}
	h.prevRX, h.prevAt = v.ruleRX, now
}

// rateInterval reports whether two polls dt seconds apart give a meaningful rate: not the same instant, and not
// so far apart (a stalled hub) that the rate would be an average over minutes.
func rateInterval(dt float64) bool { return dt > 0.01 && dt < 30 }

// ruleRates is the frames per second of every rule whose counter grew between two polls dt seconds apart.
func ruleRates(prev, cur map[string]uint64, dt float64) map[string]uint64 {
	rate := map[string]uint64{}
	for id, rx := range cur {
		if p, ok := prev[id]; ok && rx > p {
			rate[id] = uint64(float64(rx-p)/dt + 0.5)
		}
	}
	return rate
}
