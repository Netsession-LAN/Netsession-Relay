package gamedb

import (
	"errors"
	"strings"
	"testing"
)

func TestSeedLoads(t *testing.T) {
	db, err := Seed()
	if err != nil {
		t.Fatalf("games/games.yaml invalid:\n%v", err)
	}
	// a lower bound, not the exact number: adding a game must not need a change here, losing half the file must
	// not go unnoticed
	if len(db.Games) < 140 {
		t.Errorf("games = %d, want at least 140: is games/games.yaml truncated?", len(db.Games))
	}
	relay := 0
	for _, g := range db.Games {
		if g.RelayNeeded == RelayYes {
			relay++
		}
	}
	if relay < 100 {
		t.Errorf("only %d games have discovery ports to relay, want at least 100", relay)
	}
	if len(db.Hash()) != 64 {
		t.Errorf("hash = %q", db.Hash())
	}
}

// FlatOut has priority. Lock in the researched state.
func TestSeedFlatOut(t *testing.T) {
	db, err := Seed()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"flatout1", "flatout2"} {
		g, ok := db.Get(id)
		if !ok {
			t.Fatalf("%s missing", id)
		}
		if !g.DefaultEnabled || g.RelayNeeded != RelayYes || len(g.Discovery) != 1 {
			t.Errorf("%s: %+v", id, g)
			continue
		}
		s := g.Discovery[0].Effective()
		if s.Kind != KindBroadcast || s.Ports != "23757" || s.SourceMode != SourceOriginal || s.DstMode != DstKeep {
			t.Errorf("%s spec = %+v", id, s)
		}
		if g.GamePorts["udp"] != "23756" {
			t.Errorf("%s game port = %q, want 23756", id, g.GamePorts["udp"])
		}
	}
	if g, _ := db.Get("flatout_uc"); g.RelayNeeded != RelayUnknown || g.DefaultEnabled {
		t.Errorf("flatout_uc should be unknown/off until measured: %+v", g)
	}
	if g, _ := db.Get("flatout4"); g.RelayNeeded != RelayNo {
		t.Errorf("flatout4 has no LAN mode: %+v", g)
	}
}

