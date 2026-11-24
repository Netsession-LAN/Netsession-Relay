//go:build linux

package engine

import (
	"fmt"
	"io"
	"net/netip"
	"sort"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/netif"
)

// Players lists recent discovery senders (engine API).
func (e *Engine) Players() []engineapi.Player {
	list := e.guard.Players.List()
	out := append(make([]engineapi.Player, 0, len(list)+16), list...)
	for _, s := range e.guard.Probe.Servers() {
		out = append(out, engineapi.Player{Rule: s.Rule, Src: s.Src, VID: s.VID, First: s.First, Last: s.Last, Count: s.Count,
			Server: true, Port: s.Port, Name: s.Name, Map: s.Map, Game: s.Game, Num: s.Num, Max: s.Max})
	}
	return out
}

// ProbeNow runs one server probe round right away (engine API, admin probe).
func (e *Engine) ProbeNow() {
	if st := e.st.Load(); st != nil {
		e.probe(st)
	}
}

// Ack acknowledges all alarms of a kind (engine API).
func (e *Engine) Ack(kind, by string) int {
	n := e.guard.Alarms.Ack(kind, by, time.Now())
	e.Log.Info("alarm acknowledged", "kind", kind, "by", by, "alarms", n)
	return n
}

// FinderStart starts a port finder session (engine API).
func (e *Engine) FinderStart(req engineapi.FinderStart) error {
	fr := FinderRequest{VIDs: req.VIDs, Duration: time.Duration(req.Seconds) * time.Second}
	if req.Src != "" {
		a, err := netip.ParseAddr(req.Src)
		if err != nil || !a.Is4() {
			return fmt.Errorf("bad sender address %q", req.Src)
		}
		fr.Src = a
	}
	var have []int
	if st := e.st.Load(); st != nil {
		for _, v := range st.table.Snapshot.VLANs {
			have = append(have, v.VID)
		}
	}
	if err := CheckFinderVIDs(req.VIDs, have); err != nil {
		return err
	}
	if err := e.guard.Finder.Start(fr, time.Now()); err != nil {
		return err
	}
	e.Log.Info("port finder started", "vids", req.VIDs, "src", req.Src, "seconds", req.Seconds)
	return nil
}

// FinderStop ends the port finder session (engine API).
func (e *Engine) FinderStop() {
	e.guard.Finder.Stop(time.Now())
	e.Log.Info("port finder stopped")
}

// Finder reports the port finder state (engine API).
func (e *Engine) Finder() engineapi.FinderStatus {
	return FinderAPI(e.guard.Finder.Status(time.Now()))
}

// FinderPcap writes the capture of the last session (engine API).
func (e *Engine) FinderPcap(w io.Writer) error { return e.guard.Finder.WritePcap(w) }

// Status reports the engine state (engine API).
func (e *Engine) Status() engineapi.Status {
	st := engineapi.Status{Version: e.Version, Started: e.started, Standby: e.guard.Standby(),
		RX: e.Counters.RX.Load(), Sent: e.Counters.Sent.Load(),
		Reasons: e.Counters.ReasonCounts(), BeaconAuth: e.beaconAuth, IfacePolicy: "auto"}
	if is := e.hostIssues.Load(); is != nil {
		for _, x := range *is {
			st.HostIssues = append(st.HostIssues, engineapi.HostIssue{Code: x.Code, Detail: x.Detail})
			st.SelfTest = append(st.SelfTest, x.Code+": "+x.Detail)
		}
	}
	if len(e.AllowedIfaces) > 0 {
		st.IfacePolicy = "list"
	}
	if cur := e.st.Load(); cur != nil {
		e.tableStatus(&st, cur.table, e.rates.Load())
	}
	for _, a := range e.guard.Alarms.List() {
		st.Alarms = append(st.Alarms, engineapi.Alarm(a))
	}
	st.TopUnknown = topUnknown(&e.Counters, 10)
	return st
}

// tableStatus fills in the snapshot, the rule and VLAN counters and the rates (rs; nil before the first sample).
func (e *Engine) tableStatus(st *engineapi.Status, t *Table, rs *rateSample) {
	snap := t.Snapshot
	st.Mode, st.SnapshotHash, st.VLANs, st.Rules = snap.Mode, snap.Hash, len(snap.VLANs), len(snap.Rules)
	st.Sending = e.guard.Sending(time.Now()) && snap.Mode == compile.ModeActive
	for _, r := range t.RuleStats() {
		st.RuleStats = append(st.RuleStats, engineapi.RuleStat{ID: r.ID, RX: r.RX, Relayed: r.Relayed})
	}
	for _, v := range t.VLANs {
		vs := engineapi.VLANStat{VID: v.VID, Name: v.Name, Group: v.Group, Interface: netif.LinkName(v.VLAN),
			In: e.Counters.VLANIn[v.VID].Load(), Out: e.Counters.VLANOut[v.VID].Load()}
		if rs != nil {
			vs.InRate, vs.LastIn = rs.vlanRate[v.VID], rs.lastIn[v.VID]
		}
		st.VLANStats = append(st.VLANStats, vs)
	}
	if rs != nil {
		st.RXRate, st.SentRate = rs.rxRate, rs.sentRate
	}
}

func topUnknown(c *Counters, n int) []engineapi.PortCount {
	var out []engineapi.PortCount
	for p := range c.UnknownPorts {
		if v := c.UnknownPorts[p].Load(); v > 0 {
			out = append(out, engineapi.PortCount{Port: uint16(p), Count: v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Count > out[j].Count })
	if len(out) > n {
		out = out[:n]
	}
	return out
}
