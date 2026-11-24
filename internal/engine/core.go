// Package engine is the packet data plane: per-packet decisions (this file, portable and unit-tested on every
// OS) and the Linux runtime with AF_PACKET sockets (engine_linux.go).
//
// Processing order per received frame:
//
//  1. frame from an own or partner MAC                -> drop own_mac
//  2. DHCP ports 67/68, source 0.0.0.0                 -> drop blocked
//  3. source IP not in the ingress VLAN's network      -> drop foreign_relay (in another VLAN's network) or src_subnet
//  4. look up the rule by (broadcast | multicast group, destination port); none -> count unknown port
//     source filter of the rule restricts the ingress VLAN and the source is not allowed -> drop src_filter
//  5. target VLANs from the rule's flows (never the ingress VLAN)
//  6. payload size 1..payload_max
//  7. same datagram seen on another VLAN within dedup_ms -> drop loop (alarm)
//  8. rule auto-paused by the storm brake for this ingress VLAN -> drop auto_paused (before the buckets, so a
//     paused flood does not starve other games of the same network)
//  9. token buckets per (rule, source IP, port), per ingress VLAN (frames) and per ingress VLAN (copies the
//     frame fans out to) -> drop rate
//     engine not sending (observe/paused mode, standby, start holdoff) -> drop mode
//  10. fan-out: build one frame per target VLAN; a copy that cannot be built or sent -> send_error
//
// A panic while a frame is handled costs that frame only -> drop panic (engine_linux.go).
package engine

import (
	"cmp"
	"fmt"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/Netsession2024/Netsession-Relay/internal/compile"
	"github.com/Netsession2024/Netsession-Relay/internal/gamedb"
	"github.com/Netsession2024/Netsession-Relay/internal/pkt"
)

// Reason is why a frame was not relayed (or Relayed).
type Reason int

// Reasons. Keep in sync with reasonNames.
const (
	Relayed Reason = iota
	DropParse
	DropOwnMAC
	DropBlocked
	DropForeignRelay
	DropSrcSubnet
	DropNotBroadcast
	DropUnknownPort
	DropNoTarget
	DropSize
	DropTTL
	DropMode // engine in observe/paused/standby/holdoff: nothing is sent
	DropLoop
	DropRate
	DropAutoPaused
	DropSrcFilter // the rule's source filter restricts the ingress VLAN and the sender is not on it
	// DropSendError: at least one copy of the frame could not be built or sent (interface down, socket error).
	// Unlike the other reasons it is also counted for a frame of which other copies did go out, so the reason
	// counters can add up to more than the frames received.
	DropSendError
	DropPanic // handling the frame panicked; the engine keeps running
	numReasons
)

var reasonNames = [numReasons]string{
	"relayed", "parse", "own_mac", "blocked", "foreign_relay", "src_subnet", "not_broadcast",
	"unknown_port", "no_target", "size", "ttl", "mode", "loop", "rate", "auto_paused",
	"src_filter", "send_error", "panic",
}

func (r Reason) String() string {
	if r < 0 || r >= numReasons {
		return fmt.Sprintf("reason(%d)", int(r))
	}
	return reasonNames[r]
}

// VLANRuntime is a snapshot VLAN plus what the runtime learned about its interface.
type VLANRuntime struct {
	compile.VLAN
	Index     int // position in Table.VLANs
	Ifindex   int // kernel interface index (0 in unit tests)
	MAC       pkt.MAC
	Broadcast netip.Addr // directed broadcast of CIDR
}

type ruleRT struct {
	compile.Rule
	group netip.Addr // multicast only
	// targets[in] = VLAN indexes a frame arriving on VLAN in is sent to
	targets [][]int
	// srcAllow[in] = allowed source prefixes for frames arriving on VLAN in (nil = unrestricted)
	srcAllow [][]netip.Prefix
	rx       atomic.Uint64
	relayed  atomic.Uint64
	copies   []atomic.Uint64 // copies[in] = frames sent for frames that arrived on VLAN in (storm brake)
}

type mcastKey struct {
	group netip.Addr
	port  uint16
}

