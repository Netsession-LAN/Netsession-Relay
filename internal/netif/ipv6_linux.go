//go:build linux

package netif

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netlink/nl"
)

// Keeping IPv6 off a relay interface: no router solicitations or link-local chatter into the game VLANs.

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
