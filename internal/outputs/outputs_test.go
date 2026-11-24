package outputs

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/engineapi"
	"github.com/Netsession-LAN/Netsession-Relay/internal/monitor"
	"github.com/Netsession-LAN/Netsession-Relay/internal/notify"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

func newService(t *testing.T, mgmt netip.Addr) (*Service, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "relay.db"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(Options{Store: st, Version: "test",
		Report:   func(context.Context) (monitor.Report, []engineapi.RuleStat) { return monitor.Report{}, nil },
		MgmtAddr: func() netip.Addr { return mgmt }})
	t.Cleanup(func() {
		s.Close()
		st.Close()
	})
	return s, st
}

func TestBindFrom(t *testing.T) {
	for in, want := range map[string]string{"192.0.2.55:8080": "192.0.2.55", "127.0.0.1:8080": "", "0.0.0.0:8080": "", ":8080": "", "[2001:db8::1]:80": "2001:db8::1"} {
		got := BindFrom(in)
		if (want == "" && got.IsValid()) || (want != "" && got.String() != want) {
			t.Errorf("BindFrom(%q) = %v, want %q", in, got, want)
		}
	}
}

func TestHostName(t *testing.T) {
	ctx := context.Background()
	s, st := newService(t, netip.Addr{})
	if s.HostName(ctx) == "" {
		t.Error("no host name without the setting (want the name of the machine)")
	}
	if err := st.SetSetting(ctx, SettingHostName, "relay01"); err != nil {
		t.Fatal(err)
	}
	if h := s.HostName(ctx); h != "relay01" {
		t.Errorf("host name %q, want the setting", h)
	}
}

// The ids of an installation are created once and stay; the SNMPv3 boot counter grows with every start.
func TestInstallationIDs(t *testing.T) {
	ctx := context.Background()
	s, st := newService(t, netip.Addr{})
	id, eid := s.instanceID(ctx), ""
	if id == "" || s.instanceID(ctx) != id {
		t.Errorf("instance id %q is not stable", id)
	}
	eid, boots := s.snmpEngine(ctx)
	if again, _ := s.snmpEngine(ctx); eid == "" || again != eid || boots != 1 {
		t.Errorf("SNMP engine id %q / %q, boots %d (want a stable id and 1 boot)", eid, again, boots)
	}
	second := New(Options{Store: st, Version: "test", Report: s.report})
	defer second.Close()
	if again, boots := second.snmpEngine(ctx); again != eid || boots != 2 {
		t.Errorf("after a second start: engine id %q (was %q), boots %d", again, eid, boots)
	}
}

func TestAgentSettings(t *testing.T) {
	ctx := context.Background()
	s, st := newService(t, netip.Addr{})
	a, sec := s.AgentSettings(ctx)
	if a.Enabled || a.Port != 161 || a.AuthProto != "sha256" || a.PrivProto != "aes128" || sec != (AgentSecret{}) {
		t.Errorf("defaults: %+v %+v", a, sec)
	}
	want := AgentSettings{Enabled: true, Port: 1161, V2c: true, User: "monitor", AuthProto: "sha512", PrivProto: "aes256",
		Allow: []string{"192.0.2.0/24"}, Contact: "crew", Location: "hall 1"}
	wantSec := AgentSecret{Community: "c0mmunity", AuthPass: "auth-pass-1", PrivPass: "priv-pass-1"}
	if err := s.SaveAgentSettings(ctx, want, wantSec); err != nil {
		t.Fatal(err)
	}
	a, sec = s.AgentSettings(ctx)
	if a.Port != want.Port || !a.Enabled || a.User != "monitor" || len(a.Allow) != 1 || a.Location != "hall 1" || sec != wantSec {
		t.Errorf("after saving: %+v %+v", a, sec)
	}
	// the options never contain a secret (they go into the audit log)
	if raw, _ := st.Setting(ctx, settingSNMPAgent, ""); strings.Contains(raw, "c0mmunity") || strings.Contains(raw, "pass-1") {
		t.Errorf("secret in the options: %s", raw)
	}
	// a stored value that is not JSON gives the defaults again instead of half a configuration
	if err := st.SetSetting(ctx, settingSNMPAgent, "{broken"); err != nil {
		t.Fatal(err)
	}
	if a, _ := s.AgentSettings(ctx); a.Enabled || a.Port != 161 {
		t.Errorf("broken setting: %+v, want the defaults", a)
	}
}

// The agent binds the override, else the listen address of the web part, else the management address - never
// every address.
func TestAgentHost(t *testing.T) {
	ctx := context.Background()
	mgmt := netip.MustParseAddr("192.0.2.10")
	s, _ := newService(t, mgmt)
	if h := s.AgentHost(); h != mgmt {
		t.Errorf("without anything set: %v, want the management address", h)
	}
	s.SetBindAddr(ctx, ":8080")
	if h := s.AgentHost(); h != mgmt || s.BindAddr().IsValid() {
		t.Errorf("web part on every address: agent %v, bind %v", h, s.BindAddr())
	}
	s.SetBindAddr(ctx, "192.0.2.20:8080")
	if h := s.AgentHost(); h.String() != "192.0.2.20" || s.BindAddr() != h {
		t.Errorf("web part on one address: agent %v, bind %v", h, s.BindAddr())
	}
	s.SetSNMPAddr("192.0.2.30")
	if h := s.AgentHost(); h.String() != "192.0.2.30" {
		t.Errorf("with the override: %v", h)
	}
	s.SetSNMPAddr("0.0.0.0")
	if h := s.AgentHost(); h.String() != "192.0.2.20" {
		t.Errorf("override 0.0.0.0 must not count: %v", h)
	}
}

