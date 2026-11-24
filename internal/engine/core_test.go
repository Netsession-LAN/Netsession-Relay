package engine

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"testing"

	"github.com/Netsession2024/Netsession-Relay/internal/compile"
	"github.com/Netsession2024/Netsession-Relay/internal/pkt"
)

// Lab layout: gs=1100 (gameserver), t1..t3=1101..1103 (tische), wlan=1253.
func testSnapshot(rules ...compile.Rule) *compile.Snapshot {
	mk := func(vid int, group string, third int) compile.VLAN {
		return compile.VLAN{VID: vid, Name: fmt.Sprint(vid), Group: group, Attach: compile.AttachTrunk, Parent: "trunk0",
			CIDR:    netip.MustParsePrefix(fmt.Sprintf("10.10.%d.0/24", third)),
			RelayIP: netip.MustParseAddr(fmt.Sprintf("10.10.%d.55", third))}
	}
	return &compile.Snapshot{
		Version: compile.SnapshotVersion, Mode: compile.ModeActive,
		VLANs: []compile.VLAN{mk(1100, "gameserver", 0), mk(1101, "tische", 1), mk(1102, "tische", 2), mk(1103, "tische", 3), mk(1253, "wlan", 253)},
		Rules: rules,
	}
}

var gamesFlows = []compile.Edge{{Src: "tische", Dst: "tische"}, {Src: "tische", Dst: "gameserver"}, {Src: "gameserver", Dst: "tische"}}

func rule(id, kind, group, ports, src, dst string) compile.Rule {
	return compile.Rule{ID: id, Kind: kind, Group: group, Ports: ports, Specs: []string{id}, SourceMode: src, DstMode: dst,
		TTLMode: "keep", Flows: gamesFlows, RatePPS: 5, RateBurst: 10, PayloadMax: 1472}
}

func defaultRules() []compile.Rule {
	return []compile.Rule{
		rule("bc:23757", "broadcast", "", "23757", "original", "keep"),
		rule("bc:27015-27020", "broadcast", "", "27015-27020", "original", "keep"),
		rule("bc:5000", "broadcast", "", "5000", "relay", "subnet"),
		rule("bc:5001", "broadcast", "", "5001", "original", "limited"),
		rule("mc:224.0.2.60:4445", "multicast", "224.0.2.60", "4445", "original", "keep"),
	}
}

const (
	gs, t1, t2, t3, wlan = 0, 1, 2, 3, 4
)

var relayMAC = pkt.MAC{0x02, 0xaa, 0, 0, 0, 0x55}

func newTable(t *testing.T, s *compile.Snapshot) *Table {
	t.Helper()
	macs := map[int]pkt.MAC{}
	for _, v := range s.VLANs {
		macs[v.VID] = relayMAC
	}
	tb, err := NewTable(s, macs)
	if err != nil {
		t.Fatal(err)
	}
	return tb
}

func frame(src, dst string, sport, dport uint16, payload []byte) []byte {
	f := pkt.Frame{DstMAC: pkt.BroadcastMAC, SrcMAC: pkt.MAC{0x02, 0, 0, 0, 0, 0x10}, TTL: 128,
		Src: netip.MustParseAddr(src), Dst: netip.MustParseAddr(dst), SrcPort: sport, DstPort: dport,
		UDPChecksum: 1, Payload: payload}
	if f.Dst.IsMulticast() {
		f.DstMAC = pkt.MulticastMAC(f.Dst)
	}
	b, err := pkt.Build(nil, &f)
	if err != nil {
		panic(err)
	}
	return b
}

type sent struct {
	out int
	f   pkt.Frame
}

func run(t *testing.T, tb *Table, in int, b []byte) (Reason, []sent) {
	t.Helper()
	var got []sent
	p := NewProcessor(&Counters{}, nil)
	r := p.Process(tb, in, b, func(out int, fr []byte) error {
		f, err := pkt.Parse(append([]byte{}, fr...))
		if err != nil {
			t.Fatalf("emitted unparsable frame: %v", err)
		}
		got = append(got, sent{out, f})
		return nil
	})
	return r, got
}

