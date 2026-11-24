package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

func TestDiscoveryPorts(t *testing.T) {
	tests := []struct {
		name  string
		specs []gamedb.Spec
		want  string
	}{
		{"none", nil, ""},
		{"broadcast", []gamedb.Spec{{Ports: "27015"}}, "27015"},
		{"multicast with group", []gamedb.Spec{{Ports: "4445"}, {Group: "239.255.255.250", Ports: "1900"}}, "4445 239.255.255.250:1900"},
	}
	for _, tt := range tests {
		if got := discoveryPorts(tt.specs); got != tt.want {
			t.Errorf("%s: discoveryPorts = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestVLANInterface(t *testing.T) {
	tests := []struct {
		v    store.VLANConfig
		want string
	}{
		{store.VLANConfig{VID: 101, Attach: "trunk", Parent: "trunk0", Device: "ignored"}, "trunk0.101"},
		{store.VLANConfig{VID: 101, Attach: "device", Device: "eth2"}, "eth2"},
	}
	for _, tt := range tests {
		if got := vlanInterface(tt.v); got != tt.want {
			t.Errorf("vlanInterface(%+v) = %q, want %q", tt.v, got, tt.want)
		}
	}
}

func TestDecodeImport(t *testing.T) {
	tests := []struct {
		name, yaml, wantErr string
	}{
		{"empty", "", "admin import: in.yaml: empty"},
		{"unknown field", "event: lan\nbogus: 1\n", "admin import: in.yaml: "},
		{"no VLANs", "event: lan\n", "admin import: no event or no VLANs in in.yaml"},
		{"no event", "vlans:\n  - vid: 101\n", "admin import: no event or no VLANs in in.yaml"},
		{"ok", "event: lan\nvlans:\n  - vid: 101\n", ""},
	}
	for _, tt := range tests {
		cfg, err := decodeImport([]byte(tt.yaml), "in.yaml")
		switch {
		case tt.wantErr == "" && err != nil:
			t.Errorf("%s: unexpected error %v", tt.name, err)
		case tt.wantErr == "" && (cfg.Event != "lan" || len(cfg.VLANs) != 1):
			t.Errorf("%s: decoded %+v", tt.name, cfg)
		case tt.wantErr != "" && (err == nil || !strings.HasPrefix(err.Error(), tt.wantErr)):
			t.Errorf("%s: error %v, want prefix %q", tt.name, err, tt.wantErr)
		}
	}
}

func TestPrintStatus(t *testing.T) {
	last := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		st        engineapi.Status
		want, not []string
	}{
		{"before the first snapshot", engineapi.Status{},
			[]string{"mode (none), sending false, standby false, snapshot -, 0 VLANs, 0 rules\n"}, []string{"host prepared"}},
		{"running", engineapi.Status{Mode: "active", SnapshotHash: "0123456789abcdef",
			VLANStats: []engineapi.VLANStat{{VID: 101, Name: "T1", Interface: "trunk0.101"}, {VID: 102, Name: "T2", Interface: "trunk0.102", In: 3, LastIn: last}},
			RuleStats: []engineapi.RuleStat{{ID: "quiet"}, {ID: "cs2", RX: 5, Relayed: 4}},
			Alarms:    []engineapi.Alarm{{Kind: "loop", Key: "101", Count: 2, Detail: "seen twice"}}},
			[]string{"snapshot 0123456789ab,", "vlan 101 T1 (trunk0.101): in 0, out 0, last in never\n",
				"vlan 102 T2 (trunk0.102): in 3, out 0, last in 2026-11-24T12:00:00Z\n", "rule cs2: rx 5, relayed 4\n",
				"ALARM loop 101 x2: seen twice\n", "host prepared\n"},
			[]string{"rule quiet"}},
		{"host issue", engineapi.Status{Mode: "observe", HostIssues: []engineapi.HostIssue{{Code: "sysctl", Detail: "rp_filter"}}},
			[]string{"HOST sysctl: rp_filter\n"}, []string{"host prepared"}},
	}
	for _, tt := range tests {
		var b bytes.Buffer
		printStatus(&b, tt.st)
		for _, w := range tt.want {
			if !strings.Contains(b.String(), w) {
				t.Errorf("%s: output lacks %q:\n%s", tt.name, w, b.String())
			}
		}
		for _, n := range tt.not {
			if strings.Contains(b.String(), n) {
				t.Errorf("%s: output has %q:\n%s", tt.name, n, b.String())
			}
		}
	}
}