func TestAgentLifecycle(t *testing.T) {
	ctx := context.Background()
	s, _ := newService(t, netip.Addr{}) // no management address known
	if running, _, err := s.AgentStatus(ctx); running || err != nil {
		t.Errorf("switched off: running %v, %v", running, err)
	}
	// a free UDP port on the loopback
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	pc.Close()
	a := AgentSettings{Enabled: true, Port: port, V2c: true}
	if err := s.SaveAgentSettings(ctx, a, AgentSecret{Community: "public-test"}); err != nil {
		t.Fatal(err)
	}
	s.RestartAgent(ctx)
	if running, _, err := s.AgentStatus(ctx); running || !errors.Is(err, ErrNoAgentAddr) {
		t.Errorf("no address to bind: running %v, %v; want ErrNoAgentAddr", running, err)
	}
	s.SetSNMPAddr("127.0.0.1")
	s.RestartAgent(ctx)
	running, listen, err := s.AgentStatus(ctx)
	if !running || err != nil || listen != net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) {
		t.Fatalf("agent: running %v on %q, %v", running, listen, err)
	}
	a.Enabled = false
	if err := s.SaveAgentSettings(ctx, a, AgentSecret{Community: "public-test"}); err != nil {
		t.Fatal(err)
	}
	s.RestartAgent(ctx)
	if running, _, err := s.AgentStatus(ctx); running || err != nil {
		t.Errorf("switched off again: running %v, %v", running, err)
	}
}

// Targets come from the database; Reload picks up changes, Test reaches a target, Stats counts them.
func TestTargets(t *testing.T) {
	ctx := context.Background()
	s, st := newService(t, netip.Addr{})
	if total, failing, _ := s.Stats(); total != 0 || failing != 0 {
		t.Errorf("no targets: %d/%d", total, failing)
	}
	sink, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	id, err := st.SaveTarget(ctx, "test", store.Target{Kind: store.TargetSyslog, Name: "A", Enabled: true, Endpoint: sink.LocalAddr().String(),
		Events: []string{"alarm"}, Config: map[string]any{"transport": "udp", "format": "rfc5424", "facility": "local0"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, SettingHostName, "relay01"); err != nil {
		t.Fatal(err)
	}
	s.Reload(ctx)
	if total, _, _ := s.Stats(); total != 1 || len(s.Status()) != 1 {
		t.Errorf("after the reload: %d targets, status %+v", total, s.Status())
	}
	if err := s.Test(ctx, id, notify.Event{Type: notify.TypeTest, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	_ = sink.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, _, err := sink.ReadFrom(buf)
	if err != nil || !strings.Contains(string(buf[:n]), "hello") || !strings.Contains(string(buf[:n]), "relay01") {
		t.Errorf("test message: %q, %v", buf[:n], err)
	}
	if err := s.Test(ctx, id+99, notify.Event{Type: notify.TypeTest}); err == nil {
		t.Error("test to a target that does not exist succeeded")
	}
}

// The source address is built into every sender: a new listen address must reach the running senders although
// no target changed, and a reload afterwards must leave them alone.
func TestBindAddrReachesSenders(t *testing.T) {
	ctx := context.Background()
	s, st := newService(t, netip.Addr{})
	sink, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	id, err := st.SaveTarget(ctx, "test", store.Target{Kind: store.TargetSyslog, Name: "A", Enabled: true, Endpoint: sink.LocalAddr().String(),
		Events: []string{"alarm"}, Config: map[string]any{"transport": "udp", "format": "rfc5424", "facility": "local0"}})
	if err != nil {
		t.Fatal(err)
	}
	s.Reload(ctx)
	sent := func() uint64 {
		t.Helper()
		for _, x := range s.Status() {
			if x.ID == id {
				return x.Sent
			}
		}
		t.Fatal("target not in the status")
		return 0
	}
	ev := notify.Event{Type: notify.TypeTest, Text: "hello"}
	if err := s.Test(ctx, id, ev); err != nil || sent() != 1 {
		t.Fatalf("before any bind address: %v, sent %d", err, sent())
	}
	// 192.0.2.20 (TEST-NET-1) is no address of this machine: a sender that binds it cannot send
	s.SetBindAddr(ctx, "192.0.2.20:8080")
	if err := s.Test(ctx, id, ev); err == nil {
		t.Fatal("the sender still sends from any address: the bind address did not reach it")
	}
	if n := sent(); n != 0 {
		t.Errorf("sent %d after the change, want a new sender that starts at 0", n)
	}
	s.SetBindAddr(ctx, ":8080")
	if err := s.Test(ctx, id, ev); err != nil || sent() != 1 {
		t.Fatalf("back to any address: %v, sent %d", err, sent())
	}
	// nothing changed: neither a reload nor the same listen address again may restart the sender
	s.Reload(ctx)
	s.SetBindAddr(ctx, ":8080")
	if n := sent(); n != 1 {
		t.Errorf("sent %d after a reload without changes, want 1 (sender kept)", n)
	}
}

func TestConfigHelpers(t *testing.T) {
	m := map[string]any{"s": "x", "empty": "", "b": true, "n": 3.0}
	if ConfigString(m, "s", "d") != "x" || ConfigString(m, "empty", "d") != "d" || ConfigString(m, "missing", "d") != "d" || ConfigString(m, "n", "d") != "d" {
		t.Error("ConfigString")
	}
	if !ConfigBool(m, "b") || ConfigBool(m, "missing") || ConfigBool(m, "s") {
		t.Error("ConfigBool")
	}
	if ConfigString(nil, "s", "d") != "d" || ConfigBool(nil, "b") {
		t.Error("nil map")
	}
}
