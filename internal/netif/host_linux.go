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
	nlErr := setDevconfNetlink(link.Attrs().Index)
	if nlErr == nil {
		return nil
	}
	if err := setDevconfProc(name); err != nil {
		return fmt.Errorf("netlink: %w; %w", nlErr, err)
	}
	return nil
}

// setDevconfNetlink sets relayDevconf on the interface with one RTM_SETLINK message.
func setDevconfNetlink(index int) error {
	req := nl.NewNetlinkRequest(unix.RTM_SETLINK, unix.NLM_F_ACK)
	msg := nl.NewIfInfomsg(unix.AF_UNSPEC)
	msg.Index = int32(index)
	req.AddData(msg)
	conf := nl.NewRtAttr(iflaInetConf, nil)
	for _, c := range relayDevconf {
		conf.AddRtAttr(int(c.id), nl.Uint32Attr(c.value))
	}
	af := nl.NewRtAttr(unix.AF_INET, conf.Serialize())
	req.AddData(nl.NewRtAttr(unix.IFLA_AF_SPEC, af.Serialize()))
	_, err := req.Execute(unix.NETLINK_ROUTE, 0)
	return err
}

// setDevconfProc sets relayDevconf through /proc/sys, writing only the values that differ.
func setDevconfProc(name string) error {
	var errs []error
	for _, c := range relayDevconf {
		p := "/proc/sys/net/ipv4/conf/" + name + "/" + c.file
		if b, err := os.ReadFile(p); err == nil && strings.TrimSpace(string(b)) == fmt.Sprint(c.value) {
			continue
		}
		if err := os.WriteFile(p, []byte(fmt.Sprint(c.value)), 0o644); err != nil { //nolint:gosec // G306: writes an existing /proc/sys setting; the mode is not used
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// DisableIPv6 keeps IPv6 off a relay interface (no router solicitations or link-local chatter into the game
// VLANs). The sysctl is preferred; where /proc/sys is read-only (containers) the interface gets no
// automatic IPv6 address and existing IPv6 addresses are removed, which silences it just the same.
func DisableIPv6(name string) error {
	done, err := disableIPv6Sysctl(name)
	if done || err != nil {
		return err
	}
	link, err := netlink.LinkByName(name)
	if err != nil {
		return err
	}
	if err := netlink.LinkSetIP6AddrGenMode(link, nl.IN6_ADDR_GEN_MODE_NONE); err != nil {
		return fmt.Errorf("address generation off: %w", err)
	}
	return removeIPv6Addrs(link)
}

// disableIPv6Sysctl switches IPv6 off through /proc/sys. done is true when IPv6 is off now or the host has no
// IPv6 at all; false with a nil error means the sysctl cannot be written.
func disableIPv6Sysctl(name string) (done bool, err error) {
	p := "/proc/sys/net/ipv6/conf/" + name + "/disable_ipv6"
	b, err := os.ReadFile(p)
	if err != nil {
		if _, e := os.Stat("/proc/sys/net/ipv6"); e != nil {
			return true, nil //nolint:nilerr // the host has no IPv6 at all: nothing to switch off
		}
		return false, err
	}
	return strings.TrimSpace(string(b)) == "1" || os.WriteFile(p, []byte("1"), 0o644) == nil, nil //nolint:gosec // G306: writes an existing /proc/sys setting; the mode is not used
}

func removeIPv6Addrs(link netlink.Link) error {
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
		out = append(out, hostIface(l, def[l.Attrs().Name]))
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func hostIface(l netlink.Link, defaultRoute bool) HostIface {
	a := l.Attrs()
	return HostIface{Name: a.Name, Up: a.Flags&net.FlagUp != 0, Carrier: hasCarrier(a), Loopback: a.Flags&net.FlagLoopback != 0,
		DefaultRoute: defaultRoute, Relay: a.Alias == Alias, Kind: l.Type(), Addrs: globalAddrs(l)}
}

// hasCarrier reports a link that is up at the physical layer; drivers without operstate report "unknown" and
// the running flag.
func hasCarrier(a *netlink.LinkAttrs) bool {
	return a.OperState == netlink.OperUp || a.OperState == netlink.OperUnknown && a.Flags&net.FlagRunning != 0
}

// globalAddrs lists the addresses of the interface without the link-local ones.
func globalAddrs(l netlink.Link) []string {
	addrs, err := netlink.AddrList(l, netlink.FAMILY_ALL)
	if err != nil {
		return nil
	}
	var out []string
	for _, ad := range addrs {
		if !ad.IP.IsLinkLocalUnicast() {
			out = append(out, ad.IPNet.String())
		}
	}
	return out
}
