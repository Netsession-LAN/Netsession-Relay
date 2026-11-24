package engine

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/Netsession2024/Netsession-Relay/internal/compile"
	"github.com/Netsession2024/Netsession-Relay/internal/pkt"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time      { return c.t }
func (c *fakeClock) add(d time.Duration) { c.t = c.t.Add(d) }
func newClock() *fakeClock               { return &fakeClock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)} }
func guardAt(c *fakeClock) *Guard        { g := NewGuard(c.now); g.Holdoff = 0; return g }
func limitsSnapshot(s *compile.Snapshot) *compile.Snapshot {
	s.Limits = compile.DefaultLimits
	return s
}

func process(p *Processor, tb *Table, in int, b []byte) Reason {
	return p.Process(tb, in, b, func(int, []byte) error { return nil })
}

func TestRateLimitPerFlow(t *testing.T) {
	c := newClock()
	g := guardAt(c)
	tb := newTable(t, limitsSnapshot(testSnapshot(defaultRules()...)))
	p := NewProcessor(&Counters{}, g)
	b := frame("10.10.1.10", "255.255.255.255", 50000, 23757, []byte("q"))
	relayed := 0
	for range 1000 { // 1000 frames in the same instant: only the burst (10) passes
		if process(p, tb, t1, b) == Relayed {
			relayed++
		}
	}
	if relayed != 10 {
		t.Fatalf("relayed %d of 1000 at once, want burst 10", relayed)
	}
	c.add(time.Second) // rate 5/s refills 5 tokens
	relayed = 0
	for range 100 {
		if process(p, tb, t1, b) == Relayed {
			relayed++
		}
	}
	if relayed != 5 {
		t.Errorf("after 1 s relayed %d, want 5", relayed)
	}
	if p.c.Reasons[DropRate].Load() == 0 {
		t.Error("rate drops not counted")
	}
	// another source has its own bucket
	if r := process(p, tb, t1, frame("10.10.1.11", "255.255.255.255", 50000, 23757, []byte("q"))); r != Relayed {
		t.Errorf("second source: %v", r)
	}
}

func TestRateLimitPerVLAN(t *testing.T) {
	c := newClock()
	g := guardAt(c)
	s := limitsSnapshot(testSnapshot(defaultRules()...))
	s.Limits.VLANPPS, s.Limits.VLANBurst = 3, 3
	tb := newTable(t, s)
	p := NewProcessor(&Counters{}, g)
	relayed := 0
	for i := range 10 { // 10 different sources, each within its flow budget
		src := netip.AddrFrom4([4]byte{10, 10, 1, byte(20 + i)}).String()
		if process(p, tb, t1, frame(src, "255.255.255.255", 50000, 23757, []byte("q"))) == Relayed {
			relayed++
		}
	}
	if relayed != 3 {
		t.Errorf("relayed %d, want VLAN burst 3", relayed)
	}
}

func TestLoopDetection(t *testing.T) {
	c := newClock()
	g := guardAt(c)
	// a datagram whose source fits both VLANs is impossible after step 3, so emulate a loop with a /16 that
	// covers both lab VLANs on the second VLAN (misconfigured switch: same broadcast domain twice)
	s := limitsSnapshot(testSnapshot(defaultRules()...))
	tb := newTable(t, s)
	p := NewProcessor(&Counters{}, g)
	b := frame("10.10.1.10", "255.255.255.255", 50000, 23757, []byte("same"))
	f, _ := pkt.Parse(b)
	if g.SeenElsewhere(&f, t1, c.now(), 200*time.Millisecond) {
		t.Fatal("first sighting reported as loop")
	}
	if !g.SeenElsewhere(&f, t2, c.now().Add(50*time.Millisecond), 200*time.Millisecond) {
		t.Error("same datagram on another VLAN within window not detected")
	}
	if g.SeenElsewhere(&f, t1, c.now().Add(60*time.Millisecond), 200*time.Millisecond) {
		t.Error("repeat on the original VLAN is not a loop (rate limit handles it)")
	}
	if g.SeenElsewhere(&f, t1, c.now().Add(time.Second), 200*time.Millisecond) {
		t.Error("outside the window is not a loop")
	}
	_ = tb
	_ = p
}

