package store

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"

	"gopkg.in/yaml.v3"
)

func TestGameSourcesRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	var dflt string
	if err := s.db.QueryRow(`SELECT dflt_value FROM pragma_table_info('event_game') WHERE name = 'sources'`).Scan(&dflt); err != nil || dflt != "''" {
		t.Fatalf("column sources: default %q, %v", dflt, err)
	}
	seed, err := gamedb.Seed()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncSeed(ctx, seed); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Version: 1, Event: "test", Mode: compile.ModeActive,
		VLANs:    []VLANConfig{{VID: 1254, Name: "Orga", Group: "orga", CIDR: "10.10.254.0/24", RelayIP: "10.10.254.56", Attach: "trunk", Parent: "eth0", Enabled: true}},
		Profiles: []ProfileConfig{{Name: "p", Default: true, Edges: []compile.Edge{{Src: "orga", Dst: "orga"}}}},
		Games:    []EventGameConfig{{ID: "mdns", Sources: []string{"10.10.254.20", "10.10.254.9/28", "10.10.254.20"}}, {ID: "flatout2"}}}
	if err := s.ImportConfig(ctx, "test", cfg); err != nil {
		t.Fatal(err)
	}
	c, err := s.ExportConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.10.254.0/28", "10.10.254.20"} // normalised: masked, sorted, no duplicates
	if len(c.Games) != 2 || !slices.Equal(c.Games[1].Sources, want) || c.Games[0].Sources != nil {
		t.Fatalf("exported games %+v", c.Games)
	}
	y, _ := yaml.Marshal(c)
	if strings.Count(string(y), "sources:") != 1 {
		t.Errorf("YAML: sources only for the game with a filter:\n%s", y)
	}
	var back Config
	if err := yaml.Unmarshal(y, &back); err != nil || !slices.Equal(back.Games[1].Sources, want) {
		t.Fatalf("YAML round trip %+v %v", back.Games, err)
	}
	in, err := s.BuildInput(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if i := slices.IndexFunc(in.Games, func(g compile.Selection) bool { return g.GameID == "mdns" }); i < 0 ||
		!slices.Equal(in.Games[i].Sources, []netip.Prefix{netip.MustParsePrefix("10.10.254.0/28"), netip.MustParsePrefix("10.10.254.20/32")}) {
		t.Errorf("compiler input %+v", in.Games)
	}

	// set, list, clear
	if err := s.SetGameSources(ctx, "admin", "mdns", []netip.Prefix{netip.MustParsePrefix("10.10.254.30/32")}); err != nil {
		t.Fatal(err)
	}
	games, _ := s.Games(ctx)
	for _, g := range games {
		if g.ID == "mdns" && !slices.Equal(g.SrcFilter, []string{"10.10.254.30"}) || g.ID != "mdns" && g.SrcFilter != nil {
			t.Errorf("%s: SrcFilter %v", g.ID, g.SrcFilter)
		}
	}
	log, _ := s.AuditLog(ctx, 5)
	if len(log) == 0 || log[0].Action != "game.sources" || log[0].Before != "10.10.254.0/28 10.10.254.20" || log[0].After != "10.10.254.30" {
		t.Errorf("audit %+v", log)
	}
	if err := s.SetGameSources(ctx, "admin", "mdns", nil); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.ExportConfig(ctx); c.Games[1].Sources != nil {
		t.Errorf("filter not cleared: %v", c.Games[1].Sources)
	}
	if err := s.SetGameSources(ctx, "admin", "quake3", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("game not switched on: %v", err)
	}
	cfg.Games[0].Sources = []string{"drucker"}
	if err := s.ImportConfig(ctx, "test", cfg); err == nil {
		t.Error("invalid source imported")
	}
}

// Switching a game off and on again keeps its source filter and profile; while off it is not compiled, and an
// export/import (revision rollback) keeps the settings, too.
func TestGameOffKeepsSettings(t *testing.T) {
	ctx := context.Background()
	s := open(t)
	seed, err := gamedb.Seed()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncSeed(ctx, seed); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Version: 1, Event: "test", Mode: compile.ModeActive,
		VLANs:    []VLANConfig{{VID: 1254, Name: "Orga", Group: "orga", CIDR: "10.10.254.0/24", RelayIP: "10.10.254.56", Attach: "trunk", Parent: "eth0", Enabled: true}},
		Profiles: []ProfileConfig{{Name: "p", Default: true, Edges: []compile.Edge{{Src: "orga", Dst: "orga"}}}},
		Games:    []EventGameConfig{{ID: "mdns", FlowProfile: "p", Sources: []string{"10.10.254.20"}}, {ID: "flatout2"}}}
	if err := s.ImportConfig(ctx, "test", cfg); err != nil {
		t.Fatal(err)
	}
	compiled := func() []string {
		in, err := s.BuildInput(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, g := range in.Games {
			ids = append(ids, g.GameID)
		}
		return ids
	}
	if err := s.SetGameEnabled(ctx, "test", "mdns", false); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGameEnabled(ctx, "test", "flatout2", false); err != nil {
		t.Fatal(err)
	}
	if ids := compiled(); len(ids) != 0 {
		t.Fatalf("switched-off games compiled: %v", ids)
	}
	c, err := s.ExportConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Games) != 1 || c.Games[0].ID != "mdns" || !c.Games[0].Off { // flatout2 has nothing to keep
		t.Fatalf("export while off: %+v", c.Games)
	}
	if err := s.ImportConfig(ctx, "test", c); err != nil { // rollback path
		t.Fatal(err)
	}
	if err := s.SetGameEnabled(ctx, "test", "mdns", true); err != nil {
		t.Fatal(err)
	}
	c, err = s.ExportConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Games) != 1 || c.Games[0].Off || c.Games[0].FlowProfile != "p" || !slices.Equal(c.Games[0].Sources, []string{"10.10.254.20"}) {
		t.Fatalf("settings lost after off/on: %+v", c.Games)
	}
	if ids := compiled(); !slices.Equal(ids, []string{"mdns"}) {
		t.Fatalf("compiled after on: %v", ids)
	}
	games, err := s.Games(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range games {
		if g.ID == "flatout2" && g.Enabled {
			t.Error("flatout2 shown as on")
		}
	}
}