func outs(s []sent) []int {
	var o []int
	for _, x := range s {
		o = append(o, x.out)
	}
	slices.Sort(o)
	return o
}

func TestFlatOutQuery(t *testing.T) {
	tb := newTable(t, testSnapshot(defaultRules()...))
	r, got := run(t, tb, t1, frame("10.10.1.10", "255.255.255.255", 50000, 23757, []byte("FO2-query")))
	if r != Relayed {
		t.Fatalf("reason = %v", r)
	}
	if !slices.Equal(outs(got), []int{gs, t2, t3}) {
		t.Fatalf("targets = %v, want gs,t2,t3 (not t1, not wlan)", outs(got))
	}
	for _, s := range got {
		if s.f.Src.String() != "10.10.1.10" || s.f.Dst != pkt.LimitedBroadcast || s.f.SrcPort != 50000 || s.f.DstPort != 23757 {
			t.Errorf("out %d: %+v", s.out, s.f)
		}
		if s.f.SrcMAC != relayMAC || s.f.DstMAC != pkt.BroadcastMAC || s.f.TTL != 128 || string(s.f.Payload) != "FO2-query" {
			t.Errorf("out %d header: %+v", s.out, s.f)
		}
	}
}

func TestFlatOutAnswerSubnetBroadcast(t *testing.T) {
	tb := newTable(t, testSnapshot(defaultRules()...))
	payload := make([]byte, 106)
	r, got := run(t, tb, t2, frame("10.10.2.20", "10.10.2.255", 23757, 23757, payload))
	if r != Relayed || !slices.Equal(outs(got), []int{gs, t1, t3}) {
		t.Fatalf("reason=%v targets=%v", r, outs(got))
	}
	for _, s := range got {
		want := tb.VLANs[s.out].Broadcast // keep: subnet broadcast becomes the target's subnet broadcast
		if s.f.Dst != want || len(s.f.Payload) != 106 {
			t.Errorf("out %d dst=%s want %s", s.out, s.f.Dst, want)
		}
	}
}

func TestDstAndSourceModes(t *testing.T) {
	tb := newTable(t, testSnapshot(defaultRules()...))
	// subnet + relay: limited broadcast in -> subnet broadcast out, source = relay IP of target VLAN
	_, got := run(t, tb, t1, frame("10.10.1.10", "255.255.255.255", 1234, 5000, []byte("x")))
	for _, s := range got {
		v := tb.VLANs[s.out]
		if s.f.Dst != v.Broadcast || s.f.Src != v.RelayIP {
			t.Errorf("5000 out %d: src=%s dst=%s", s.out, s.f.Src, s.f.Dst)
		}
	}
	// limited: subnet broadcast in -> 255.255.255.255 out
	_, got = run(t, tb, t1, frame("10.10.1.10", "10.10.1.255", 1234, 5001, []byte("x")))
	if len(got) == 0 {
		t.Fatal("nothing sent for 5001")
	}
	for _, s := range got {
		if s.f.Dst != pkt.LimitedBroadcast || s.f.Src.String() != "10.10.1.10" {
			t.Errorf("5001 out %d: src=%s dst=%s", s.out, s.f.Src, s.f.Dst)
		}
	}
}

func TestSourceQueryKeepsLimited(t *testing.T) {
	tb := newTable(t, testSnapshot(defaultRules()...))
	a2s := append([]byte{0xff, 0xff, 0xff, 0xff}, []byte("TSource Engine Query\x00")...)
	r, got := run(t, tb, t3, frame("10.10.3.30", "255.255.255.255", 27005, 27015, a2s))
	if r != Relayed || len(got) != 3 {
		t.Fatalf("reason=%v n=%d", r, len(got))
	}
	for _, s := range got {
		if s.f.Dst != pkt.LimitedBroadcast {
			t.Errorf("dst %s", s.f.Dst)
		}
	}
}

func TestMulticast(t *testing.T) {
	tb := newTable(t, testSnapshot(defaultRules()...))
	r, got := run(t, tb, t1, frame("10.10.1.10", "224.0.2.60", 4445, 4445, []byte("[MOTD]x[/MOTD][AD]25565[/AD]")))
	if r != Relayed || len(got) != 3 {
		t.Fatalf("reason=%v n=%d", r, len(got))
	}
	for _, s := range got {
		if s.f.Dst.String() != "224.0.2.60" || s.f.DstMAC != pkt.MulticastMAC(s.f.Dst) {
			t.Errorf("multicast out: %+v", s.f)
		}
	}
}