func TestForeignRelayAlarm(t *testing.T) {
	c := newClock()
	g := guardAt(c)
	tb := newTable(t, limitsSnapshot(testSnapshot(defaultRules()...)))
	p := NewProcessor(&Counters{}, g)
	for range 3 {
		if r := process(p, tb, t1, frame("10.10.2.99", "255.255.255.255", 1, 23757, []byte("x"))); r != DropForeignRelay {
			t.Fatalf("reason %v", r)
		}
	}
	al := g.Alarms.List()
	if len(al) != 1 || al[0].Kind != AlarmForeignRelay || al[0].Count != 3 || al[0].Key != "1101" {
		t.Errorf("alarms = %+v", al)
	}
}

func TestHoldoffAndStandby(t *testing.T) {
	c := newClock()
	g := NewGuard(c.now) // default holdoff 1.5 s
	tb := newTable(t, limitsSnapshot(testSnapshot(defaultRules()...)))
	p := NewProcessor(&Counters{}, g)
	b := func() []byte { return frame("10.10.1.10", "255.255.255.255", 50000, 23757, []byte(c.now().String())) }
	if r := process(p, tb, t1, b()); r != DropMode {
		t.Errorf("during holdoff: %v", r)
	}
	c.add(2 * time.Second)
	if r := process(p, tb, t1, b()); r != Relayed {
		t.Errorf("after holdoff: %v", r)
	}
	// an engine that started earlier is active -> standby
	other := Beacon{Started: g.Started.Add(-time.Hour).UnixNano(), Active: true}
	other.Instance[0] = 0xEE
	if !g.SeeBeacon(other, c.now()) || !g.Standby() {
		t.Fatal("did not enter standby")
	}
	if r := process(p, tb, t1, b()); r != DropMode {
		t.Errorf("in standby: %v", r)
	}
	if g.MyBeacon(tb.Snapshot).Active {
		t.Error("standby engine announces itself as active")
	}
	// earlier engine goes silent -> back to active after 10 s
	c.add(5 * time.Second)
	if g.PeerTick(c.now()) {
		t.Error("left standby too early")
	}
	c.add(6 * time.Second)
	if !g.PeerTick(c.now()) || g.Standby() {
		t.Error("did not leave standby after peer timeout")
	}
}

func TestLaterEngineDoesNotYield(t *testing.T) {
	c := newClock()
	g := guardAt(c)
	later := Beacon{Started: g.Started.Add(time.Minute).UnixNano(), Active: true}
	later.Instance[0] = 1
	if g.SeeBeacon(later, c.now()) || g.Standby() {
		t.Error("earlier engine yielded to a later one")
	}
	inactive := Beacon{Started: g.Started.Add(-time.Minute).UnixNano(), Active: false}
	inactive.Instance[0] = 2
	if g.SeeBeacon(inactive, c.now()) {
		t.Error("yielded to a standby/observe engine")
	}
	self := g.MyBeacon(&compile.Snapshot{Mode: compile.ModeActive})
	if g.SeeBeacon(self, c.now()) {
		t.Error("yielded to itself")
	}
}

