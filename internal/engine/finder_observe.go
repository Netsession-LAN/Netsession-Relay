package engine

import (
	"net/netip"
	"slices"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

// Recording frames while the port finder runs: the group of a frame, its senders and a few captured frames.

type groupKey struct {
	group netip.Addr // zero for broadcast
	port  uint16
}

type capFrame struct {
	ts   time.Time
	data []byte
}

// Observe records one parsed frame received on VLAN vid. rule is the matching rule ID or "".
func (f *Finder) Observe(vid int, fr *pkt.Frame, raw []byte, rule string, now time.Time) {
	if !f.active.Load() {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.s
	if s == nil {
		return
	}
	if !now.Before(s.until) {
		f.active.Store(false)
		return
	}
	if !s.wants(vid, fr.Src) {
		return
	}
	s.frames++
	k := frameGroupKey(fr)
	g := s.groups[k]
	if g == nil {
		if g = s.newGroup(k, fr, rule, now); g == nil {
			return
		}
	}
	g.record(vid, fr, now)
	s.capture(raw, now)
}

// wants applies the session's filter: VLAN and sender.
func (s *finderSession) wants(vid int, src netip.Addr) bool {
	if s.vids != nil && !s.vids[vid] {
		return false
	}
	return !s.req.Src.IsValid() || src == s.req.Src
}

// frameGroupKey returns the group of a frame: its multicast group (none for a broadcast) and destination port.
func frameGroupKey(fr *pkt.Frame) groupKey {
	k := groupKey{port: fr.DstPort}
	if fr.Dst.IsMulticast() {
		k.group = fr.Dst
	}
	return k
}

// newGroup records the group k of a frame seen for the first time (nil when the session has finderMaxGroups
// already).
func (s *finderSession) newGroup(k groupKey, fr *pkt.Frame, rule string, now time.Time) *FinderGroup {
	if len(s.groups) >= finderMaxGroups {
		return nil
	}
	g := &FinderGroup{Kind: "broadcast", Port: fr.DstPort, Rule: rule, First: now, MinLen: len(fr.Payload),
		New: now.Sub(s.started) > finderNewAfter}
	if k.group.IsValid() {
		g.Kind, g.Group = "multicast", k.group.String()
	}
	g.Sample = append([]byte(nil), fr.Payload[:min(len(fr.Payload), finderSampleLen)]...)
	s.groups[k] = g
	return g
}

// record counts one frame of the group from VLAN vid.
func (g *FinderGroup) record(vid int, fr *pkt.Frame, now time.Time) {
	g.Count++
	g.Last = now
	g.MinLen, g.MaxLen = min(g.MinLen, len(fr.Payload)), max(g.MaxLen, len(fr.Payload))
	if len(g.Srcs) < finderMaxSrcs && !slices.Contains(g.Srcs, fr.Src) {
		g.Srcs = append(g.Srcs, fr.Src)
	}
	if !slices.Contains(g.VIDs, vid) {
		g.VIDs = append(g.VIDs, vid)
	}
}

// capture keeps a copy of the raw frame for the pcap download while the limits allow.
func (s *finderSession) capture(raw []byte, now time.Time) {
	if len(s.caps) >= finderMaxFrames || s.bytes+len(raw) > finderMaxBytes {
		return
	}
	s.caps = append(s.caps, capFrame{ts: now, data: append([]byte(nil), raw...)})
	s.bytes += len(raw)
}