// Table is the immutable lookup structure built from a snapshot. The engine swaps tables atomically.
type Table struct {
	Snapshot *compile.Snapshot
	VLANs    []VLANRuntime
	Mode     string

	// Clamped lists the limits of the snapshot the engine did not take as they were (bounds.go), for the log.
	Clamped []string

	limits    compile.Limits // the snapshot's limits inside the engine's hard bounds
	copyRate  int            // copies per second one ingress VLAN may cause (AllowCopies)
	ownMACs   map[pkt.MAC]bool
	broadcast [65536]*ruleRT
	multicast map[mcastKey]*ruleRT
	rules     []*ruleRT
}

// NewTable builds the lookup table. macs maps VLAN ID -> interface MAC (from the runtime; may be empty in tests).
func NewTable(s *compile.Snapshot, macs map[int]pkt.MAC) (*Table, error) {
	t := &Table{Snapshot: s, Mode: s.Mode, ownMACs: map[pkt.MAC]bool{}, multicast: map[mcastKey]*ruleRT{}}
	t.limits = t.boundLimits(s.Limits)
	t.copyRate = max(t.limits.StormPPS/2, 1)
	groupVLANs := map[string][]int{}
	for i, v := range s.VLANs {
		if v.VID < 1 || v.VID > MaxVID {
			return nil, fmt.Errorf("VLAN %d (%s): VID out of range 1..%d", v.VID, v.Name, MaxVID)
		}
		v.RatePPS, v.Burst = t.boundVLAN(v)
		rt := VLANRuntime{VLAN: v, Index: i, MAC: macs[v.VID], Broadcast: pkt.SubnetBroadcast(v.CIDR)}
		t.VLANs = append(t.VLANs, rt)
		groupVLANs[v.Group] = append(groupVLANs[v.Group], i)
		if rt.MAC != (pkt.MAC{}) {
			t.ownMACs[rt.MAC] = true
		}
	}
	for _, p := range s.Peers {
		for _, m := range p.MACs {
			hw, err := parseMAC(m)
			if err != nil {
				return nil, fmt.Errorf("peer %s: %w", p.Name, err)
			}
			t.ownMACs[hw] = true
		}
	}
	for _, r := range s.Rules {
		r = t.boundRule(r)
		rt := &ruleRT{Rule: r, targets: make([][]int, len(t.VLANs)), srcAllow: make([][]netip.Prefix, len(t.VLANs)),
			copies: make([]atomic.Uint64, len(t.VLANs))}
		for in, v := range t.VLANs {
			for _, p := range r.SrcAllow {
				if compile.PrefixWithin(p, v.CIDR) {
					rt.srcAllow[in] = append(rt.srcAllow[in], p)
				}
			}
			seen := map[int]bool{}
			for _, e := range r.Flows {
				if e.Src != v.Group {
					continue
				}
				for _, out := range groupVLANs[e.Dst] {
					if out != in && !seen[out] {
						seen[out] = true
						rt.targets[in] = append(rt.targets[in], out)
					}
				}
			}
		}
		ps, err := parsePorts(r.Ports)
		if err != nil {
			return nil, fmt.Errorf("rule %s: %w", r.ID, err)
		}
		switch r.Kind {
		case gamedb.KindBroadcast:
			for _, p := range ps {
				if t.broadcast[p] != nil {
					return nil, fmt.Errorf("rule %s: port %d already used by %s", r.ID, p, t.broadcast[p].ID)
				}
				t.broadcast[p] = rt
			}
		case gamedb.KindMulticast:
			g, err := netip.ParseAddr(r.Group)
			if err != nil || !g.Is4() || !g.IsMulticast() {
				return nil, fmt.Errorf("rule %s: bad group %q", r.ID, r.Group)
			}
			rt.group = g
			for _, p := range ps {
				k := mcastKey{g, p}
				if other := t.multicast[k]; other != nil {
					return nil, fmt.Errorf("rule %s: group %s port %d already used by %s", r.ID, g, p, other.ID)
				}
				t.multicast[k] = rt
			}
		default:
			return nil, fmt.Errorf("rule %s: kind %q", r.ID, r.Kind)
		}
		t.rules = append(t.rules, rt)
	}
	return t, nil
}

