package web

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/netif"
)

// The addresses the guard refuses: the relay IPs of the configured networks and the addresses of the interfaces on
// the relay side.

// hostIfaces lists the interfaces of the host (HostIfaces in tests and the demo).
func (s *Server) hostIfaces() ([]netif.HostIface, error) {
	if s.HostIfaces != nil {
		return s.HostIfaces()
	}
	return netif.Interfaces()
}

func addrOf(cidr string) (netip.Addr, bool) {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return netip.Addr{}, false
	}
	return p.Addr().Unmap(), true
}

// relayAddrs computes the addresses the UI is not served on.
func (s *Server) relayAddrs(ctx context.Context) map[netip.Addr]bool {
	out, names := s.configuredRelaySide(ctx)
	ifs, err := s.hostIfaces()
	if err != nil {
		return out // not on Linux: the configured relay IPs are all that is known
	}
	relay := func(i netif.HostIface) bool { return guardRelayIface(i, names) }
	for _, a := range guardAddrsWhere(ifs, relay) {
		out[a] = true
	}
	for _, a := range guardAddrsWhere(ifs, guardMgmtIface) {
		delete(out, a)
	}
	return out
}

// configuredRelaySide reads the configured networks: their relay IPs and the names of the interfaces they use.
// An unreadable configuration gives two empty sets.
func (s *Server) configuredRelaySide(ctx context.Context) (ips map[netip.Addr]bool, ifaceNames map[string]bool) {
	ips, ifaceNames = map[netip.Addr]bool{}, map[string]bool{}
	c, err := s.Store.ExportConfig(ctx)
	if err != nil {
		return ips, ifaceNames
	}
	for _, v := range c.VLANs {
		if a, err := netip.ParseAddr(v.RelayIP); err == nil {
			ips[a.Unmap()] = true
		}
		if v.Attach == compile.AttachDevice {
			ifaceNames[v.Device] = true
		} else {
			ifaceNames[fmt.Sprintf("%s.%d", v.Parent, v.VID)] = true
		}
	}
	return ips, ifaceNames
}

// guardMgmtIface reports whether an interface belongs to the management side (default route or loopback); its
// addresses are never refused.
func guardMgmtIface(i netif.HostIface) bool { return i.DefaultRoute || i.Loopback }

// guardRelayIface reports whether an interface is on the relay side: used by a configured network or created by
// the engine, and not on the management side.
func guardRelayIface(i netif.HostIface, configured map[string]bool) bool {
	return (configured[i.Name] || i.Relay) && !guardMgmtIface(i)
}

// guardAddrsWhere gives the parseable addresses (without prefix length) of the interfaces that match, in the
// order of the interfaces.
func guardAddrsWhere(ifs []netif.HostIface, match func(netif.HostIface) bool) []netip.Addr {
	var out []netip.Addr
	for _, i := range ifs {
		if !match(i) {
			continue
		}
		for _, c := range i.Addrs {
			if a, ok := addrOf(c); ok {
				out = append(out, a)
			}
		}
	}
	return out
}