// Decisions of 2026-11-16: noise services and Steam 27036 exist but are off by default.
func TestSeedDefaultsOff(t *testing.T) {
	db, _ := Seed()
	for _, g := range db.Games {
		if g.Category == CategoryNoise && g.DefaultEnabled {
			t.Errorf("noise service %s enabled by default", g.ID)
		}
	}
	for _, id := range []string{"steam_client", "netbios_ns", "mdns", "ssdp", "llmnr", "ws_discovery"} {
		g, ok := db.Get(id)
		if !ok || g.DefaultEnabled {
			t.Errorf("%s: present=%v enabled=%v, want present and off", id, ok, g.DefaultEnabled)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	base := `- id: x
  name: X
  category: game
  relay_needed: "yes"
  default_enabled: false
  confidence: high
  source_ip: original
  discovery:
  - kind: broadcast
    ports: "1234"
`
	tests := []struct {
		name, yaml, want string
	}{
		{"unknown field", base + "  colour: red\n", "colour"},
		{"bad id", strings.Replace(base, "id: x", "id: X-1", 1), "invalid id"},
		{"duplicate", base + strings.Replace(base, "name: X", "name: Y", 1), "duplicate id"},
		{"dhcp", strings.Replace(base, `"1234"`, `"60-70"`, 1), "blocked"},
		{"bad ports", strings.Replace(base, `"1234"`, `"0"`, 1), "invalid port"},
		{"bad kind", strings.Replace(base, "broadcast", "anycast", 1), "kind"},
		{"mcast no group", strings.Replace(base, "broadcast", "multicast", 1), "not an IPv4 multicast"},
		{"bcast with group", base + "    group: 224.0.2.60\n", "must not have a group"},
		{"yes without spec", strings.SplitN(base, "  discovery:", 2)[0], "no discovery spec"},
		{"enabled but unknown", strings.Replace(strings.Replace(strings.SplitN(base, "  discovery:", 2)[0],
			`"yes"`, "unknown", 1), "default_enabled: false", "default_enabled: true", 1), "default_enabled requires"},
		{"bad dst_mode", base + "    dst_mode: everywhere\n", "dst_mode"},
		{"source mode without an implementation", base + "    source_mode: relay_proxy\n", "source_mode"},
		{"burst < rate", base + "    rate_pps: 10\n    rate_burst: 5\n", "rate_pps/rate_burst"},
	}
	for _, tt := range tests {
		_, err := Load(strings.NewReader(tt.yaml))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want containing %q", tt.name, err, tt.want)
		}
	}
	if _, err := Load(strings.NewReader(base)); err != nil {
		t.Errorf("base document should be valid: %v", err)
	}
}

// The problems the web UI puts into words keep their identity through the joined error.
func TestValidateErrorIdentity(t *testing.T) {
	ok := Game{ID: "x", Name: "X", Category: CategoryGame, RelayNeeded: RelayYes, Confidence: "high", SourceIP: SourceOriginal,
		Discovery: []Spec{{Kind: KindBroadcast, Ports: "1234"}}}
	if err := Validate([]Game{ok}); err != nil {
		t.Fatal(err)
	}
	for name, tt := range map[string]struct {
		change func(*Game)
		want   error
	}{
		"id":   {func(g *Game) { g.ID = "Not Valid" }, ErrInvalidID},
		"name": {func(g *Game) { g.Name = " " }, ErrNameMissing},
		"dhcp": {func(g *Game) { g.Discovery = []Spec{{Kind: KindBroadcast, Ports: "67"}} }, ErrBlockedPort},
	} {
		g := ok
		tt.change(&g)
		err := Validate([]Game{g})
		if !errors.Is(err, tt.want) {
			t.Errorf("%s: %v, want %v", name, err, tt.want)
		}
		for _, other := range []error{ErrInvalidID, ErrNameMissing, ErrBlockedPort} {
			if other != tt.want && errors.Is(err, other) {
				t.Errorf("%s: %v is also %v", name, err, other)
			}
		}
	}
}

func TestEffectiveDefaults(t *testing.T) {
	s := Spec{Kind: KindBroadcast, Ports: "1"}.Effective()
	if s.SourceMode != SourceOriginal || s.DstMode != DstKeep || s.TTLMode != TTLKeep ||
		s.RatePPS != DefaultRatePPS || s.RateBurst != DefaultRateBurst || s.PayloadMax != DefaultPayloadMax {
		t.Errorf("Effective() = %+v", s)
	}
	s = Spec{Kind: KindBroadcast, Ports: "1", DstMode: DstSubnet, RatePPS: 20, RateBurst: 40}.Effective()
	if s.DstMode != DstSubnet || s.RatePPS != 20 || s.RateBurst != 40 {
		t.Errorf("explicit values overwritten: %+v", s)
	}
}

// Pinned games come from the file (no game is special in the code), and a mistake in the entries is an ErrInvalid.
func TestPinnedAndInvalid(t *testing.T) {
	db, err := Seed()
	if err != nil {
		t.Fatal(err)
	}
	pinned := 0
	for _, g := range db.Games {
		if g.Pinned {
			pinned++
		}
	}
	if g, _ := db.Get("flatout2"); !g.Pinned || pinned == 0 || pinned > 20 {
		t.Errorf("flatout2 pinned: %v, %d games pinned in all (the list is meant to be short)", g.Pinned, pinned)
	}
	ok := Game{ID: "x", Name: "X", Category: CategoryGame, RelayNeeded: RelayYes, Confidence: "high", SourceIP: SourceOriginal,
		Discovery: []Spec{{Kind: KindBroadcast, Ports: "1234"}}}
	if err := Validate([]Game{ok}); err != nil {
		t.Fatalf("valid game refused: %v", err)
	}
	bad := ok
	bad.ID, bad.Category = "Not An ID", "nonsense"
	err = Validate([]Game{bad})
	if !errors.Is(err, ErrInvalid) || !errors.Is(err, ErrInvalidID) || !strings.Contains(err.Error(), "category") {
		t.Errorf("invalid game: %v, want an ErrInvalid that is also ErrInvalidID and names the category", err)
	}
	if _, err := Load(strings.NewReader("- id: x\n  name: X\n  pinned: maybe\n")); err == nil {
		t.Error("pinned with a value that is not a boolean accepted")
	}
}
