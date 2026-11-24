package compile

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
)

var update = flag.Bool("update", false, "rewrite golden files in testdata/")

// netsessionVLANs is the real event layout:
// gameserver 1100 = 10.10.0.0/24, table n = VLAN 1100+n = 10.10.n.0/24, WiFi 1253, orga 1254, relay IP .55.
func netsessionVLANs() []VLAN {
	mk := func(vid int, name, group string, third int) VLAN {
		return VLAN{
			VID: vid, Name: name, Group: group, Enabled: true,
			CIDR:    netip.MustParsePrefix(fmt.Sprintf("10.10.%d.0/24", third)),
			RelayIP: netip.MustParseAddr(fmt.Sprintf("10.10.%d.55", third)),
			Attach:  AttachTrunk, Parent: "trunk0",
		}
	}
	v := []VLAN{mk(1100, "Gameserver", "gameserver", 0)}
	for n := 1; n <= 14; n++ {
		v = append(v, mk(1100+n, fmt.Sprintf("Tisch %d", n), "tische", n))
	}
	return append(v, mk(1253, "User-WiFi", "wlan", 253), mk(1254, "Orga", "orga", 254))
}

// Decision 2026-11-16: orga sees and hosts games, WiFi does not.
func gamesProfile() FlowProfile {
	var edges []Edge
	for _, a := range []string{"tische", "gameserver", "orga"} {
		for _, b := range []string{"tische", "gameserver", "orga"} {
			edges = append(edges, Edge{a, b})
		}
	}
	return FlowProfile{Name: "spiele", Edges: edges}
}

func seed(t *testing.T) *gamedb.DB {
	t.Helper()
	db, err := gamedb.Seed()
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func exampleInput(t *testing.T) Input {
	db := seed(t)
	var sel []Selection
	for _, g := range db.Games {
		if g.DefaultEnabled {
			sel = append(sel, Selection{GameID: g.ID})
		}
	}
	return Input{DB: db, VLANs: netsessionVLANs(), Profiles: []FlowProfile{gamesProfile()}, DefaultProfile: "spiele", Games: sel, Probe: true}
}

func TestExampleEventGolden(t *testing.T) {
	snap, _, err := Compile(exampleInput(t))
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", "example_snapshot.json")
	if *update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run: go test ./internal/compile -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("snapshot differs from %s; if intended, run: go test ./internal/compile -update", path)
	}
}

func ruleByPort(s *Snapshot, kind string, port string) *Rule {
	for i := range s.Rules {
		if s.Rules[i].Kind == kind && s.Rules[i].Ports == port {
			return &s.Rules[i]
		}
	}
	return nil
}