func TestDrops(t *testing.T) {
	s := testSnapshot(append(defaultRules(), rule("bc:9999", "broadcast", "", "9999", "original", "keep"))...)
	tb := newTable(t, s)
	own := frame("10.10.1.10", "255.255.255.255", 1, 23757, []byte("x"))
	copy(own[6:12], relayMAC[:])
	big := make([]byte, 1473)
	tests := []struct {
		name string
		in   int
		b    []byte
		want Reason
	}{
		{"own mac", t1, own, DropOwnMAC},
		{"dhcp discover", t1, frame("10.10.1.10", "255.255.255.255", 68, 67, []byte("x")), DropBlocked},
		{"dhcp from 0.0.0.0", t1, frame("0.0.0.0", "255.255.255.255", 5555, 23757, []byte("x")), DropBlocked},
		{"foreign relay", t1, frame("10.10.2.99", "255.255.255.255", 1, 23757, []byte("x")), DropForeignRelay},
		{"unknown source net", t1, frame("192.168.1.5", "255.255.255.255", 1, 23757, []byte("x")), DropSrcSubnet},
		{"directed bcast of other vlan", t1, frame("10.10.1.10", "10.10.2.255", 1, 23757, []byte("x")), DropNotBroadcast},
		{"unicast", t1, frame("10.10.1.10", "10.10.1.20", 1, 23757, []byte("x")), DropNotBroadcast},
		{"unknown port", t1, frame("10.10.1.10", "255.255.255.255", 1, 4711, []byte("x")), DropUnknownPort},
		{"unknown group", t1, frame("10.10.1.10", "239.1.2.3", 1, 4445, []byte("x")), DropUnknownPort},
		{"wlan not in matrix", wlan, frame("10.10.253.7", "255.255.255.255", 1, 23757, []byte("x")), DropNoTarget},
		{"empty payload", t1, frame("10.10.1.10", "255.255.255.255", 1, 23757, nil), DropSize},
		{"too big", t1, frame("10.10.1.10", "255.255.255.255", 1, 23757, big), DropSize},
		{"garbage", t1, []byte{1, 2, 3}, DropParse},
	}
	for _, tt := range tests {
		r, got := run(t, tb, tt.in, tt.b)
		if r != tt.want || len(got) != 0 {
			t.Errorf("%s: reason=%v sent=%d, want %v and nothing sent", tt.name, r, len(got), tt.want)
		}
	}
}

func TestUnknownPortCounted(t *testing.T) {
	tb := newTable(t, testSnapshot(defaultRules()...))
	c := &Counters{}
	p := NewProcessor(c, nil)
	for range 3 {
		p.Process(tb, t1, frame("10.10.1.10", "255.255.255.255", 1, 4711, []byte("x")), func(int, []byte) error { return nil })
	}
	if c.UnknownPorts[4711].Load() != 3 || c.Reasons[DropUnknownPort].Load() != 3 || c.RX.Load() != 3 {
		t.Errorf("unknown=%d reason=%d rx=%d", c.UnknownPorts[4711].Load(), c.Reasons[DropUnknownPort].Load(), c.RX.Load())
	}
}

func TestObserveModeSendsNothing(t *testing.T) {
	s := testSnapshot(defaultRules()...)
	s.Mode = compile.ModeObserve
	tb := newTable(t, s)
	r, got := run(t, tb, t1, frame("10.10.1.10", "255.255.255.255", 1, 23757, []byte("x")))
	if r != DropMode || len(got) != 0 {
		t.Errorf("observe: reason=%v sent=%d", r, len(got))
	}
	if st := tb.RuleStats(); st[0].RX != 1 || st[0].Relayed != 0 {
		t.Errorf("observe must still count rx: %+v", st[0])
	}
}