func TestBeaconRoundTrip(t *testing.T) {
	g := NewGuard(nil)
	b := g.MyBeacon(&compile.Snapshot{Mode: compile.ModeActive, Hash: "0123456789abcdef"})
	f := BuildBeacon(pkt.MAC{2, 0, 0, 0, 0, 1}, b)
	got, ok := ParseBeacon(f)
	if !ok || !reflect.DeepEqual(got, b) {
		t.Fatalf("round trip: ok=%v %+v != %+v", ok, got, b)
	}
	g.SetPorts(&compile.Snapshot{Rules: []compile.Rule{{Ports: "23757"}, {Ports: "2302-2470,47624"}, {Ports: "23756-23758"}}})
	b = g.MyBeacon(&compile.Snapshot{Mode: compile.ModeActive})
	if want := []PortRange{{2302, 2470}, {23756, 23758}, {47624, 47624}}; b.AllPorts || !reflect.DeepEqual(b.Ports, want) {
		t.Fatalf("ports %v all=%v, want %v", b.Ports, b.AllPorts, want)
	}
	if got, ok := ParseBeacon(BuildBeacon(pkt.MAC{2, 0, 0, 0, 0, 1}, b)); !ok || !reflect.DeepEqual(got, b) {
		t.Fatalf("v2 round trip: ok=%v %+v != %+v", ok, got, b)
	}
	if _, ok := ParseBeacon(f[:20]); ok {
		t.Error("short beacon accepted")
	}
	f[14] = 'X'
	if _, ok := ParseBeacon(f); ok {
		t.Error("bad magic accepted")
	}
}

func TestStormBrake(t *testing.T) {
	c := newClock()
	g := guardAt(c)
	s := limitsSnapshot(testSnapshot(defaultRules()...))
	s.Limits.StormPPS = 100
	tb := newTable(t, s)
	cnt := &Counters{}
	fo := tb.rules[0] // bc:23757
	for sec := 1; sec <= 3; sec++ {
		cnt.Sent.Add(500)
		fo.copies[t1].Add(400) // table 1 floods FlatOut
		fo.copies[t2].Add(60)
		tb.rules[1].copies[t1].Add(40)
		c.add(time.Second)
		id, vid := g.StormTick(tb, cnt, c.now())
		if sec < 3 && id != "" {
			t.Fatalf("paused after %d s", sec)
		}
		if sec == 3 && (id != "bc:23757" || vid != 1101) {
			t.Fatalf("paused %q for VLAN %d, want the loudest pair bc:23757 from 1101", id, vid)
		}
	}
	p := NewProcessor(cnt, g)
	if r := process(p, tb, t1, frame("10.10.1.10", "255.255.255.255", 1, 23757, []byte("x"))); r != DropAutoPaused {
		t.Errorf("paused rule from the flooding VLAN: %v", r)
	}
	// the same game from every other VLAN keeps working: one participant cannot take it away from all
	if r := process(p, tb, t2, frame("10.10.2.10", "255.255.255.255", 1, 23757, []byte("x2"))); r != Relayed {
		t.Errorf("same rule from another VLAN must keep working: %v", r)
	}
	if r := process(p, tb, t1, frame("10.10.1.10", "255.255.255.255", 1, 27015, []byte("x"))); r != Relayed {
		t.Errorf("other rule must keep working: %v", r)
	}
	// the paused flood goes on (spoofed senders) and must not use up the budgets of table 1 (found in the lab:
	// the budgets were taken before the pause was checked, so other games of the flooding table starved)
	for i := range 500 {
		src := netip.AddrFrom4([4]byte{10, 10, 1, byte(i%250 + 2)}).String()
		if r := process(p, tb, t1, frame(src, "255.255.255.255", 1, 23757, []byte{byte(i), byte(i >> 8)})); r != DropAutoPaused {
			t.Fatalf("paused flood frame %d: %v", i, r)
		}
	}
	if r := process(p, tb, t1, frame("10.10.1.12", "255.255.255.255", 1, 27015, []byte("after flood"))); r != Relayed {
		t.Errorf("other rule of the flooding VLAN starved by the paused flood: %v", r)
	}
	// the pause survives a snapshot change (new table)
	if r := process(p, newTable(t, s), t1, frame("10.10.1.11", "255.255.255.255", 1, 23757, []byte("z"))); r != DropAutoPaused {
		t.Errorf("pause lost with a new table: %v", r)
	}
	c.add(61 * time.Second)
	if r := process(p, tb, t1, frame("10.10.1.10", "255.255.255.255", 1, 23757, []byte("y"))); r != Relayed {
		t.Errorf("after 60 s: %v", r)
	}
	if al := g.Alarms.List(); len(al) != 1 || al[0].Kind != AlarmStorm {
		t.Errorf("alarms %+v", al)
	}
	// quiet seconds reset the counter
	cnt.Sent.Add(10)
	if id, _ := g.StormTick(tb, cnt, c.now()); id != "" || g.stormOverSecs != 0 || g.pausedN.Load() != 0 {
		t.Error("quiet second did not reset (or the expired pause was kept)")
	}
}