func TestExampleEventContent(t *testing.T) {
	snap, warns, err := Compile(exampleInput(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.VLANs) != 17 || snap.Mode != ModeActive || snap.Version != SnapshotVersion {
		t.Fatalf("vlans=%d mode=%s version=%d", len(snap.VLANs), snap.Mode, snap.Version)
	}
	// FlatOut 1+2 share 23757, original source, keep destination, matrix without WiFi
	fo := ruleByPort(snap, gamedb.KindBroadcast, "23757")
	if fo == nil {
		t.Fatal("no rule for 23757")
	}
	if !slices.Equal(fo.Specs, []string{"flatout1", "flatout2"}) || fo.SourceMode != "original" || fo.DstMode != "keep" {
		t.Errorf("23757 rule = %+v", fo)
	}
	for _, e := range fo.Flows {
		if e.Src == "wlan" || e.Dst == "wlan" {
			t.Errorf("WiFi must not be in the games matrix: %+v", e)
		}
	}
	if fo.ID != "bc:23757" {
		t.Errorf("id = %q", fo.ID)
	}
	// UT99 range stays one rule
	if r := ruleByPort(snap, gamedb.KindBroadcast, "8777-8786"); r == nil {
		t.Error("UT99 8777-8786 not merged into one rule")
	}
	// Minecraft Java multicast
	var mc *Rule
	for i := range snap.Rules {
		if snap.Rules[i].Kind == gamedb.KindMulticast && snap.Rules[i].Group == "224.0.2.60" {
			mc = &snap.Rules[i]
		}
	}
	if mc == nil || mc.Ports != "4445" || mc.ID != "mc:224.0.2.60:4445" {
		t.Errorf("minecraft rule = %+v", mc)
	}
	// nothing below 1024, nothing from Steam 27036 / noise by default
	for _, r := range snap.Rules {
		if r.Ports == "27036" || strings.HasPrefix(r.Ports, "137") || strings.HasPrefix(r.Ports, "138") {
			t.Errorf("default event contains %s (%v)", r.ID, r.Specs)
		}
	}
	// shared Source port range produces exactly one shared_port warning
	n := 0
	for _, w := range warns {
		if w.Code == WarnSharedPort && strings.Contains(w.Message, "27015") {
			n++
		}
	}
	if n == 0 {
		t.Error("expected a shared_port warning for the Source range")
	}
	if len(snap.Hash) != 64 {
		t.Errorf("hash %q", snap.Hash)
	}
}

func TestHashDeterministic(t *testing.T) {
	in := exampleInput(t)
	a, _, err := Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(in.Games)
	slices.Reverse(in.VLANs)
	b, _, err := Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash != b.Hash {
		t.Error("hash depends on input order")
	}
	in.Mode = ModeObserve
	c, _, _ := Compile(in)
	if c.Hash == a.Hash {
		t.Error("hash ignores mode")
	}
}

const conflictYAML = `
- id: alpha
  name: Alpha
  category: game
  relay_needed: "yes"
  default_enabled: true
  confidence: high
  source_ip: original
  discovery:
  - {kind: broadcast, ports: "6112"}
- id: beta
  name: Beta
  category: game
  relay_needed: "yes"
  default_enabled: true
  confidence: high
  source_ip: relay
  discovery:
  - {kind: broadcast, ports: "6111-6112", dst_mode: subnet, rate_pps: 20, rate_burst: 40}
- id: gamma
  name: Gamma
  category: game
  relay_needed: "no"
  default_enabled: false
  confidence: high
  source_ip: unknown
`

func smallInput(t *testing.T, sel ...Selection) Input {
	db, err := gamedb.Load(strings.NewReader(conflictYAML))
	if err != nil {
		t.Fatal(err)
	}
	return Input{DB: db, VLANs: netsessionVLANs()[:3], Profiles: []FlowProfile{{Name: "p", Edges: []Edge{{"tische", "tische"}}}},
		DefaultProfile: "p", Games: sel}
}

func codes(ws []Warning) []string {
	var c []string
	for _, w := range ws {
		c = append(c, w.Code)
	}
	return c
}

func TestConflicts(t *testing.T) {
	snap, warns, err := Compile(smallInput(t,
		Selection{GameID: "alpha"},
		Selection{GameID: "beta", Override: Override{SourceMode: "relay"}},
		Selection{GameID: "gamma"},
	))
	if err != nil {
		t.Fatal(err)
	}
	r6111 := ruleByPort(snap, gamedb.KindBroadcast, "6111")
	r6112 := ruleByPort(snap, gamedb.KindBroadcast, "6112")
	if r6111 == nil || r6112 == nil {
		t.Fatalf("rules = %+v", snap.Rules)
	}
	// 6111 only beta: its own values
	if r6111.SourceMode != "relay" || r6111.DstMode != "subnet" || r6111.RatePPS != 20 {
		t.Errorf("6111 = %+v", r6111)
	}
	// 6112 shared: original wins, keep wins, rate = max
	if r6112.SourceMode != "original" || r6112.DstMode != "keep" || r6112.RatePPS != 20 || r6112.RateBurst != 40 {
		t.Errorf("6112 = %+v", r6112)
	}
	c := codes(warns)
	for _, want := range []string{WarnSourceModeConflict, WarnDstModeConflict, WarnSharedPort, WarnGameNoRelay} {
		if !slices.Contains(c, want) {
			t.Errorf("missing warning %s in %v", want, warns)
		}
	}
	for _, w := range warns {
		if w.Code == WarnSourceModeConflict && !strings.Contains(w.Message, "broadcast port 6112") {
			t.Errorf("conflict warning without port: %s", w)
		}
	}
	if len(snap.Warnings) != len(warns) {
		t.Errorf("snapshot warnings %d != %d", len(snap.Warnings), len(warns))
	}
}

func TestRelayIPRewriteWarning(t *testing.T) {
	_, warns, err := Compile(smallInput(t, Selection{GameID: "alpha", Override: Override{SourceMode: "relay"}}))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(codes(warns), WarnRelayIPRewrite) {
		t.Errorf("want relay_ip_rewrite warning, got %v", warns)
	}
}

func TestDisabledVLANAndEmptyGroup(t *testing.T) {
	in := smallInput(t, Selection{GameID: "alpha"})
	in.VLANs[0].Enabled = false // gameserver
	in.VLANs[0].CIDR = netip.Prefix{}
	snap, warns, err := Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.VLANs) != 2 {
		t.Errorf("vlans = %d", len(snap.VLANs))
	}
	if !slices.Contains(codes(warns), WarnGroupEmpty) {
		t.Errorf("want group_empty, got %v", warns)
	}
}

