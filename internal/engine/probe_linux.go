//go:build linux

package engine

import (
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

// probe replays the learned server queries from the relay address of every VLAN (probe.go). Not in pause
// (emergency stop) or standby, and only when the snapshot switches it on.
func (e *Engine) probe(st *state) {
	t := st.table
	if !e.probing(t) {
		return
	}
	now := time.Now()
	groups := e.seedProbes(t, now)
	qs := e.guard.Probe.Queries(now)
	var buf []byte
	sent := 0
	for i := range t.VLANs {
		v := &t.VLANs[i]
		if !canProbeFrom(v) {
			continue
		}
		for _, q := range qs {
			if groups[q.Rule][v.Group] {
				buf, sent = e.sendProbe(st.out[i], v, q, buf, sent)
			}
		}
	}
	if sent > 0 {
		e.Log.Debug("server probe sent", "queries", len(qs), "frames", sent)
	}
}

// probing reports whether the server probe runs: switched on by the snapshot, in active or observe mode, and
// while the engine sends.
func (e *Engine) probing(t *Table) bool {
	modeOK := t.Mode == compile.ModeActive || t.Mode == compile.ModeObserve
	return t.Snapshot.Probe && e.guard.Sending(time.Now()) && modeOK
}

// seedProbes seeds the prober with the ports of every probing rule and returns, per rule, the VLAN groups in its
// flows: probes stay inside the matrix.
func (e *Engine) seedProbes(t *Table, now time.Time) map[string]map[string]bool {
	groups := map[string]map[string]bool{}
	for _, r := range t.rules {
		if !r.Probe {
			continue
		}
		g := map[string]bool{}
		for _, f := range r.Flows {
			g[f.Src], g[f.Dst] = true, true
		}
		groups[r.ID] = g
		if ps, err := parsePorts(r.Ports); err == nil {
			e.guard.Probe.Seed(r.ID, ps, now)
		}
	}
	return groups
}

// canProbeFrom reports whether VLAN v has what a probe is sent from: a relay IP and an interface MAC.
func canProbeFrom(v *VLANRuntime) bool {
	return v.RelayIP.IsValid() && v.MAC != (pkt.MAC{})
}

// sendProbe sends query q out of VLAN v; it returns the (reused) buffer and the number of probes sent so far,
// which also numbers the probes.
func (e *Engine) sendProbe(out *packetSock, v *VLANRuntime, q ProbeQuery, buf []byte, sent int) ([]byte, int) {
	b, err := BuildProbe(buf, v, q, uint16(sent))
	if err != nil {
		return buf, sent
	}
	if err := out.write(b); err != nil {
		e.Log.Debug("probe", "vlan", v.VID, "err", err)
		return b, sent
	}
	return b, sent + 1
}
