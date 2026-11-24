//go:build linux

package netif

import (
	"errors"
	"fmt"
	"net"

	"github.com/vishvananda/netlink"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

// Ensure creates (trunk) or adopts (device) the interface for v, brings it up and assigns the relay IP.
// It is idempotent.
func Ensure(v compile.VLAN) (Link, error) {
	name := LinkName(v)
	var link netlink.Link
	var err error
	switch v.Attach {
	case compile.AttachTrunk:
		link, err = ensureVLAN(v.Parent, name, v.VID)
	case compile.AttachDevice:
		link, err = netlink.LinkByName(name)
	default:
		err = fmt.Errorf("attach %q", v.Attach)
	}
	if err != nil {
		return Link{}, fmt.Errorf("VLAN %d (%s): %w", v.VID, name, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return Link{}, fmt.Errorf("%s: set up: %w", name, err)
	}
	if v.RelayIP.IsValid() {
		addr := &netlink.Addr{IPNet: &net.IPNet{IP: v.RelayIP.AsSlice(), Mask: net.CIDRMask(v.CIDR.Bits(), 32)}}
		if err := netlink.AddrReplace(link, addr); err != nil {
			return Link{}, fmt.Errorf("%s: address %s: %w", name, addr, err)
		}
	}
	link, err = netlink.LinkByIndex(link.Attrs().Index) // refresh (MAC, state)
	if err != nil {
		return Link{}, err
	}
	hw := link.Attrs().HardwareAddr
	if len(hw) != 6 {
		return Link{}, fmt.Errorf("%s: no Ethernet MAC", name)
	}
	return Link{Name: name, Index: link.Attrs().Index, MAC: pkt.MAC(hw)}, nil
}

func ensureVLAN(parentName, name string, vid int) (netlink.Link, error) {
	parent, err := netlink.LinkByName(parentName)
	if err != nil {
		return nil, fmt.Errorf("trunk interface %q: %w", parentName, err)
	}
	if err := netlink.LinkSetUp(parent); err != nil {
		return nil, fmt.Errorf("trunk %s: set up: %w", parentName, err)
	}
	if l, err := netlink.LinkByName(name); err == nil {
		vl, ok := l.(*netlink.Vlan)
		if !ok || vl.VlanId != vid || vl.ParentIndex != parent.Attrs().Index {
			return nil, fmt.Errorf("interface %s exists but is not VLAN %d on %s", name, vid, parentName)
		}
		return l, nil
	}
	vl := &netlink.Vlan{LinkAttrs: netlink.LinkAttrs{Name: name, ParentIndex: parent.Attrs().Index}, VlanId: vid}
	if err := netlink.LinkAdd(vl); err != nil {
		return nil, fmt.Errorf("create: %w", err)
	}
	l, err := netlink.LinkByName(name)
	if err != nil {
		return nil, err
	}
	if err := netlink.LinkSetAlias(l, Alias); err != nil {
		return nil, fmt.Errorf("alias: %w", err)
	}
	// before the link goes up, so it never sends a router solicitation; hostprep reports if it stays on
	_ = DisableIPv6(name)
	return l, nil
}

// Cleanup deletes engine-created links (alias "nsrelay") whose name is not in keep. It returns the names
// of deleted links.
func Cleanup(keep map[string]bool) ([]string, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}
	var deleted []string
	var errs []error
	for _, l := range links {
		a := l.Attrs()
		if !stale(a.Name, a.Alias, keep) {
			continue
		}
		if err := netlink.LinkDel(l); err != nil {
			errs = append(errs, fmt.Errorf("delete %s: %w", a.Name, err))
			continue
		}
		deleted = append(deleted, a.Name)
	}
	return deleted, errors.Join(errs...)
}

// DefaultRouteLinks returns the names of the interfaces that carry a default route (IPv4 or IPv6, main table):
// the management side of the host, which the engine does not use unless it is told to (engine.CheckIfaces).
func DefaultRouteLinks() (map[string]bool, error) {
	routes, err := netlink.RouteList(nil, netlink.FAMILY_ALL)
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	add := func(index int) {
		if index <= 0 {
			return
		}
		if l, err := netlink.LinkByIndex(index); err == nil {
			out[l.Attrs().Name] = true
		}
	}
	for _, r := range routes {
		if r.Dst != nil {
			if ones, _ := r.Dst.Mask.Size(); ones != 0 {
				continue
			}
		}
		add(r.LinkIndex)
		for _, nh := range r.MultiPath {
			add(nh.LinkIndex)
		}
	}
	return out, nil
}
