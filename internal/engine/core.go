// Package engine is the packet data plane: per-packet decisions (this file, the lookup table in table.go and the
// copies in fanout.go; portable and unit-tested on every OS) and the Linux runtime with AF_PACKET sockets
// (engine_linux.go, apply_linux.go, receive_linux.go).
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
// A panic while a frame is handled costs that frame only -> drop panic (receive_linux.go).
package engine

import (
	"cmp"
	"fmt"
	"net/netip"
	"sync/atomic"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
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

// pass is a pipeline step's result for a frame the step lets through to the next step.
const pass = Relayed

func (p *Processor) process(t *Table, in int, frame []byte, emit Emit) Reason {
	f, err := pkt.Parse(frame)
	if err != nil {
		return DropParse
	}
	// 1.-3. sender
	if r := p.checkSender(t, in, &f); r != pass {
		return r
	}
	// 4. rule lookup
	vin := &t.VLANs[in]
	rule, r := p.lookupRule(t, vin, &f)
	if r != pass {
		return r
	}
	p.observe(vin, &f, frame, rule)
	if rule == nil {
		return DropUnknownPort
	}
	// 4.-6. the rule's own checks
	if r := p.admit(t, rule, in, &f); r != pass {
		return r
	}
	// 7.-9. guard
	if r := p.protect(t, rule, in, &f); r != pass {
		return r
	}
	if t.Mode != compile.ModeActive {
		return DropMode
	}
	// 10. fan-out
	return p.fanOut(t, rule, in, &f, emit)
}

// checkSender drops frames of own or partner MACs, of the fixed block list and from outside the ingress VLAN's
// network (steps 1-3).
func (p *Processor) checkSender(t *Table, in int, f *pkt.Frame) Reason {
	if t.ownMACs[f.SrcMAC] {
		return DropOwnMAC
	}
	if isBlocked(f) {
		return DropBlocked
	}
	if !t.VLANs[in].CIDR.Contains(f.Src) {
		return p.foreignSource(t, in, f.Src)
	}
	return pass
}

// isBlocked reports frames of the fixed block list: DHCP and senders without an address.
func isBlocked(f *pkt.Frame) bool {
	return isDHCPPort(f.DstPort) || isDHCPPort(f.SrcPort) || f.Src.IsUnspecified()
}

func isDHCPPort(port uint16) bool { return port == 67 || port == 68 }

// foreignSource classifies a source outside the ingress VLAN's network: in the network of another VLAN another
// relay is active (alarm), otherwise the sender is simply misconfigured.
func (p *Processor) foreignSource(t *Table, in int, src netip.Addr) Reason {
	for i := range t.VLANs {
		if i != in && t.VLANs[i].CIDR.Contains(src) {
			p.raiseForeignRelay(&t.VLANs[in], src, t.VLANs[i].VID)
			return DropForeignRelay
		}
	}
	return DropSrcSubnet
}

func (p *Processor) raiseForeignRelay(vin *VLANRuntime, src netip.Addr, otherVID int) {
	if p.g == nil {
		return
	}
	p.g.Alarms.Raise(AlarmForeignRelay, fmt.Sprintf("%d", vin.VID), p.g.Now(),
		"VLAN %d (%s) receives frames from %s (network of VLAN %d): another relay is active",
		vin.VID, vin.Name, src, otherVID)
}

// lookupRule finds the rule of a multicast or broadcast frame (nil: no rule for the port; broadcasts on such ports
// are counted for the port finder). Other destinations are not relayed at all.
func (p *Processor) lookupRule(t *Table, vin *VLANRuntime, f *pkt.Frame) (*ruleRT, Reason) {
	switch {
	case f.Dst.IsMulticast():
		return t.multicast[mcastKey{f.Dst, f.DstPort}], pass
	case f.Dst == pkt.LimitedBroadcast || f.Dst == vin.Broadcast:
		rule := t.broadcast[f.DstPort]
		if rule == nil {
			p.c.UnknownPorts[f.DstPort].Add(1)
		}
		return rule, pass
	}
	return nil, DropNotBroadcast
}

// observe hands the frame to the game finder while it records.
func (p *Processor) observe(vin *VLANRuntime, f *pkt.Frame, frame []byte, rule *ruleRT) {
	if p.g == nil || !p.g.Finder.active.Load() {
		return
	}
	id := ""
	if rule != nil {
		id = rule.ID
	}
	p.g.Finder.Observe(vin.VID, f, frame, id, p.g.Now())
}

// admit applies the rule's source filter, records the sender, and checks targets, size and TTL (steps 4-6).
// With TTL mode decrement it lowers the frame's TTL.
func (p *Processor) admit(t *Table, rule *ruleRT, in int, f *pkt.Frame) Reason {
	if allow := rule.srcAllow[in]; allow != nil && !containsAddr(allow, f.Src) {
		return DropSrcFilter
	}
	rule.rx.Add(1)
	p.learn(t, rule, t.VLANs[in].VID, f)
	if len(rule.targets[in]) == 0 {
		return DropNoTarget
	}
	if len(f.Payload) == 0 || len(f.Payload) > rule.PayloadMax {
		return DropSize
	}
	if rule.TTLMode == gamedb.TTLDecrement {
		if f.TTL <= 1 {
			return DropTTL
		}
		f.TTL--
	}
	return pass
}

// learn notes the player for the players page and the payload for the server probe.
func (p *Processor) learn(t *Table, rule *ruleRT, vid int, f *pkt.Frame) {
	g := p.g
	if g == nil {
		return
	}
	g.Players.Seen(rule.ID, f.Src, vid, g.Now())
	// only payloads the rule would relay are replayed by the server probe
	if rule.Probe && t.Snapshot.Probe && len(f.Payload) <= rule.PayloadMax {
		g.Probe.Learn(rule.ID, f.DstPort, f.Payload, g.Now())
	}
}

// protect runs the guard: loop detection, storm brake, rate limits and whether the engine sends (steps 7-9).
func (p *Processor) protect(t *Table, rule *ruleRT, in int, f *pkt.Frame) Reason {
	g := p.g
	if g == nil {
		return pass
	}
	now := g.Now()
	// 7. loop detection across VLANs
	if g.SeenElsewhere(f, in, now, time.Duration(t.limits.DedupMS)*time.Millisecond) {
		g.Alarms.Raise(AlarmLoop, rule.ID, now, "same datagram from %s:%d seen on several VLANs (loop or second relay)", f.Src, f.SrcPort)
		return DropLoop
	}
	// 8. storm brake: before the rate limits, so a paused flood does not use up the budgets of its network
	// and other games from there keep working
	if g.Paused(rule.ID, t.VLANs[in].VID, now) {
		return DropAutoPaused
	}
	// 9. rate limits
	if !p.withinRates(t, rule, in, f, now) {
		return DropRate
	}
	if !g.Sending(now) {
		return DropMode
	}
	return pass
}

// withinRates takes the frame from the token buckets per flow, per ingress VLAN and per ingress VLAN's copies.
func (p *Processor) withinRates(t *Table, rule *ruleRT, in int, f *pkt.Frame, now time.Time) bool {
	g, vin := p.g, &t.VLANs[in]
	if !g.AllowFlow(rule.ID, f.Src, f.DstPort, rule.RatePPS, rule.RateBurst, now) {
		return false
	}
	vr, vb := cmp.Or(vin.RatePPS, t.limits.VLANPPS), cmp.Or(vin.Burst, t.limits.VLANBurst)
	if !g.AllowVLAN(vin.VID, vr, vb, now) {
		return false
	}
	return g.AllowCopies(vin.VID, len(rule.targets[in]), t.copyRate, t.copyRate, now)
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
