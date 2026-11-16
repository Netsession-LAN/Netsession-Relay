//go:build linux

package netif

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/vishvananda/netlink"

	"github.com/Netsession2024/Netsession-Relay/internal/compile"
	"github.com/Netsession2024/Netsession-Relay/internal/pkt"
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
	disableIPv6(name)
	return l, nil
}

// disableIPv6 turns IPv6 off on a relay VLAN interface (no link-local chatter into the game VLANs).
// Best effort: the sysctl may not exist in minimal containers.
func disableIPv6(name string) {
	_ = os.WriteFile("/proc/sys/net/ipv6/conf/"+name+"/disable_ipv6", []byte("1"), 0o644)
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
		if a.Alias != Alias || keep[a.Name] {
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

// SelfTest checks host settings the relay depends on and returns human-readable findings
// (empty = all fine). See docs/02-plan.md, 4.2.
func SelfTest() []string {
	var findings []string
	check := func(path, want, why string) {
		b, err := os.ReadFile(path)
		if err != nil {
			return
		}
		if got := strings.TrimSpace(string(b)); got != want {
			findings = append(findings, fmt.Sprintf("%s = %s, want %s (%s)", strings.TrimPrefix(path, "/proc/sys/"), got, want, why))
		}
	}
	check("/proc/sys/net/ipv4/ip_forward", "0", "the relay VM must not route between game VLANs")
	check("/proc/sys/net/ipv4/conf/all/arp_ignore", "1", "answer ARP only on the interface owning the address")
	check("/proc/sys/net/ipv4/conf/all/arp_announce", "2", "use the best local address in ARP requests")
	return findings
}
