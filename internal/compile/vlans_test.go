package compile

import (
	"errors"
	"fmt"
	"net/netip"
	"testing"
)

// checkVLANs reports every problem with its exact wording, in input order, overlaps last.
func TestCheckVLANsMessages(t *testing.T) {
	pfx, addr := netip.MustParsePrefix, netip.MustParseAddr
	in := []VLAN{
		{VID: 0, Name: "zero", Enabled: true, Attach: "x"},
		{VID: 0, Name: "again", Group: "g", Enabled: false},
		{VID: 20, Name: "tiny", Group: "g", Enabled: true, CIDR: pfx("10.0.0.1/31"), RelayIP: addr("10.0.0.0"), Attach: AttachTrunk},
		{VID: 30, Name: "dev", Group: "h", Enabled: true, CIDR: pfx("10.1.0.0/16"), RelayIP: addr("10.2.0.1"), Attach: AttachDevice, Parent: "eth0"},
		{VID: 10, Name: "wide", Group: "h", Enabled: true, CIDR: pfx("10.1.2.9/24"), RelayIP: addr("10.1.2.1"), Attach: AttachTrunk, Parent: "eth0", Device: "x"},
		{VID: 5000, Name: "off", Group: "off", Enabled: false},
	}
	out, groups, errs := checkVLANs(in)
	if got := errors.Join(errs...).Error(); got != checkVLANsWant {
		t.Errorf("checkVLANs:\n%s\nwant:\n%s", got, checkVLANsWant)
	}
	var vids []int
	for _, v := range out {
		vids = append(vids, v.VID)
	}
	if got := fmt.Sprint(vids); got != "[0 10 20 30]" {
		t.Errorf("enabled VLANs = %s", got)
	}
	if out[1].CIDR != pfx("10.1.2.0/24") || out[1].Device != "" || out[3].Parent != "" {
		t.Errorf("VLANs not normalised: %+v", out)
	}
	if groups[""] != 1 || groups["g"] != 1 || groups["h"] != 2 || groups["off"] != 0 || len(groups) != 4 {
		t.Errorf("groups = %v", groups)
	}
}

const checkVLANsWant = `VLAN 0 (zero): VID out of range
VLAN 0 (zero): no group
VLAN 0 (zero): IPv4 network missing
VLAN 0 (zero): relay IP invalid IP not in invalid Prefix
VLAN 0 (zero): attach "x" invalid
VLAN 0 (again): VID out of range
VLAN 0 (again): VID used twice
VLAN 20 (tiny): network 10.0.0.1/31 too small
VLAN 20 (tiny): trunk attach needs a parent interface
VLAN 30 (dev): relay IP 10.2.0.1 not in 10.1.0.0/16
VLAN 30 (dev): device attach needs a device name
VLAN 5000 (off): VID out of range
VLAN 10 and 30: networks overlap: 10.1.2.0/24 and 10.1.0.0/16`