func TestTTLDecrement(t *testing.T) {
	r := rule("bc:7000", "broadcast", "", "7000", "original", "keep")
	r.TTLMode = "decrement"
	tb := newTable(t, testSnapshot(r))
	_, got := run(t, tb, t1, frame("10.10.1.10", "255.255.255.255", 1, 7000, []byte("x")))
	if len(got) == 0 || got[0].f.TTL != 127 {
		t.Fatalf("ttl not decremented: %+v", got)
	}
	b := frame("10.10.1.10", "255.255.255.255", 1, 7000, []byte("x"))
	f, _ := pkt.Parse(b)
	f.TTL = 1
	b, _ = pkt.Build(nil, &f)
	if reason, _ := run(t, tb, t1, b); reason != DropTTL {
		t.Errorf("ttl 1: %v", reason)
	}
}

func TestNewTableRejectsDuplicatePort(t *testing.T) {
	s := testSnapshot(rule("a", "broadcast", "", "1000-1010", "original", "keep"), rule("b", "broadcast", "", "1005", "original", "keep"))
	if _, err := NewTable(s, nil); err == nil {
		t.Error("overlapping broadcast rules accepted")
	}
}

func TestNewTableRejectsDuplicateMulticast(t *testing.T) {
	s := testSnapshot(rule("a", "multicast", "224.0.2.60", "4445-4450", "original", "keep"), rule("b", "multicast", "224.0.2.60", "4447", "original", "keep"))
	if _, err := NewTable(s, nil); err == nil {
		t.Error("overlapping multicast rules for the same group accepted")
	}
	s = testSnapshot(rule("a", "multicast", "224.0.2.60", "4445", "original", "keep"), rule("b", "multicast", "224.0.0.251", "4445", "original", "keep"))
	if _, err := NewTable(s, nil); err != nil {
		t.Errorf("same port in two groups rejected: %v", err)
	}
}

// BenchmarkProcess measures the basic pipeline without the guard (no loop detection, rate limits or storm
// brake): parse, rule lookup and three copies. BenchmarkProcessGuarded is the number that matters in production.
func BenchmarkProcess(b *testing.B) {
	s := testSnapshot(defaultRules()...)
	tb, _ := NewTable(s, nil)
	c := &Counters{}
	p := NewProcessor(c, nil)
	fr := frame("10.10.1.10", "255.255.255.255", 50000, 23757, make([]byte, 106))
	emit := func(int, []byte) error { return nil }
	b.ReportAllocs()
	for b.Loop() {
		p.Process(tb, t1, fr, emit)
	}
	if rx, relayed := c.RX.Load(), c.Reasons[Relayed].Load(); relayed != rx {
		b.Fatalf("only %d of %d frames relayed", relayed, rx)
	}
}

func TestSourceFilter(t *testing.T) {
	r := rule("bc:23757", "broadcast", "", "23757", "original", "keep")
	r.SrcAllow = []netip.Prefix{netip.MustParsePrefix("10.10.1.20/32"), netip.MustParsePrefix("10.10.9.0/24")}
	tb := newTable(t, testSnapshot(r))
	// Tisch 1 is restricted: only 10.10.1.20 is relayed
	if got, s := run(t, tb, t1, frame("10.10.1.20", "255.255.255.255", 50000, 23757, []byte("q"))); got != Relayed || len(s) != 3 {
		t.Errorf("allowed source: %v, %d copies", got, len(s))
	}
	if got, s := run(t, tb, t1, frame("10.10.1.21", "255.255.255.255", 50000, 23757, []byte("q"))); got != DropSrcFilter || len(s) != 0 {
		t.Errorf("other source in the restricted VLAN: %v, %d copies", got, len(s))
	}
	// Tisch 2 has no prefix inside its network: unrestricted
	if got, _ := run(t, tb, t2, frame("10.10.2.5", "255.255.255.255", 50000, 23757, []byte("q"))); got != Relayed {
		t.Errorf("unrestricted VLAN: %v", got)
	}
	if DropSrcFilter.String() != "src_filter" {
		t.Errorf("reason name %q", DropSrcFilter)
	}
}

