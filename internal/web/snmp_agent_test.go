package web

import (
	"slices"
	"testing"
)

func TestParseAgentAllow(t *testing.T) {
	allow, bad := parseAgentAllow("10.0.0.5, 10.1.0.0/16\n192.168.1.0/24 fd00::1")
	if want := []string{"10.0.0.5/32", "10.1.0.0/16", "192.168.1.0/24", "fd00::1/128"}; bad != "" || !slices.Equal(allow, want) {
		t.Errorf("allow %v bad %q, want %v", allow, bad, want)
	}
	if allow, bad := parseAgentAllow(""); allow != nil || bad != "" {
		t.Errorf("empty: %v %q", allow, bad)
	}
	if allow, bad := parseAgentAllow("10.0.0.5, zabbix, 10.0.0.6"); allow != nil || bad != "zabbix" {
		t.Errorf("bad entry: %v %q", allow, bad)
	}
}
