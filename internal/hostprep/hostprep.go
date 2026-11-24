// Package hostprep prepares the host for the relay, so that nothing has to be set up by hand before the first
// start: the engine runs in the network namespace of the host with CAP_NET_ADMIN and does it itself every time
// it applies a snapshot.
//
// For every relay interface (VLAN interfaces on the trunk, interfaces attached as a device, the trunk itself
// when it has no address):
//
//   - IPv4 forwarding off, reverse-path filter off, ARP only for the own address, IPv6 off;
//   - an nftables table "nsrelay_engine": nothing is forwarded from or to these interfaces, and from them the
//     host accepts only ping, IGMP and answers to its own connections. Participants therefore reach neither
//     SSH nor the web UI nor any other service of the host through a relay address. The relay itself reads
//     with packet sockets before the firewall and is not affected.
//
// And for the management side: answers from a management address to a client inside a relayed network leave
// through the management gateway, not directly through the VLAN interface (asymmetric routing breaks TCP at
// stateful firewalls).
//
// Interfaces that carry a default route (the management interface) are never touched, even when the relay
// uses one of them as a device. Whatever cannot be done is reported, the relay keeps working.
package hostprep

import (
	"net/netip"
	"sort"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/netif"
)

// Issue codes.
const (
	IssueSysctl    = "sysctl"     // per-interface IPv4 settings
	IssueIPv6      = "ipv6"       // IPv6 could not be switched off on a relay interface
	IssueFirewall  = "firewall"   // nftables table
	IssueMgmtRoute = "mgmt_route" // source routing for the management address
	IssueForward   = "forward"    // the host forwards IPv4 and neither sysctl nor firewall stop it on relay interfaces
)

// Issue is one thing the engine could not prepare.
type Issue struct {
	Code   string `json:"code"`
	Detail string `json:"detail"` // interfaces and the error of the kernel, English
}

// Plan is what a snapshot needs from the host.
type Plan struct {
	// Ifaces are the relay interfaces to shield, sorted. Management interfaces are not in it.
	Ifaces []string
	// Networks are the relayed networks whose interface is shielded (for the management routes).
	Networks []netip.Prefix
}

// PlanFor derives the plan from a snapshot. protected are the interfaces carrying a default route; hasAddr
// reports whether an interface has an IPv4 address of its own (a trunk with an address is used for something
// else as well, management or storage, and is left alone; its VLAN interfaces are shielded all the same).
func PlanFor(s *compile.Snapshot, protected map[string]bool, hasAddr func(string) bool) Plan {
	set := map[string]bool{}
	var p Plan
	for _, v := range s.VLANs {
		name := netif.LinkName(v)
		if !shieldable(name, protected) {
			continue
		}
		set[name] = true
		if v.CIDR.IsValid() {
			p.Networks = append(p.Networks, v.CIDR.Masked())
		}
		if shieldsTrunk(v, protected, set, hasAddr) {
			set[v.Parent] = true
		}
	}
	for n := range set {
		p.Ifaces = append(p.Ifaces, n)
	}
	sort.Strings(p.Ifaces)
	sort.Slice(p.Networks, func(i, j int) bool { return p.Networks[i].Addr().Less(p.Networks[j].Addr()) })
	return p
}

// shieldable reports whether an interface may be shielded at all: never the loopback, never an interface that
// carries a default route.
func shieldable(name string, protected map[string]bool) bool { return name != "lo" && !protected[name] }

// shieldsTrunk reports whether the trunk of a VLAN interface is to be shielded as well: it is shieldable, not
// in the set yet and has no IPv4 address of its own.
func shieldsTrunk(v compile.VLAN, protected, set map[string]bool, hasAddr func(string) bool) bool {
	if v.Attach != compile.AttachTrunk || !shieldable(v.Parent, protected) {
		return false
	}
	return !set[v.Parent] && !hasAddr(v.Parent)
}

// mgmtRule is one policy rule of the source routing for the management side: answers from the management
// address Src to the relayed network Dst use the management routing table.
type mgmtRule struct {
	Src netip.Addr
	Dst netip.Prefix
}

// mgmtRules returns the policy rules the management addresses need: one per address and relayed network. A
// relayed network that is on-link on the management interface itself needs no detour and gets none.
func mgmtRules(addrs []netip.Addr, networks, onLink []netip.Prefix) []mgmtRule {
	var out []mgmtRule
	for _, a := range addrs {
		for _, n := range networks {
			if !overlapsAny(n, onLink) {
				out = append(out, mgmtRule{Src: a, Dst: n})
			}
		}
	}
	return out
}

func overlapsAny(p netip.Prefix, list []netip.Prefix) bool {
	for _, q := range list {
		if p.Overlaps(q) {
			return true
		}
	}
	return false
}
