package store

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
)

// seeded returns a store with the game database synced in.
func seeded(t *testing.T) *Store {
	t.Helper()
	s := open(t)
	seed, err := gamedb.Seed()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncSeed(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	return s
}

func testConfig() Config {
	return Config{Version: 1, Event: "Test LAN", Mode: compile.ModeObserve,
		VLANs: []VLANConfig{
			{VID: 1100, Name: "Server", Group: "gameserver", CIDR: "10.10.0.0/24", RelayIP: "10.10.0.55", Attach: "trunk", Parent: "eth1", Enabled: true},
			{VID: 1101, Name: "Tisch 1", Group: "tische", CIDR: "10.10.1.0/24", RelayIP: "10.10.1.55", Attach: "trunk", Parent: "eth1", Enabled: true, RatePPS: 200, Burst: 400},
			{VID: 1102, Name: "Tisch 2", Group: "tische", CIDR: "10.10.2.0/24", RelayIP: "10.10.2.55", Attach: "device", Device: "eth2"},
		},
		Profiles: []ProfileConfig{
			{Name: "nur-server", Edges: []compile.Edge{{Src: "tische", Dst: "gameserver"}}},
			{Name: "spiele", Default: true, Edges: []compile.Edge{{Src: "gameserver", Dst: "tische"}, {Src: "tische", Dst: "gameserver"}, {Src: "tische", Dst: "tische"}}},
		},
		Games: []EventGameConfig{
			{ID: "flatout2"},
			{ID: "mdns", FlowProfile: "nur-server", Sources: []string{"10.10.1.20"}, Off: true},
			{ID: "source_engine", Override: compile.Override{SourceMode: gamedb.SourceRelay}},
		}}
}

// What is imported comes back unchanged from the export, and the compiler input is built from exactly that.
func TestConfigRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := seeded(t)
	if _, err := s.ExportConfig(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("export without an event: %v, want ErrNotFound", err)
	}
	if _, err := s.BuildInput(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("compiler input without an event: %v, want ErrNotFound", err)
	}
	want := testConfig()
	if err := s.ImportConfig(ctx, "test", want); err != nil {
		t.Fatal(err)
	}
	got, err := s.ExportConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("export differs from the import:\n got %+v\nwant %+v", got, want)
	}
	if ev, err := s.ActiveEvent(ctx); err != nil || ev != "Test LAN" {
		t.Errorf("active event %q, %v", ev, err)
	}
	if log, _ := s.AuditLog(ctx, 1); len(log) != 1 || log[0].Action != "config.import" || log[0].User != "test" {
		t.Errorf("import not in the audit log: %+v", log)
	}

	in, err := s.BuildInput(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if in.Mode != compile.ModeObserve || in.DefaultProfile != "spiele" || len(in.Profiles) != 2 || !in.Probe {
		t.Errorf("compiler input: mode %q, default profile %q, %d profiles, probe %v", in.Mode, in.DefaultProfile, len(in.Profiles), in.Probe)
	}
	if len(in.VLANs) != 3 || in.VLANs[1].CIDR != netip.MustParsePrefix("10.10.1.0/24") || in.VLANs[1].RelayIP != netip.MustParseAddr("10.10.1.55") ||
		in.VLANs[1].RatePPS != 200 || in.VLANs[2].Enabled || in.VLANs[2].Device != "eth2" {
		t.Errorf("networks of the compiler input: %+v", in.VLANs)
	}
	// the switched-off game keeps its settings in the export but is not compiled
	if len(in.Games) != 2 || in.Games[0].GameID != "flatout2" || in.Games[1].GameID != "source_engine" || in.Games[1].Override.SourceMode != gamedb.SourceRelay {
		t.Errorf("games of the compiler input: %+v", in.Games)
	}
	if snap, _, err := compile.Compile(in); err != nil || len(snap.VLANs) != 2 || len(snap.Rules) == 0 {
		t.Errorf("the stored configuration does not compile: %v", err)
	}

	// the server probe setting reaches the compiler input
	if err := s.SetSetting(ctx, SettingProbeServers, "0"); err != nil {
		t.Fatal(err)
	}
	if in, err := s.BuildInput(ctx); err != nil || in.Probe {
		t.Errorf("probe switched off: Probe=%v, %v", in.Probe, err)
	}

	// a second import replaces everything, also for another event
	next := Config{Version: 1, Event: "Next LAN", Mode: compile.ModeActive,
		VLANs:    []VLANConfig{{VID: 20, Name: "Halle", Group: "tische", CIDR: "192.0.2.0/24", RelayIP: "192.0.2.5", Attach: "device", Device: "eth3", Enabled: true}},
		Profiles: []ProfileConfig{{Name: "alle", Default: true, Edges: []compile.Edge{{Src: "tische", Dst: "tische"}}}}}
	if err := s.ImportConfig(ctx, "test", next); err != nil {
		t.Fatal(err)
	}
	if got, err := s.ExportConfig(ctx); err != nil || !reflect.DeepEqual(got, next) {
		t.Errorf("after the second import: %+v, %v", got, err)
	}
}