// Counters are engine-wide statistics. All fields are safe for concurrent use.
type Counters struct {
	RX           atomic.Uint64
	Sent         atomic.Uint64 // frames sent (one received frame may produce several)
	Reasons      [numReasons]atomic.Uint64
	UnknownPorts [65536]atomic.Uint32      // broadcasts on ports without a rule (port finder)
	VLANIn       [MaxVID + 1]atomic.Uint64 // frames received per VLAN ID (after the kernel filter)
	VLANOut      [MaxVID + 1]atomic.Uint64 // frames sent per VLAN ID
}

// MaxVID is the highest VLAN ID (IEEE 802.1Q). NewTable refuses a snapshot with a VID outside 1..MaxVID, so the
// VID of a table VLAN is always a valid index of the per-VLAN counters.
const MaxVID = 4094

// Emit sends a built frame out of the VLAN with the given table index. The frame buffer is only valid
// during the call.
type Emit func(vlanIndex int, frame []byte) error

// Processor handles frames for one receive goroutine. It owns a reusable output buffer and is not safe
// for concurrent use; create one per goroutine.
type Processor struct {
	c   *Counters
	g   *Guard // nil: steps 7-9 and the dual-active guard are skipped (unit tests of the basic pipeline)
	buf []byte
}

// NewProcessor returns a processor that counts into c and protects with g.
func NewProcessor(c *Counters, g *Guard) *Processor {
	return &Processor{c: c, g: g, buf: make([]byte, 0, 2048)}
}

// Process decides about one frame received on VLAN index in and calls emit for every target VLAN.
// It returns the reason for the frame as a whole (Relayed if at least one copy was sent).
func (p *Processor) Process(t *Table, in int, frame []byte, emit Emit) Reason {
	p.c.RX.Add(1)
	p.c.VLANIn[t.VLANs[in].VID].Add(1)
	r := p.process(t, in, frame, emit)
	p.c.Reasons[r].Add(1)
	return r
}