// One VLAN cannot use up the engine-wide storm budget: its copies (frames times target VLANs) are limited,
// whatever source addresses it invents.
func TestCopyBudgetPerVLAN(t *testing.T) {
	c := newClock()
	g := guardAt(c)
	s := limitsSnapshot(testSnapshot(defaultRules()...))
	s.Limits.StormPPS, s.Limits.VLANPPS, s.Limits.VLANBurst = 200, 5000, 5000
	tb := newTable(t, s)
	cnt := &Counters{}
	p := NewProcessor(cnt, g)
	relayed := 0
	for i := range 250 { // every frame from a new (spoofed) source, so the per-flow bucket never matches
		src := netip.AddrFrom4([4]byte{10, 10, 1, byte(i%250 + 2)}).String()
		if process(p, tb, t1, frame(src, "255.255.255.255", 1, 23757, []byte{byte(i)})) == Relayed {
			relayed++
		}
	}
	// each frame goes to 3 VLANs; the budget is storm_pps/2 = 100 copies
	if relayed == 0 || cnt.Sent.Load() > 100 {
		t.Errorf("relayed %d frames, %d copies; want at most 100 copies", relayed, cnt.Sent.Load())
	}
	if r := process(p, tb, t2, frame("10.10.2.10", "255.255.255.255", 1, 23757, []byte("other"))); r != Relayed {
		t.Errorf("another VLAN has its own budget: %v", r)
	}
}

// The engine keeps its protections whatever the snapshot says.
func TestHardBounds(t *testing.T) {
	s := testSnapshot(defaultRules()...)
	s.Limits = compile.Limits{VLANPPS: 0, VLANBurst: -5, StormPPS: 10_000_000, DedupMS: 0}
	s.Rules[0].RatePPS, s.Rules[0].RateBurst, s.Rules[0].PayloadMax = 0, 1_000_000, 9000
	s.VLANs[1].RatePPS = 1_000_000
	tb := newTable(t, s)
	d := compile.DefaultLimits
	if got, want := tb.limits, (compile.Limits{VLANPPS: d.VLANPPS, VLANBurst: d.VLANBurst, StormPPS: maxStormPPS, DedupMS: d.DedupMS}); got != want {
		t.Errorf("limits %+v, want %+v", got, want)
	}
	if r := tb.rules[0]; r.RatePPS != 5 || r.RateBurst != maxRuleBurst || r.PayloadMax != 1472 {
		t.Errorf("rule bounds: %d/%d/%d", r.RatePPS, r.RateBurst, r.PayloadMax)
	}
	if tb.VLANs[1].RatePPS != maxVLANPPS || tb.VLANs[0].RatePPS != 0 {
		t.Errorf("VLAN override: %d / %d", tb.VLANs[1].RatePPS, tb.VLANs[0].RatePPS)
	}
	if len(tb.Clamped) != 8 {
		t.Errorf("clamped notes: %q", tb.Clamped)
	}
	if s.Limits.StormPPS != 10_000_000 || s.Rules[0].RatePPS != 0 {
		t.Error("the snapshot itself must stay as it was sent (hash)")
	}
	if tb := newTable(t, limitsSnapshot(testSnapshot(defaultRules()...))); len(tb.Clamped) != 0 {
		t.Errorf("sane snapshot clamped: %q", tb.Clamped)
	}
	// a rate of 0 no longer means "no limit": rule 1 (rate 0 -> default 5/s, burst 10) from one source
	s2 := limitsSnapshot(testSnapshot(defaultRules()...))
	s2.Rules[0].RatePPS, s2.Rules[0].RateBurst = 0, 0
	tb2 := newTable(t, s2)
	c := newClock()
	p := NewProcessor(&Counters{}, guardAt(c))
	n := 0
	for i := range 100 {
		if process(p, tb2, t1, frame("10.10.1.10", "255.255.255.255", 1, 23757, []byte{byte(i)})) == Relayed {
			n++
		}
	}
	if n != 10 {
		t.Errorf("%d of 100 frames relayed with rate 0 in the snapshot, want the default burst 10", n)
	}
}

