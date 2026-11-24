package web

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
)

func TestVLANFormParse(t *testing.T) {
	ok := vlanForm{VID: "11", Name: "Tisch 1", Group: compile.GroupTables, CIDR: "10.10.1.7/24", RelayIP: "10.10.1.250",
		Attach: compile.AttachTrunk, Parent: "ens19", Rate: "500", Enable: true}
	v, msg := ok.parse(fmt.Sprintf)
	if msg != "" || v.VID != 11 || v.CIDR != "10.10.1.0/24" || v.RelayIP != "10.10.1.250" || v.Parent != "ens19" ||
		v.Device != "" || v.RatePPS != 500 || v.Burst != 0 || !v.Enabled {
		t.Fatalf("valid form: %+v %q", v, msg)
	}
	for _, c := range []struct {
		change func(*vlanForm)
		want   string // part of the error text
	}{
		{func(f *vlanForm) { f.VID = "4095" }, "VLAN-ID"},
		{func(f *vlanForm) { f.VID = "elf" }, "VLAN-ID"},
		{func(f *vlanForm) { f.Name = "" }, "Namen"},
		{func(f *vlanForm) { f.Name = strings.Repeat("n", 41) }, "zu lang"},
		{func(f *vlanForm) { f.CIDR = "fd00::/64" }, "IPv4-Netz"},
		{func(f *vlanForm) { f.CIDR = "10.10.1.0/31" }, "zu klein"},
		{func(f *vlanForm) { f.RelayIP = "relay" }, "keine IPv4-Adresse"},
		{func(f *vlanForm) { f.RelayIP = "10.10.2.250" }, "liegt nicht in 10.10.1.0/24"},
		{func(f *vlanForm) { f.RelayIP = "10.10.1.0" }, "liegt nicht in"},
		{func(f *vlanForm) { f.Parent = "" }, "Trunk-Interface"},
		{func(f *vlanForm) { f.Attach = compile.AttachDevice }, "Namen des Interfaces"},
		{func(f *vlanForm) { f.Attach = "" }, "wie das Netz anliegt"},
		{func(f *vlanForm) { f.Rate = "-1" }, "Grenze"},
		{func(f *vlanForm) { f.Burst = "1000001" }, "Grenze"},
	} {
		f := ok
		c.change(&f)
		if _, msg := f.parse(fmt.Sprintf); !strings.Contains(msg, c.want) {
			t.Errorf("%+v: %q, want %q", f, msg, c.want)
		}
	}
	dev := ok
	dev.Attach, dev.Parent, dev.Device = compile.AttachDevice, "", "eth1"
	if v, msg := dev.parse(fmt.Sprintf); msg != "" || v.Device != "eth1" || v.Parent != "" {
		t.Errorf("own interface: %+v %q", v, msg)
	}
}
