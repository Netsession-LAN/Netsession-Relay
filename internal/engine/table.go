package engine

import (
	"fmt"
	"net/netip"
	"sync/atomic"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

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
	groupVLANs, err := t.addVLANs(s.VLANs, macs)
	if err != nil {
		return nil, err
	}
	if err := t.addPeerMACs(s.Peers); err != nil {
		return nil, err
	}
	for _, r := range s.Rules {
		rt := t.newRule(t.boundRule(r), groupVLANs)
		if err := t.index(rt); err != nil {
			return nil, fmt.Errorf("rule %s: %w", r.ID, err)
		}
		t.rules = append(t.rules, rt)
	}
	return t, nil
}

// addVLANs adds the snapshot's VLANs with their interface MACs and returns the VLAN indexes of every group.
func (t *Table) addVLANs(vlans []compile.VLAN, macs map[int]pkt.MAC) (map[string][]int, error) {
	groupVLANs := map[string][]int{}
	for i, v := range vlans {
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
	return groupVLANs, nil
}

// addPeerMACs adds the MACs of the partner relays: their frames are never relayed again.
func (t *Table) addPeerMACs(peers []compile.Peer) error {
	for _, p := range peers {
		for _, m := range p.MACs {
			hw, err := parseMAC(m)
			if err != nil {
				return fmt.Errorf("peer %s: %w", p.Name, err)
			}
			t.ownMACs[hw] = true
		}
	}
	return nil
}

// newRule resolves a rule's flows and source filter per ingress VLAN.
func (t *Table) newRule(r compile.Rule, groupVLANs map[string][]int) *ruleRT {
	n := len(t.VLANs)
	rt := &ruleRT{Rule: r, targets: make([][]int, n), srcAllow: make([][]netip.Prefix, n), copies: make([]atomic.Uint64, n)}
	for in, v := range t.VLANs {
		rt.srcAllow[in] = prefixesWithin(r.SrcAllow, v.CIDR)
		rt.targets[in] = flowTargets(r.Flows, v.Group, in, groupVLANs)
	}
	return rt
}

// prefixesWithin returns the prefixes of ps that lie inside network (nil if none).
func prefixesWithin(ps []netip.Prefix, network netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range ps {
		if compile.PrefixWithin(p, network) {
			out = append(out, p)
		}
	}
	return out
}

// flowTargets returns the VLAN indexes the flows send a frame from VLAN in (of group src) to: each VLAN once, never
// the ingress VLAN itself.
func flowTargets(flows []compile.Edge, src string, in int, groupVLANs map[string][]int) []int {
	var out []int
	seen := map[int]bool{}
	for _, e := range flows {
		if e.Src != src {
			continue
		}
		for _, v := range groupVLANs[e.Dst] {
			if v != in && !seen[v] {
				seen[v] = true
				out = append(out, v)
			}
		}
	}
	return out
}

// index makes the rule findable by its ports (and multicast group); a port may belong to one rule only.
func (t *Table) index(rt *ruleRT) error {
	ps, err := parsePorts(rt.Ports)
	if err != nil {
		return err
	}
	switch rt.Kind {
	case gamedb.KindBroadcast:
		return t.indexBroadcast(rt, ps)
	case gamedb.KindMulticast:
		return t.indexMulticast(rt, ps)
	}
	return fmt.Errorf("kind %q", rt.Kind)
}

func (t *Table) indexBroadcast(rt *ruleRT, ports []uint16) error {
	for _, p := range ports {
		if other := t.broadcast[p]; other != nil {
			return fmt.Errorf("port %d already used by %s", p, other.ID)
		}
		t.broadcast[p] = rt
	}
	return nil
}

func (t *Table) indexMulticast(rt *ruleRT, ports []uint16) error {
	g, err := netip.ParseAddr(rt.Group)
	if err != nil || !g.Is4() || !g.IsMulticast() {
		return fmt.Errorf("bad group %q", rt.Group)
	}
	rt.group = g
	for _, p := range ports {
		k := mcastKey{g, p}
		if other := t.multicast[k]; other != nil {
			return fmt.Errorf("group %s port %d already used by %s", g, p, other.ID)
		}
		t.multicast[k] = rt
	}
	return nil
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
