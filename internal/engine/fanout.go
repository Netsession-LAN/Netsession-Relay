package engine

import (
	"net/netip"

	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

// fanOut sends one copy of the frame to every target VLAN of the rule.
func (p *Processor) fanOut(t *Table, rule *ruleRT, in int, f *pkt.Frame, emit Emit) Reason {
	sent, failed := 0, 0
	for _, out := range rule.targets[in] {
		if p.sendCopy(t, rule, out, f, emit) {
			sent++
		} else {
			failed++
		}
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

// sendCopy builds the copy of orig for VLAN index out and emits it; false if it could not be built or sent.
func (p *Processor) sendCopy(t *Table, rule *ruleRT, out int, orig *pkt.Frame, emit Emit) bool {
	vout := &t.VLANs[out]
	f := *orig
	rule.rewrite(&f, vout)
	b, err := pkt.Build(p.buf, &f)
	if err != nil {
		return false
	}
	p.buf = b[:0]
	if emit(out, b) != nil {
		return false
	}
	p.c.Sent.Add(1)
	p.c.VLANOut[vout.VID].Add(1)
	return true
}

// rewrite turns a received frame into the copy for VLAN vout: the relay's MAC as source, the destination after
// the rule's destination mode and, unless the rule keeps the original sender, the VLAN's relay IP as source.
func (r *ruleRT) rewrite(f *pkt.Frame, vout *VLANRuntime) {
	f.SrcMAC = vout.MAC
	if r.Kind == gamedb.KindMulticast {
		f.DstMAC = pkt.MulticastMAC(f.Dst)
	} else {
		f.DstMAC = pkt.BroadcastMAC
		f.Dst = r.broadcastDst(f.Dst, vout)
	}
	if r.SourceMode != gamedb.SourceOriginal && vout.RelayIP.IsValid() {
		f.Src = vout.RelayIP
	}
}

// broadcastDst is the destination address of a broadcast copy for VLAN vout.
func (r *ruleRT) broadcastDst(dst netip.Addr, vout *VLANRuntime) netip.Addr {
	switch r.DstMode {
	case gamedb.DstLimited:
		return pkt.LimitedBroadcast
	case gamedb.DstSubnet:
		return vout.Broadcast
	}
	// keep: a limited broadcast stays one, a directed broadcast becomes the one of the target VLAN
	if dst == pkt.LimitedBroadcast {
		return dst
	}
	return vout.Broadcast
}
