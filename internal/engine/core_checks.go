package engine

import (
	"fmt"
	"net/netip"

	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

// The checks of pipeline steps 1-6 (core.go): the sender, then source filter, targets, size and TTL of the frame.

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

// admit applies the rule's source filter, records the sender, and checks targets, size and TTL (steps 4-6).
// With TTL mode decrement it lowers the frame's TTL.
func (p *Processor) admit(t *Table, rule *ruleRT, in int, f *pkt.Frame) Reason {
	if allow := rule.srcAllow[in]; allow != nil && !containsAddr(allow, f.Src) {
		return DropSrcFilter
	}
	rule.rx.Add(1)
	p.learn(t, rule, t.VLANs[in].VID, f)
	return checkDelivery(rule, in, f)
}

// checkDelivery checks that the frame has targets, a size the rule relays and TTL left (steps 5-6).
func checkDelivery(rule *ruleRT, in int, f *pkt.Frame) Reason {
	if len(rule.targets[in]) == 0 {
		return DropNoTarget
	}
	if len(f.Payload) == 0 || len(f.Payload) > rule.PayloadMax {
		return DropSize
	}
	return applyTTLMode(rule.TTLMode, f)
}

// applyTTLMode lowers the TTL with mode decrement; a frame whose TTL would reach zero is dropped.
func applyTTLMode(mode string, f *pkt.Frame) Reason {
	if mode != gamedb.TTLDecrement {
		return pass
	}
	if f.TTL <= 1 {
		return DropTTL
	}
	f.TTL--
	return pass
}

func containsAddr(ps []netip.Prefix, a netip.Addr) bool {
	for _, p := range ps {
		if p.Contains(a) {
			return true
		}
	}
	return false
}