func signedBeacon(g *Guard, key []byte, at time.Time, mut func(*Beacon)) []byte {
	b := Beacon{Started: g.Started.Add(-time.Hour).UnixNano(), Active: true, AllPorts: true}
	b.Instance[0] = 0xEE
	if mut != nil {
		mut(&b)
	}
	f := BuildBeacon(pkt.MAC{2, 0, 0, 0, 0, 9}, b)
	if key != nil {
		f = SignBeacon(f, key, at)
	}
	return f
}

// With a key only beacons signed with it count; anything else is ignored and raises unknown_beacon.
func TestBeaconSignature(t *testing.T) {
	c := newClock()
	key := []byte("0123456789abcdef0123456789abcdef")
	newGuard := func() *Guard {
		g := guardAt(c)
		g.SetBeaconKey(key)
		g.SetPorts(&compile.Snapshot{Rules: []compile.Rule{{Ports: "23757"}}})
		return g
	}
	alarm := func(g *Guard) string {
		for _, a := range g.Alarms.List() {
			if a.Kind == AlarmUnknownBeacon {
				return a.Key
			}
		}
		return ""
	}
	// the forged beacon of the audit: unsigned, active, started long ago, all ports
	g := newGuard()
	if g.SeeFrame(signedBeacon(g, nil, c.now(), func(b *Beacon) { b.Started = 1 }), 1101, c.now()) || g.Standby() {
		t.Fatal("an unsigned beacon put the relay into standby")
	}
	if alarm(g) != "1101" {
		t.Errorf("no unknown_beacon alarm for VLAN 1101: %+v", g.Alarms.List())
	}
	// version 1 (old engine) counts as unsigned
	g = newGuard()
	v1 := signedBeacon(g, nil, c.now(), nil)
	v1[pkt.EthHeaderLen+4] = 1
	if g.SeeFrame(v1[:pkt.EthHeaderLen+beaconLen], 1102, c.now()) || alarm(g) != "1102" {
		t.Error("version 1 beacon must be ignored with an alarm")
	}
	// wrong key, changed content, replay
	g = newGuard()
	if g.SeeFrame(signedBeacon(g, []byte("another-key-another-key"), c.now(), nil), 1101, c.now()) || alarm(g) == "" {
		t.Error("beacon signed with another key accepted")
	}
	g = newGuard()
	f := signedBeacon(g, key, c.now(), func(b *Beacon) { b.Active = false })
	f[pkt.EthHeaderLen+29] = 1 // attacker flips "active" in a recorded standby beacon
	if g.SeeFrame(f, 1101, c.now()) || alarm(g) == "" {
		t.Error("modified beacon accepted")
	}
	g = newGuard()
	old := signedBeacon(g, key, c.now(), nil)
	if g.SeeFrame(old, 1101, c.now().Add(BeaconMaxSkew+time.Second)) || alarm(g) == "" {
		t.Error("replayed (old) beacon accepted")
	}
	// the real thing: same key, fresh -> standby, no alarm
	g = newGuard()
	if !g.SeeFrame(signedBeacon(g, key, c.now(), nil), 1101, c.now().Add(2*time.Second)) || !g.Standby() || alarm(g) != "" {
		t.Errorf("valid beacon: standby %v, alarms %+v", g.Standby(), g.Alarms.List())
	}
	// an own frame (same instance) never counts, signed or not
	g = newGuard()
	if g.SeeFrame(g.BeaconFrame(pkt.MAC{2}, &compile.Snapshot{Mode: compile.ModeActive}, c.now()), 1101, c.now()) || alarm(g) != "" {
		t.Error("own beacon handled")
	}
	// other ports (mDNS instance next to the games) with another key: no conflict, no alarm
	g = newGuard()
	mdns := func(b *Beacon) { b.AllPorts, b.Ports = false, []PortRange{{5353, 5353}} }
	if g.SeeFrame(signedBeacon(g, []byte("key-of-the-mdns-instance"), c.now(), mdns), 1101, c.now()) || alarm(g) != "" {
		t.Error("relay for other ports must be ignored quietly")
	}
	// and with the same key it stays out of the way as before
	if g.SeeFrame(signedBeacon(g, key, c.now(), mdns), 1101, c.now()) || g.Standby() || alarm(g) != "" {
		t.Error("mDNS instance with the same key caused standby or an alarm")
	}
	// without a key everything is as before signatures: unsigned beacons count
	g = guardAt(c)
	if !g.SeeFrame(signedBeacon(g, nil, c.now(), nil), 1101, c.now()) || !g.Standby() {
		t.Error("without a key an unsigned beacon must still count")
	}
	// a signed frame is still a valid version 2 beacon for engines without signature support
	b, ok := ParseBeacon(signedBeacon(g, key, c.now(), func(b *Beacon) { b.AllPorts, b.Ports = false, []PortRange{{1, 2}, {7, 9}} }))
	if !ok || b.AllPorts || len(b.Ports) != 2 || !b.Active {
		t.Errorf("signed beacon does not parse as v2: %v %+v", ok, b)
	}
}

