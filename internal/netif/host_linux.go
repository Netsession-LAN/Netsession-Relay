//go:build linux

package netif

import (
	"errors"
	"fmt"
	"net"
	"os"
	"sort"
	"strings"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
	"golang.org/x/sys/unix"
)

// IPv4 per-interface settings (enum IPV4_DEVCONF_* of the kernel, linux/ip.h) and the attribute that carries
// them in a link message (IFLA_INET_CONF inside IFLA_AF_SPEC/AF_INET).
const (
	devconfForwarding  = 1
	devconfRPFilter    = 8
	devconfARPAnnounce = 18
	devconfARPIgnore   = 19
	iflaInetConf       = 1
)

// relayDevconf is what every relay interface gets: the host never routes what arrives there, answers ARP only
// for the address of that interface, announces the matching address, and does not drop frames whose source
// lies in another relayed network.
var relayDevconf = []struct {
	id    uint16
	file  string
	value uint32
}{
	{devconfForwarding, "forwarding", 0},
	{devconfRPFilter, "rp_filter", 0},
	{devconfARPIgnore, "arp_ignore", 1},
	{devconfARPAnnounce, "arp_announce", 2},
}

// HardenLink applies the IPv4 settings of a relay interface. It uses netlink, which works in a container with
// CAP_NET_ADMIN where /proc/sys is read-only; if the kernel refuses that, it falls back to /proc/sys.
func HardenLink(name string) error {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return err
	}
	req := nl.NewNetlinkRequest(unix.RTM_SETLINK, unix.NLM_F_ACK)
	msg := nl.NewIfInfomsg(unix.AF_UNSPEC)
	msg.Index = int32(link.Attrs().Index)
	req.AddData(msg)
	conf := nl.NewRtAttr(iflaInetConf, nil)
	for _, c := range relayDevconf {
		conf.AddRtAttr(int(c.id), nl.Uint32Attr(c.value))
	}
	af := nl.NewRtAttr(unix.AF_INET, conf.Serialize())
	req.AddData(nl.NewRtAttr(unix.IFLA_AF_SPEC, af.Serialize()))
	_, nlErr := req.Execute(unix.NETLINK_ROUTE, 0)
	if nlErr == nil {
		return nil
	}
	var errs []error
	for _, c := range relayDevconf {
		p := "/proc/sys/net/ipv4/conf/" + name + "/" + c.file
		if b, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(b)) == fmt.Sprint(c.value) {
			continue
		}
		if err := os.WriteFile(p, []byte(fmt.Sprint(c.value)), 0o644); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("netlink: %w; %w", nlErr, errors.Join(errs...))
	}
	return nil
}

// DisableIPv6 keeps IPv6 off a relay interface (no router solicitations or link-local chatter into the game
// VLANs). The sysctl is preferred; where /proc/sys is read-only (containers) the interface gets no
// automatic IPv6 address and existing IPv6 addresses are removed, which silences it just the same.
func DisableIPv6(name string) error {
	p := "/proc/sys/net/ipv6/conf/" + name + "/disable_ipv6"
	b, err := os.ReadFile(p)
	if err != nil {
		if _, e := os.Stat("/proc/sys/net/ipv6"); e != nil {
			return nil // the host has no IPv6 at all
		}
		return err
	}
	if strings.TrimSpace(string(b)) == "1" || os.WriteFile(p, []byte("1"), 0o644) == nil {
		return nil
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		return err
	}
	if err := netlink.LinkSetIP6AddrGenMode(link, nl.IN6_ADDR_GEN_MODE_NONE); err != nil {
		return fmt.Errorf("address generation off: %w", err)
	}
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V6)
	if err != nil {
		return err
	}
	var errs []error
	for i := range addrs {
		if err := netlink.AddrDel(link, &addrs[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// HasIPv4 reports whether the interface has an IPv4 address of its own.
func HasIPv4(name string) bool {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return false
	}
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	return err == nil && len(addrs) > 0
}

// Interfaces lists the network interfaces of the host for the setup page and the listener guard.
func Interfaces() ([]HostIface, error) {
	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}
	def, _ := DefaultRouteLinks()
	var out []HostIface
	for _, l := range links {
		a := l.Attrs()
		h := HostIface{Name: a.Name, Up: a.Flags&net.FlagUp != 0, Carrier: a.OperState == netlink.OperUp || a.OperState == netlink.OperUnknown && a.Flags&net.FlagRunning != 0,
			Loopback: a.Flags&net.FlagLoopback != 0, DefaultRoute: def[a.Name], Relay: a.Alias == Alias, Kind: l.Type()}
		if addrs, err := netlink.AddrList(l, netlink.FAMILY_ALL); err == nil {
			for _, ad := range addrs {
				if ad.IP.IsLinkLocalUnicast() {
					continue
				}
				h.Addrs = append(h.Addrs, ad.IPNet.String())
			}
		}
		out = append(out, h)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