func TestErrors(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*Input)
		want string
	}{
		{"unknown game", func(in *Input) { in.Games = append(in.Games, Selection{GameID: "nope"}) }, "not in database"},
		{"twice", func(in *Input) { in.Games = append(in.Games, in.Games[0]) }, "selected twice"},
		{"unknown profile", func(in *Input) { in.Games[0].FlowProfile = "x" }, "not defined"},
		{"profile group without vlan", func(in *Input) { in.Profiles[0].Edges = append(in.Profiles[0].Edges, Edge{"tische", "wlan"}) }, `group "wlan" has no VLAN`},
		{"overlap", func(in *Input) {
			in.VLANs[2].CIDR = netip.MustParsePrefix("10.10.0.0/16")
			in.VLANs[2].RelayIP = netip.MustParseAddr("10.10.200.55")
		}, "overlap"},
		{"relay ip outside", func(in *Input) { in.VLANs[1].RelayIP = netip.MustParseAddr("10.10.9.55") }, "not in"},
		{"vid twice", func(in *Input) { in.VLANs[1].VID = in.VLANs[0].VID }, "VID used twice"},
		{"vid range", func(in *Input) { in.VLANs[1].VID = 5000 }, "out of range"},
		{"no parent", func(in *Input) { in.VLANs[1].Parent = "" }, "parent"},
		{"device without name", func(in *Input) { in.VLANs[1].Attach = AttachDevice }, "device name"},
		{"tiny net", func(in *Input) {
			in.VLANs[1].CIDR = netip.MustParsePrefix("10.10.1.55/32")
		}, "too small"},
		{"bad mode", func(in *Input) { in.Mode = "turbo" }, "mode"},
		{"proxy override", func(in *Input) { in.Games[0].Override.SourceMode = "relay_proxy" }, "not supported"},
	}
	for _, tt := range tests {
		in := smallInput(t, Selection{GameID: "alpha"})
		tt.mod(&in)
		_, _, err := Compile(in)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want containing %q", tt.name, err, tt.want)
		}
	}
}

func TestDeviceAttach(t *testing.T) {
	in := smallInput(t, Selection{GameID: "alpha"})
	for i := range in.VLANs {
		in.VLANs[i].Attach = AttachDevice
		in.VLANs[i].Device = fmt.Sprintf("ens%d", 19+i)
	}
	snap, _, err := Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	if snap.VLANs[0].Parent != "" || snap.VLANs[0].Device == "" {
		t.Errorf("vlan = %+v", snap.VLANs[0])
	}
}

func TestProbeRules(t *testing.T) {
	in := exampleInput(t)
	in.Probe = true
	snap, _, err := Compile(in)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Probe {
		t.Fatal("snapshot without probe")
	}
	probes := 0
	for _, r := range snap.Rules {
		if !r.Probe {
			continue
		}
		probes++
		if r.Kind != "broadcast" {
			t.Errorf("%s: probe on %s", r.ID, r.Kind)
		}
		for _, id := range r.Specs {
			g, _ := in.DB.Get(id)
			for _, d := range g.Discovery {
				if d.Kind == r.Kind && d.Direction != "query" && strings.Contains(r.Ports, d.Ports) {
					t.Errorf("%s: %s has direction %q", r.ID, id, d.Direction)
				}
			}
		}
	}
	if probes == 0 {
		t.Error("no probe rule in the example event")
	}
	if r := ruleByPort(snap, "broadcast", "23757"); r != nil && r.Probe { // FlatOut 2: direction both
		t.Error("FlatOut 2 port probed")
	}
}

func pfx(s ...string) []netip.Prefix {
	var out []netip.Prefix
	for _, x := range s {
		out = append(out, netip.MustParsePrefix(x))
	}
	return out
}