func TestLoadBeaconKey(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "beacon.key")
	k1, src, err := LoadBeaconKey("", file)
	if err != nil || src != "file" || len(k1) != 64 {
		t.Fatalf("first load: %q %s %v", k1, src, err)
	}
	if st, err := os.Stat(file); err != nil || (runtime.GOOS != "windows" && st.Mode().Perm() != 0o600) {
		t.Errorf("key file: %v %v", st, err)
	}
	if k2, _, err := LoadBeaconKey("", file); err != nil || string(k2) != string(k1) {
		t.Errorf("second load gives another key: %v", err)
	}
	if k, src, err := LoadBeaconKey("  shared-secret-of-two-relays ", file); err != nil || src != "env" || string(k) != "shared-secret-of-two-relays" {
		t.Errorf("env: %q %s %v", k, src, err)
	}
	if _, _, err := LoadBeaconKey("short", file); err == nil {
		t.Error("short key accepted")
	}
	if _, _, err := LoadBeaconKey("", filepath.Join(dir, "missing", "beacon.key")); err == nil {
		t.Error("unwritable state directory must be an error (engine then runs unsigned and says so)")
	}
}

func TestCheckIfaces(t *testing.T) {
	trunk := func(vid int, parent string) compile.VLAN {
		return compile.VLAN{VID: vid, Attach: compile.AttachTrunk, Parent: parent}
	}
	dev := func(vid int, name string) compile.VLAN {
		return compile.VLAN{VID: vid, Attach: compile.AttachDevice, Device: name}
	}
	snap := func(v ...compile.VLAN) *compile.Snapshot { return &compile.Snapshot{VLANs: v} }
	mgmt := map[string]bool{"eth0": true}
	for _, c := range []struct {
		name    string
		s       *compile.Snapshot
		allowed string
		ok      bool
	}{
		{"auto: trunk VLANs and another device", snap(trunk(1101, "ens19"), trunk(1102, "ens19"), dev(2000, "ens20")), "", true},
		{"auto: management interface as device", snap(trunk(1101, "ens19"), dev(4000, "eth0")), "", false},
		{"auto: management interface as trunk", snap(trunk(1101, "eth0")), "", false},
		{"auto: loopback", snap(dev(1, "lo")), "", false},
		{"list: trunk and its VLANs", snap(trunk(1101, "ens19"), trunk(1102, "ens19")), "ens19", true},
		{"list: single VLAN interface", snap(trunk(1101, "ens19")), "ens19.1101", true},
		{"list: other VLAN of the trunk", snap(trunk(1102, "ens19")), "ens19.1101", false},
		{"list: management interface named", snap(trunk(1254, "ens19"), dev(4000, "eth0")), "eth0, ens19", true},
		{"list: device not named", snap(trunk(1254, "ens19"), dev(4000, "eth0")), "ens19", false},
		{"list: glob", snap(dev(1, "ens20"), dev(2, "ens21")), "ens2*", true},
		{"list: glob does not match", snap(dev(1, "ens30")), "ens2*", false},
	} {
		err := CheckIfaces(c.s, ParseIfaceList(c.allowed), mgmt)
		if (err == nil) != c.ok {
			t.Errorf("%s: %v", c.name, err)
		}
		if err != nil && !errors.Is(err, ErrIfaceNotAllowed) {
			t.Errorf("%s: error is not ErrIfaceNotAllowed: %v", c.name, err)
		}
	}
}