// A configuration the database refuses changes nothing: the import is one transaction.
func TestImportConfigRejects(t *testing.T) {
	ctx := context.Background()
	s := seeded(t)
	good := testConfig()
	if err := s.ImportConfig(ctx, "test", good); err != nil {
		t.Fatal(err)
	}
	for name, tt := range map[string]struct {
		change func(*Config)
		want   string // part of the error
	}{
		"unknown game":  {func(c *Config) { c.Games = append(c.Games, EventGameConfig{ID: "no_such_game"}) }, `game "no_such_game"`},
		"game twice":    {func(c *Config) { c.Games = append(c.Games, EventGameConfig{ID: "flatout2"}) }, `game "flatout2"`},
		"bad source":    {func(c *Config) { c.Games[0].Sources = []string{"not-an-address"} }, "sources"},
		"VLAN ID twice": {func(c *Config) { c.VLANs[2].VID = c.VLANs[1].VID }, "VLAN 1101"},
		"VLAN ID range": {func(c *Config) { c.VLANs[0].VID = 4095 }, "VLAN 4095"},
		"attach":        {func(c *Config) { c.VLANs[0].Attach = "bridge" }, "VLAN 1100"},
		"profile twice": {func(c *Config) { c.Profiles = append(c.Profiles, ProfileConfig{Name: "spiele"}) }, "flow_profile"},
	} {
		bad := testConfig()
		tt.change(&bad)
		err := s.ImportConfig(ctx, "test", bad)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error %v, want one that names %q", name, err, tt.want)
		}
		if got, err := s.ExportConfig(ctx); err != nil || !reflect.DeepEqual(got, good) {
			t.Errorf("%s: the refused import changed the configuration: %+v, %v", name, got, err)
		}
	}
}

func TestConfigInputRejects(t *testing.T) {
	db, err := gamedb.Seed()
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Config){
		"network":    func(c *Config) { c.VLANs[0].CIDR = "10.10.0.0/33" },
		"relay IP":   func(c *Config) { c.VLANs[1].RelayIP = "10.10.1" },
		"bad source": func(c *Config) { c.Games[0].Sources = []string{"10.10.1.0/40"} },
	} {
		c := testConfig()
		change(&c)
		if _, err := ConfigInput(c, db, true); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// a network without address and relay IP yet (the form is saved step by step) is passed on as it is
	c := testConfig()
	c.VLANs[2].CIDR, c.VLANs[2].RelayIP = "", ""
	if in, err := ConfigInput(c, db, false); err != nil || in.VLANs[2].CIDR.IsValid() || in.Probe {
		t.Errorf("network without address: %+v, %v", in.VLANs[2], err)
	}
}

func TestSetMode(t *testing.T) {
	ctx := context.Background()
	s := seeded(t)
	if err := s.SetMode(ctx, "test", "turbo"); err == nil {
		t.Error("mode turbo accepted")
	}
	if err := s.SetMode(ctx, "test", compile.ModePaused); err != nil {
		t.Fatal(err)
	}
	if m, err := s.Setting(ctx, SettingMode, ""); err != nil || m != compile.ModePaused {
		t.Errorf("mode %q, %v", m, err)
	}
	if log, _ := s.AuditLog(ctx, 1); len(log) != 1 || log[0].Action != "mode.set" || log[0].Before != compile.ModeActive || log[0].After != compile.ModePaused {
		t.Errorf("audit entry of the mode change: %+v", log)
	}
}

// Setting returns the default for a missing key and for a database that cannot be read.
func TestSettingDefault(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	if v, err := s.Setting(ctx, "nothing", "fallback"); err != nil || v != "fallback" {
		t.Errorf("missing setting: %q, %v", v, err)
	}
	if err := s.SetSetting(ctx, "k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting(ctx, "k", "v2"); err != nil {
		t.Fatal(err)
	}
	if v, err := s.Setting(ctx, "k", "fallback"); err != nil || v != "v2" {
		t.Errorf("stored setting: %q, %v", v, err)
	}
	if err := s.Ping(ctx); err != nil {
		t.Errorf("ping: %v", err)
	}
	s.Close()
	if v, err := s.Setting(ctx, "k", "fallback"); err == nil || v != "fallback" {
		t.Errorf("closed database: %q, %v; want the default and an error", v, err)
	}
	if err := s.Ping(ctx); err == nil {
		t.Error("ping on a closed database succeeded")
	}
}