func (p *Processor) process(t *Table, in int, frame []byte, emit Emit) Reason {
	f, err := pkt.Parse(frame)
	if err != nil {
		return DropParse
	}
	// 1. own or partner frames
	if t.ownMACs[f.SrcMAC] {
		return DropOwnMAC
	}
	// 2. fixed block list
	if f.DstPort == 67 || f.DstPort == 68 || f.SrcPort == 67 || f.SrcPort == 68 || f.Src.IsUnspecified() {
		return DropBlocked
	}
	vin := &t.VLANs[in]
	// 3. source subnet
	if !vin.CIDR.Contains(f.Src) {
		for i := range t.VLANs {
			if i != in && t.VLANs[i].CIDR.Contains(f.Src) {
				if p.g != nil {
					p.g.Alarms.Raise(AlarmForeignRelay, fmt.Sprintf("%d", vin.VID), p.g.Now(),
						"VLAN %d (%s) receives frames from %s (network of VLAN %d): another relay is active",
						vin.VID, vin.Name, f.Src, t.VLANs[i].VID)
				}
				return DropForeignRelay
			}
		}
		return DropSrcSubnet
	}
	// 4. rule lookup
	var rule *ruleRT
	switch {
	case f.Dst.IsMulticast():
		rule = t.multicast[mcastKey{f.Dst, f.DstPort}]
	case f.Dst == pkt.LimitedBroadcast || f.Dst == vin.Broadcast:
		rule = t.broadcast[f.DstPort]
		if rule == nil {
			p.c.UnknownPorts[f.DstPort].Add(1)
		}
	default:
		return DropNotBroadcast
	}
	if p.g != nil && p.g.Finder.active.Load() {
		id := ""
		if rule != nil {
			id = rule.ID
		}
		p.g.Finder.Observe(vin.VID, &f, frame, id, p.g.Now())
	}
	if rule == nil {
		return DropUnknownPort
	}
	if allow := rule.srcAllow[in]; allow != nil && !containsAddr(allow, f.Src) {
		return DropSrcFilter
	}
	rule.rx.Add(1)
	if p.g != nil {
		p.g.Players.Seen(rule.ID, f.Src, vin.VID, p.g.Now())
		// only payloads the rule would relay are replayed by the server probe
		if rule.Probe && t.Snapshot.Probe && len(f.Payload) <= rule.PayloadMax {
			p.g.Probe.Learn(rule.ID, f.DstPort, f.Payload, p.g.Now())
		}
	}
	// 5. targets
	targets := rule.targets[in]
	if len(targets) == 0 {
		return DropNoTarget
	}
	// 6. size
	if len(f.Payload) == 0 || len(f.Payload) > rule.PayloadMax {
		return DropSize
	}
	if rule.TTLMode == gamedb.TTLDecrement {
		if f.TTL <= 1 {
			return DropTTL
		}
		f.TTL--
	}
	if g := p.g; g != nil {
		now := g.Now()
		// 7. loop detection across VLANs
		if g.SeenElsewhere(&f, in, now, time.Duration(t.limits.DedupMS)*time.Millisecond) {
			g.Alarms.Raise(AlarmLoop, rule.ID, now, "same datagram from %s:%d seen on several VLANs (loop or second relay)", f.Src, f.SrcPort)
			return DropLoop
		}
		// 8. storm brake: before the rate limits, so a paused flood does not use up the budgets of its network
		// and other games from there keep working
		if g.Paused(rule.ID, vin.VID, now) {
			return DropAutoPaused
		}
		// 9. rate limits
		if !g.AllowFlow(rule.ID, f.Src, f.DstPort, rule.RatePPS, rule.RateBurst, now) {
			return DropRate
		}
		vr, vb := cmp.Or(vin.RatePPS, t.limits.VLANPPS), cmp.Or(vin.Burst, t.limits.VLANBurst)
		if !g.AllowVLAN(vin.VID, vr, vb, now) {
			return DropRate
		}
		if !g.AllowCopies(vin.VID, len(targets), t.copyRate, t.copyRate, now) {
			return DropRate
		}
		if !g.Sending(now) {
			return DropMode
		}
	}
	if t.Mode != compile.ModeActive {
		return DropMode
	}
	// 10. fan-out
	sent, failed := 0, 0
	limited := f.Dst == pkt.LimitedBroadcast
	orig := f
	for _, out := range targets {
		vout := &t.VLANs[out]
		f = orig
		f.SrcMAC = vout.MAC
		if rule.Kind == gamedb.KindMulticast {
			f.DstMAC = pkt.MulticastMAC(f.Dst)
		} else {
			f.DstMAC = pkt.BroadcastMAC
			switch rule.DstMode {
			case gamedb.DstLimited:
				f.Dst = pkt.LimitedBroadcast
			case gamedb.DstSubnet:
				f.Dst = vout.Broadcast
			default: // keep
				if !limited {
					f.Dst = vout.Broadcast
				}
			}
		}
		if rule.SourceMode != gamedb.SourceOriginal && vout.RelayIP.IsValid() {
			f.Src = vout.RelayIP
		}
		b, err := pkt.Build(p.buf, &f)
		if err != nil {
			failed++
			continue
		}
		p.buf = b[:0]
		if emit(out, b) != nil {
			failed++
			continue
		}
		sent++
		p.c.Sent.Add(1)
		p.c.VLANOut[vout.VID].Add(1)
	}
	if sent == 0 {
		return DropSendError // there were targets (step 5), so every copy failed
	}
	if failed > 0 {
		p.c.Reasons[DropSendError].Add(1) // some copies went out: relayed, and the failure is counted too
	}
	rule.relayed.Add(1)
	rule.copies[in].Add(uint64(sent))
	return Relayed
}

// RuleStats is a per-rule counter snapshot.
type RuleStats struct {
	ID      string
	RX      uint64
	Relayed uint64
}

// RuleStats returns counters of all rules in snapshot order.
func (t *Table) RuleStats() []RuleStats {
	out := make([]RuleStats, 0, len(t.rules))
	for _, r := range t.rules {
		out = append(out, RuleStats{r.ID, r.rx.Load(), r.relayed.Load()})
	}
	return out
}

// ReasonCounts returns the reason counters as a map (for logs and the API).
func (c *Counters) ReasonCounts() map[string]uint64 {
	m := map[string]uint64{}
	for i := range c.Reasons {
		if v := c.Reasons[i].Load(); v > 0 {
			m[Reason(i).String()] = v
		}
	}
	return m
}

func containsAddr(ps []netip.Prefix, a netip.Addr) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