func TestGuardGC(t *testing.T) {
	c := newClock()
	g := guardAt(c)
	g.AllowFlow("r", netip.MustParseAddr("10.10.1.1"), 1, 5, 10, c.now())
	c.add(2 * time.Minute)
	g.GC(c.now())
	n := 0
	for i := range g.buckets {
		n += len(g.buckets[i].m)
	}
	if n != 0 {
		t.Errorf("%d buckets left after GC", n)
	}
}

// BenchmarkProcessGuarded measures the whole decision path with every protection on, for a frame that passes all
// of them and is copied to three VLANs; the no-op emit stands for the send syscalls. The clock advances one
// millisecond per frame, so no rate limit drops it - without that the benchmark would mostly measure drops.
func BenchmarkProcessGuarded(b *testing.B) {
	now := time.Unix(1_700_000_000, 0)
	g := NewGuard(func() time.Time { return now })
	g.Holdoff = 0
	s := testSnapshot(defaultRules()...)
	s.Limits = compile.Limits{VLANPPS: maxVLANPPS, VLANBurst: maxVLANBurst, StormPPS: maxStormPPS, DedupMS: 200}
	for i := range s.Rules {
		s.Rules[i].RatePPS, s.Rules[i].RateBurst = maxRulePPS, maxRuleBurst // widest limits the engine allows
	}
	tb, _ := NewTable(s, nil)
	c := &Counters{}
	p := NewProcessor(c, g)
	fr := frame("10.10.1.10", "255.255.255.255", 50000, 23757, make([]byte, 106))
	emit := func(int, []byte) error { return nil }
	b.ReportAllocs()
	for b.Loop() {
		now = now.Add(time.Millisecond)
		p.Process(tb, t1, fr, emit)
	}
	if rx, relayed := c.RX.Load(), c.Reasons[Relayed].Load(); relayed != rx {
		b.Fatalf("only %d of %d frames relayed: the benchmark measures drops", relayed, rx)
	}
}

func TestAlarmAck(t *testing.T) {
	c := newClock()
	var a Alarms
	a.Raise(AlarmForeignRelay, "1101", c.now(), "x")
	a.Raise(AlarmForeignRelay, "1102", c.now(), "x")
	a.Raise(AlarmLoop, "bc:1", c.now(), "x")
	if n := a.Ack(AlarmForeignRelay, "op", c.now()); n != 2 {
		t.Fatalf("acked %d", n)
	}
	// keeps occurring without a pause: stays acknowledged
	c.add(10 * time.Second)
	a.Raise(AlarmForeignRelay, "1101", c.now(), "x")
	for _, x := range a.List() {
		if x.Kind == AlarmForeignRelay && (x.AckedAt.IsZero() || x.AckedBy != "op") {
			t.Errorf("continuing alarm lost its acknowledgement: %+v", x)
		}
		if x.Kind == AlarmLoop && !x.AckedAt.IsZero() {
			t.Error("other kind acknowledged too")
		}
	}
	// quiet for longer than 60 s, then again: new incident, open again
	c.add(2 * time.Minute)
	a.Raise(AlarmForeignRelay, "1101", c.now(), "x")
	for _, x := range a.List() {
		if x.Kind == AlarmForeignRelay && x.Key == "1101" && !x.AckedAt.IsZero() {
			t.Error("new incident after a quiet gap is still acknowledged")
		}
	}
}