// A copy that cannot be sent is counted, whether the other copies went out or not; "no target" stays what it
// says: the matrix names no network for the frame.
func TestSendErrorsAreCounted(t *testing.T) {
	tb := newTable(t, testSnapshot(defaultRules()...))
	fr := frame("10.10.1.10", "255.255.255.255", 50000, 23757, []byte("q"))
	down := errors.New("network is down")

	// one of three target networks is down: the frame counts as relayed, and the failure is not lost
	c := &Counters{}
	p := NewProcessor(c, nil)
	got := p.Process(tb, t1, fr, func(out int, _ []byte) error {
		if out == t2 {
			return down
		}
		return nil
	})
	if got != Relayed || c.Sent.Load() != 2 || c.Reasons[Relayed].Load() != 1 || c.Reasons[DropSendError].Load() != 1 {
		t.Errorf("one copy failed: %v, sent %d, reasons %v", got, c.Sent.Load(), c.ReasonCounts())
	}
	if c.VLANOut[1102].Load() != 0 || c.VLANOut[1103].Load() != 1 || c.VLANOut[1100].Load() != 1 {
		t.Errorf("per-network counters after a failed copy: 1100=%d 1102=%d 1103=%d", c.VLANOut[1100].Load(), c.VLANOut[1102].Load(), c.VLANOut[1103].Load())
	}

	// every copy fails: not relayed, and not "no target" either
	c = &Counters{}
	p = NewProcessor(c, nil)
	if got := p.Process(tb, t1, fr, func(int, []byte) error { return down }); got != DropSendError {
		t.Errorf("all copies failed: %v, want %v", got, DropSendError)
	}
	if c.Sent.Load() != 0 || c.Reasons[DropSendError].Load() != 1 || c.Reasons[DropNoTarget].Load() != 0 || c.Reasons[Relayed].Load() != 0 {
		t.Errorf("all copies failed: sent %d, reasons %v", c.Sent.Load(), c.ReasonCounts())
	}

	// no network in the matrix for frames from the WiFi: that is "no target", nothing was tried
	c = &Counters{}
	p = NewProcessor(c, nil)
	wifi := frame("10.10.253.10", "255.255.255.255", 50000, 23757, []byte("q"))
	if got := p.Process(tb, wlan, wifi, func(int, []byte) error { t.Error("sent although no network is a target"); return nil }); got != DropNoTarget {
		t.Errorf("frame without a target network: %v", got)
	}
	if DropSendError.String() != "send_error" || DropPanic.String() != "panic" {
		t.Errorf("reason names %q %q", DropSendError, DropPanic)
	}
}

// The per-VLAN counters are indexed by VID, received and sent alike: a snapshot with a VID outside 1..4094 is
// refused when the table is built, so no frame can count into a wrong slot or outside the array.
func TestTableRefusesVIDOutOfRange(t *testing.T) {
	for _, vid := range []int{-1, 0, 4095, 4096, 70000} {
		s := testSnapshot(defaultRules()...)
		s.VLANs[0].VID = vid
		if _, err := NewTable(s, nil); err == nil {
			t.Errorf("VID %d accepted", vid)
		}
	}
	s := testSnapshot(defaultRules()...)
	s.VLANs[0].VID, s.VLANs[1].VID = 1, MaxVID
	tb := newTable(t, s)
	c := &Counters{}
	p := NewProcessor(c, nil)
	if got := p.Process(tb, t1, frame("10.10.1.10", "255.255.255.255", 50000, 23757, []byte("q")), func(int, []byte) error { return nil }); got != Relayed {
		t.Fatalf("frame on the highest VID: %v", got)
	}
	if c.VLANIn[MaxVID].Load() != 1 || c.VLANOut[1].Load() != 1 {
		t.Errorf("counters of VID %d and 1: in %d, out %d", MaxVID, c.VLANIn[MaxVID].Load(), c.VLANOut[1].Load())
	}
}

// The names of the reasons are an interface (status, metrics, SNMP table): every reason has one, none twice.
func TestReasonNames(t *testing.T) {
	seen := map[string]bool{}
	for r := Relayed; r < numReasons; r++ {
		name := r.String()
		if name == "" || seen[name] {
			t.Errorf("reason %d: name %q missing or used twice", int(r), name)
		}
		seen[name] = true
	}
}
