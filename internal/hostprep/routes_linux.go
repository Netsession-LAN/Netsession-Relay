//go:build linux

package hostprep

import (
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
)

// Helpers for IPv4 routes as netlink reports them.

// defaultRoute is the IPv4 default route of the main table with a gateway (lowest metric), or nil.
func defaultRoute() *netlink.Route {
	rs, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return nil
	}
	var best *netlink.Route
	for i := range rs {
		r := &rs[i]
		if isGatewayDefault(r) && (best == nil || r.Priority < best.Priority) {
			best = r
		}
	}
	return best
}

// isGatewayDefault reports a default route through a gateway on a device.
func isGatewayDefault(r *netlink.Route) bool {
	return isDefault(r.Dst) && r.Gw != nil && r.LinkIndex > 0
}

// prefixOf converts an IPv4 network; false for anything else.
func prefixOf(n *net.IPNet) (netip.Prefix, bool) {
	a, ok := netip.AddrFromSlice(n.IP.To4())
	if !ok {
		return netip.Prefix{}, false
	}
	ones, _ := n.Mask.Size()
	return netip.PrefixFrom(a, ones), true
}

// isDefault reports whether a route destination means the default route (none, or a /0).
func isDefault(n *net.IPNet) bool {
	if n == nil {
		return true
	}
	ones, _ := n.Mask.Size()
	return ones == 0
}

// hasRoute reports whether list has a route to the same destination over the same link and gateway.
func hasRoute(list []netlink.Route, r netlink.Route) bool {
	for _, w := range list {
		if sameNextHop(w, r) && (isDefault(w.Dst) || w.Dst.String() == r.Dst.String()) {
			return true
		}
	}
	return false
}

// sameNextHop reports routes over the same link and gateway that are both default routes or both not.
func sameNextHop(a, b netlink.Route) bool {
	return a.LinkIndex == b.LinkIndex && a.Gw.Equal(b.Gw) && isDefault(a.Dst) == isDefault(b.Dst)
}
