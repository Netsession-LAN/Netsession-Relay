package web

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/netif"
)

// The trunk interface of the setup (step 2): which host interfaces are offered, which are greyed out and which
// one is preselected.

// Interfaces left out of the trunk choice: by kind, and by name for container plumbing (inside a container
// (LXC) the real interfaces are veth devices, too, so veth goes by name, not by kind).
var (
	trunkHiddenKinds    = []string{"vlan", "tun", "wireguard"}
	trunkHiddenPrefixes = []string{"docker", "br-", "veth"}
)

// trunkCandidates lists the interfaces of the host for the trunk choice. Interfaces the relay must not use
// stay in the list, greyed out with the reason; purely internal ones (loopback, the VLAN interfaces of the
// relay, container plumbing) are left out.
func (s *Server) trunkCandidates(ctx context.Context) []setupIface {
	ifs, err := s.hostIfaces()
	if err != nil {
		return nil
	}
	// with a list of allowed interfaces on the engine, the operator has decided: nothing is greyed out here
	listed := s.engineListsIfaces(ctx)
	var out []setupIface
	for _, i := range ifs {
		if hiddenFromTrunkChoice(i) {
			continue
		}
		out = append(out, trunkCandidate(i, listed))
	}
	return out
}

// engineListsIfaces reports whether the engine runs with a list of allowed interfaces (false when it does not
// answer within 2 seconds).
func (s *Server) engineListsIfaces(ctx context.Context) bool {
	sctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	st, err := s.Engine.Status(sctx)
	return err == nil && st.IfacePolicy == "list"
}

// hiddenFromTrunkChoice reports whether an interface is purely internal and not offered at all.
func hiddenFromTrunkChoice(i netif.HostIface) bool {
	if i.Loopback || i.Relay {
		return true
	}
	return slices.Contains(trunkHiddenKinds, i.Kind) ||
		slices.ContainsFunc(trunkHiddenPrefixes, func(p string) bool { return strings.HasPrefix(i.Name, p) })
}

// trunkCandidate is one entry of the trunk choice: the management interface is blocked unless the engine has a
// list of allowed interfaces (listed); an interface with an own IPv4 address gets a note.
func trunkCandidate(i netif.HostIface, listed bool) setupIface {
	c := setupIface{HostIface: i}
	switch {
	case i.DefaultRoute && !listed:
		c.Blocked = "Verwaltung: Über diese Schnittstelle erreichst du den Server. Das Relay nimmt sie nur, wenn sie in NSRELAY_ALLOWED_IFACES der Engine steht."
	case slices.ContainsFunc(i.Addrs, isIPv4Prefix):
		c.Note = "Hat schon eine eigene Adresse. Sie bleibt, die Netze kommen als VLANs dazu."
	}
	return c
}

// isIPv4Prefix reports whether a is an IPv4 address with prefix length ("10.0.0.5/24").
func isIPv4Prefix(a string) bool {
	p, err := netip.ParsePrefix(a)
	return err == nil && p.Addr().Is4()
}

// soleTrunkCandidate is the name of the only selectable interface, "" when there is no choice or more than one
// (only preselect when there is no choice to make).
func soleTrunkCandidate(ifs []setupIface) string {
	name, free := "", 0
	for _, i := range ifs {
		if i.Blocked == "" {
			free++
			name = i.Name
		}
	}
	if free != 1 {
		return ""
	}
	return name
}

// setupTrunkProblem checks the chosen trunk against the interfaces of the host ("" when it is fine or the
// interfaces cannot be listed), in the language of T.
func (s *Server) setupTrunkProblem(ctx context.Context, trunk string, T translator) string {
	ifs := s.trunkCandidates(ctx)
	if ifs == nil {
		return ""
	}
	i := slices.IndexFunc(ifs, func(c setupIface) bool { return c.Name == trunk })
	switch {
	case i < 0:
		return T("Die Schnittstelle %s gibt es auf diesem Host nicht.", trunk)
	case ifs[i].Blocked != "":
		return T("%s ist die Verwaltungsschnittstelle dieses Hosts. Das Relay braucht eine eigene Schnittstelle für die Spieler-Netze.", trunk)
	}
	return ""
}