func TestPlayers(t *testing.T) {
	c := newClock()
	g := guardAt(c)
	tb := newTable(t, limitsSnapshot(testSnapshot(defaultRules()...)))
	p := NewProcessor(&Counters{}, g)
	for range 3 {
		process(p, tb, t1, frame("10.10.1.10", "255.255.255.255", 50000, 23757, []byte("q")))
	}
	process(p, tb, t2, frame("10.10.2.20", "10.10.2.255", 23757, 23757, make([]byte, 106)))
	process(p, tb, t1, frame("10.10.1.10", "255.255.255.255", 1, 4711, []byte("unknown port")))
	process(p, tb, t1, frame("10.10.2.99", "255.255.255.255", 1, 23757, []byte("foreign")))
	list := g.Players.List()
	if len(list) != 2 {
		t.Fatalf("players %+v", list)
	}
	for _, pl := range list {
		if pl.Rule != "bc:23757" || (pl.VID == 1101 && pl.Count != 3) {
			t.Errorf("player %+v", pl)
		}
	}
	c.add(16 * time.Minute)
	g.Players.GC(c.now())
	if n := len(g.Players.List()); n != 0 {
		t.Errorf("%d players after GC", n)
	}
	var ps Players
	for i := range maxPlayers + 10 {
		ps.Seen("r", netip.AddrFrom4([4]byte{10, byte(i >> 16), byte(i >> 8), byte(i)}), 1, c.now().Add(time.Duration(i)))
	}
	if n := len(ps.List()); n != maxPlayers {
		t.Errorf("table not bounded: %d", n)
	}
}

// A relay for other ports (mDNS next to the games) is no dual-active conflict; a version 1 peer (ports unknown)
// and a peer with an overlapping port still are.
func TestDualActiveOnlyOnOverlap(t *testing.T) {
	c := newClock()
	g := guardAt(c)
	g.SetPorts(&compile.Snapshot{Rules: []compile.Rule{{Ports: "23757"}, {Ports: "27015-27020"}}})
	earlier := func(ports ...PortRange) Beacon {
		b := Beacon{Started: g.Started.Add(-time.Hour).UnixNano(), Active: true, Ports: ports}
		b.Instance[0] = 0xEE
		return b
	}
	if g.SeeBeacon(earlier(PortRange{5353, 5353}), c.now()) || g.Standby() {
		t.Fatal("went to standby for an mDNS-only relay")
	}
	v1 := ParseBeaconV1ForTest(earlier())
	if !g.SeeBeacon(v1, c.now()) || !g.Standby() {
		t.Fatal("version 1 peer (ports unknown) did not count as a conflict")
	}
	g2 := guardAt(c)
	g2.SetPorts(&compile.Snapshot{Rules: []compile.Rule{{Ports: "23757"}, {Ports: "27015-27020"}}})
	if !g2.SeeBeacon(earlier(PortRange{27020, 27030}), c.now()) || !g2.Standby() {
		t.Fatal("overlapping peer did not put this engine into standby")
	}
}

// ParseBeaconV1ForTest builds a version 1 frame from b and parses it, like a beacon from an old engine.
func ParseBeaconV1ForTest(b Beacon) Beacon {
	f := BuildBeacon(pkt.MAC{2, 0, 0, 0, 0, 2}, b)
	f[pkt.EthHeaderLen+4] = 1
	got, _ := ParseBeacon(f[:pkt.EthHeaderLen+beaconLen])
	return got
}

func TestRelayedPortsTooMany(t *testing.T) {
	var rules []compile.Rule
	for p := 1000; p < 1000+2*(beaconMaxRanges+1); p += 2 {
		rules = append(rules, compile.Rule{Ports: strconv.Itoa(p)})
	}
	if _, all := RelayedPorts(&compile.Snapshot{Rules: rules}); !all {
		t.Error("too many ranges must count as all ports")
	}
}
