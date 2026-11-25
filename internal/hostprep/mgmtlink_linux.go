//go:build linux

package hostprep

import (
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
)

// What the management link has: its on-link networks and its global IPv4 addresses (the management addresses).

// onLinkNetworks are the networks directly attached to the link (link-scope routes without a gateway, not the
// default route). A link whose routes cannot be read has none.
func onLinkNetworks(link netlink.Link) []*net.IPNet {
	rs, err := netlink.RouteList(link, netlink.FAMILY_V4)
	if err != nil {
		return nil
	}
	var out []*net.IPNet
	for _, r := range rs {
		if isOnLinkNetwork(r) {
			out = append(out, r.Dst)
		}
	}
	return out
}

func isOnLinkNetwork(r netlink.Route) bool {
	return r.Gw == nil && r.Scope == netlink.SCOPE_LINK && !isDefault(r.Dst)
}

// prefixesOf converts the IPv4 networks to prefixes, skipping any that are not IPv4.
func prefixesOf(nets []*net.IPNet) []netip.Prefix {
	var out []netip.Prefix
	for _, n := range nets {
		if p, ok := prefixOf(n); ok {
			out = append(out, p)
		}
	}
	return out
}

// globalAddrs are the IPv4 addresses of global scope on the link: the management addresses.
func globalAddrs(link netlink.Link) ([]netip.Addr, error) {
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, a := range addrs {
		if a.Scope != int(netlink.SCOPE_UNIVERSE) {
			continue
		}
		if ip, ok := netip.AddrFromSlice(a.IP.To4()); ok {
			out = append(out, ip)
		}
	}
	return out, nil
}