func TestSourceFilterHashUnchangedWithoutFilter(t *testing.T) {
	a, _, err := Compile(smallInput(t, Selection{GameID: "alpha"}))
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := Compile(smallInput(t, Selection{GameID: "alpha", Sources: []netip.Prefix{}}))
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash != b.Hash {
		t.Error("empty filter changes the hash")
	}
	if raw, _ := json.Marshal(a.Rules); strings.Contains(string(raw), "src_allow") {
		t.Errorf("src_allow in a snapshot without filter: %s", raw)
	}
	c, _, err := Compile(smallInput(t, Selection{GameID: "alpha", Sources: pfx("10.10.1.20/32")}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Hash == a.Hash {
		t.Error("filter does not change the hash")
	}
}

func TestSourceFilter(t *testing.T) {
	// one game: the prefix restricts only Tisch 1 (10.10.1.0/24)
	snap, warns, err := Compile(smallInput(t, Selection{GameID: "alpha", Sources: pfx("10.10.1.20/32")}))
	if err != nil {
		t.Fatal(err)
	}
	if r := ruleByPort(snap, gamedb.KindBroadcast, "6112"); r == nil || !slices.Equal(r.SrcAllow, pfx("10.10.1.20/32")) {
		t.Errorf("6112 = %+v", r)
	}
	for _, w := range warns {
		if strings.HasPrefix(w.Code, "source_") {
			t.Errorf("unexpected warning %s", w)
		}
	}

	// prefix in no network: warning, no restriction; host bits are masked
	snap, warns, err = Compile(smallInput(t, Selection{GameID: "alpha", Sources: pfx("10.9.9.9/32", "10.10.2.7/28")}))
	if err != nil {
		t.Fatal(err)
	}
	if r := ruleByPort(snap, gamedb.KindBroadcast, "6112"); !slices.Equal(r.SrcAllow, pfx("10.10.2.0/28")) {
		t.Errorf("6112 = %v", r.SrcAllow)
	}
	if !slices.ContainsFunc(warns, func(w Warning) bool { return w.Code == WarnSourceNoVLAN && strings.Contains(w.Message, "10.9.9.9") }) {
		t.Errorf("no source_no_vlan warning: %v", warns)
	}

	// shared port 6112: Tisch 1 restricted by both games -> union; Tisch 2 only by beta -> open wins + warning.
	// 6111 is beta alone and keeps its own filter.
	snap, warns, err = Compile(smallInput(t,
		Selection{GameID: "alpha", Sources: pfx("10.10.1.20/32")},
		Selection{GameID: "beta", Sources: pfx("10.10.1.30/32", "10.10.2.0/28")}))
	if err != nil {
		t.Fatal(err)
	}
	if r := ruleByPort(snap, gamedb.KindBroadcast, "6112"); r == nil || !slices.Equal(r.SrcAllow, pfx("10.10.1.20/32", "10.10.1.30/32")) {
		t.Errorf("6112 = %+v", r)
	}
	if r := ruleByPort(snap, gamedb.KindBroadcast, "6111"); r == nil || !slices.Equal(r.SrcAllow, pfx("10.10.1.30/32", "10.10.2.0/28")) {
		t.Errorf("6111 = %+v", r)
	}
	i := slices.IndexFunc(warns, func(w Warning) bool { return w.Code == WarnSourceFilterOpen })
	if i < 0 || !strings.Contains(warns[i].Message, "VLAN 1102: beta filter, alpha not") || !strings.Contains(warns[i].Message, "port 6112") {
		t.Errorf("open-wins warning missing or incomplete: %v", warns)
	}

	// one game without any filter on a shared port: no restriction at all
	snap, warns, err = Compile(smallInput(t, Selection{GameID: "alpha", Sources: pfx("10.10.1.20/32")}, Selection{GameID: "beta"}))
	if err != nil {
		t.Fatal(err)
	}
	if r := ruleByPort(snap, gamedb.KindBroadcast, "6112"); len(r.SrcAllow) != 0 {
		t.Errorf("6112 restricted although beta has no filter: %v", r.SrcAllow)
	}
	if !slices.Contains(codes(warns), WarnSourceFilterOpen) {
		t.Errorf("no open-wins warning: %v", warns)
	}

	// not IPv4: error
	if _, _, err := Compile(smallInput(t, Selection{GameID: "alpha", Sources: pfx("fd00::1/128")})); err == nil || !strings.Contains(err.Error(), "IPv4") {
		t.Errorf("IPv6 source accepted: %v", err)
	}
}

func TestParseSources(t *testing.T) {
	got, err := ParseSources(" 10.10.254.20, 10.10.254.9/28;10.10.254.20\n")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, pfx("10.10.254.0/28", "10.10.254.20/32")) {
		t.Errorf("parsed %v", got)
	}
	if s := strings.Join(FormatSources(got), " "); s != "10.10.254.0/28 10.10.254.20" {
		t.Errorf("formatted %q", s)
	}
	if got, err := ParseSources("  "); err != nil || len(got) != 0 {
		t.Errorf("empty = %v, %v", got, err)
	}
	for _, bad := range []string{"drucker", "10.10.254.300", "fd00::1", "10.0.0.0/33"} {
		if _, err := ParseSources(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
