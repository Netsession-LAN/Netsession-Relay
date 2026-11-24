package engine

import (
	"bytes"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/net/bpf"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

var (
	probeRelayMAC  = pkt.MAC{0x02, 0, 0, 0, 0, 0x55}
	probeServerMAC = pkt.MAC{0x02, 0, 0, 0, 0, 0x10}
)

func probeVLAN() *VLANRuntime {
	return &VLANRuntime{VLAN: compile.VLAN{VID: 1101, CIDR: netip.MustParsePrefix("10.10.1.0/24"),
		RelayIP: netip.MustParseAddr("10.10.1.55")}, MAC: probeRelayMAC}
}

func probeFrame(t *testing.T, f pkt.Frame) []byte {
	t.Helper()
	if f.TTL == 0 {
		f.TTL = 64
	}
	b, err := pkt.Build(nil, &f)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func probeReply(t *testing.T, payload []byte) []byte {
	return probeFrame(t, pkt.Frame{DstMAC: probeRelayMAC, SrcMAC: probeServerMAC, Src: netip.MustParseAddr("10.10.1.10"),
		Dst: netip.MustParseAddr("10.10.1.55"), SrcPort: 27015, DstPort: ProbePort, UDPChecksum: 1, Payload: payload})
}

func a2sInfoReply() []byte {
	b := []byte{0xff, 0xff, 0xff, 0xff, 'I', 17}
	for _, s := range []string{"Netsession CS2 #1", "de_dust2", "csgo", "Counter-Strike 2"} {
		b = append(append(b, s...), 0)
	}
	return append(b, 0xda, 0x02, 7, 10, 0) // app id, players, max, bots
}

func TestProbeA2S(t *testing.T) {
	var p Prober
	now := time.Unix(1000, 0)
	v := probeVLAN()
	p.Seed("bc:27015-27020", []uint16{27015, 27016, 27017, 27018, 27019, 27020}, now)
	qs := p.Queries(now)
	if len(qs) != 6 || !bytes.Equal(qs[0].Payload, a2sInfo) {
		t.Fatalf("seed: %d queries", len(qs))
	}
	b, err := BuildProbe(nil, v, qs[0], 1)
	if err != nil {
		t.Fatal(err)
	}
	f, err := pkt.Parse(b)
	if err != nil || f.Dst != pkt.LimitedBroadcast || f.Src != v.RelayIP || f.SrcPort != ProbePort || f.DstPort != 27015 {
		t.Fatalf("probe frame %+v %v", f, err)
	}
	// challenge -> follow-up with the challenge, unicast to the server
	out := p.Reply(v, probeReply(t, []byte{0xff, 0xff, 0xff, 0xff, 'A', 1, 2, 3, 4}), now)
	if out == nil {
		t.Fatal("no follow-up for the challenge")
	}
	fu, _ := pkt.Parse(out)
	if fu.DstMAC != probeServerMAC || fu.Dst.String() != "10.10.1.10" || fu.DstPort != 27015 ||
		!bytes.Equal(fu.Payload, append(append([]byte(nil), a2sInfo...), 1, 2, 3, 4)) {
		t.Fatalf("follow-up %+v", fu)
	}
	if p.Reply(v, probeReply(t, a2sInfoReply()), now) != nil {
		t.Error("info answer needs no follow-up")
	}
	s := p.Servers()
	if len(s) != 1 || s[0].Name != "Netsession CS2 #1" || s[0].Map != "de_dust2" || s[0].Game != "Counter-Strike 2" || s[0].Num != 7 || s[0].Max != 10 ||
		s[0].Rule != "bc:27015-27020" || s[0].Port != 27015 || s[0].VID != 1101 {
		t.Fatalf("servers %+v", s)
	}
	p.GC(now.Add(probeTTL + time.Second))
	if len(p.Servers()) != 0 {
		t.Error("GC kept a silent server")
	}
}

func TestProbeRejects(t *testing.T) {
	var p Prober
	now := time.Unix(1000, 0)
	v := probeVLAN()
	// nothing probed yet: an answer from a port we never asked is ignored
	if p.Reply(v, probeReply(t, a2sInfoReply()), now); len(p.Servers()) != 0 {
		t.Fatal("answer without probe accepted")
	}
	p.Learn("bc:27015-27020", 27015, a2sInfo, now)
	p.Queries(now)
	// wrong destination port, foreign subnet, other MAC
	bad := []pkt.Frame{
		{DstMAC: probeRelayMAC, SrcMAC: probeServerMAC, Src: netip.MustParseAddr("10.10.1.10"), Dst: v.RelayIP, SrcPort: 27015, DstPort: 1234, Payload: []byte("x")},
		{DstMAC: probeRelayMAC, SrcMAC: probeServerMAC, Src: netip.MustParseAddr("10.10.2.10"), Dst: v.RelayIP, SrcPort: 27015, DstPort: ProbePort, Payload: []byte("x")},
		{DstMAC: probeServerMAC, SrcMAC: probeServerMAC, Src: netip.MustParseAddr("10.10.1.10"), Dst: v.RelayIP, SrcPort: 27015, DstPort: ProbePort, Payload: []byte("x")},
	}
	for i, f := range bad {
		p.Reply(v, probeFrame(t, f), now)
		if len(p.Servers()) != 0 {
			t.Fatalf("bad answer %d accepted", i)
		}
	}
	// a generic (non-A2S) answer still lists the server, without name
	p.Reply(v, probeReply(t, []byte("hello")), now)
	if s := p.Servers(); len(s) != 1 || s[0].Name != "" {
		t.Fatalf("generic answer: %+v", s)
	}
	// rule no longer a probe rule: forgotten
	p.Forget(map[string]bool{})
	if len(p.Servers()) != 0 || len(p.Queries(now)) != 0 {
		t.Error("Forget kept entries")
	}
}

func TestProbeLearn(t *testing.T) {
	var p Prober
	now := time.Unix(1000, 0)
	p.Learn("bc:27960", 27960, []byte("\xff\xff\xff\xffgetinfo xxx"), now)
	p.Learn("bc:27960", 27960, []byte("\xff\xff\xff\xffgetinfo yyy"), now) // newest payload wins
	p.Learn("bc:27015-27020", 27015, append(append([]byte(nil), a2sInfo...), 9, 9, 9, 9), now)
	p.Learn("bc:big", 1, make([]byte, maxProbeQuery+1), now) // too long: ignored
	qs := p.Queries(now)
	if len(qs) != 2 {
		t.Fatalf("queries %d", len(qs))
	}
	if !bytes.Equal(qs[0].Payload, a2sInfo) { // a client's challenged request is stored as the plain query
		t.Errorf("a2s query %q", qs[0].Payload)
	}
	if string(qs[1].Payload) != "\xff\xff\xff\xffgetinfo yyy" {
		t.Errorf("q3 query %q", qs[1].Payload)
	}
	for i := 0; i < maxProbePorts+5; i++ {
		p.Learn("bc:many", uint16(1000+i), []byte("q"), now.Add(time.Duration(i)*time.Second))
	}
	if n := len(p.Queries(now)); n != maxProbePorts {
		t.Errorf("bounded: %d", n)
	}
}

func TestRxFilter(t *testing.T) {
	vm, err := bpf.NewVM(rxFilter)
	if err != nil {
		t.Fatal(err)
	}
	src, dst := netip.MustParseAddr("10.10.1.10"), netip.MustParseAddr("10.10.1.55")
	cases := []struct {
		name string
		f    pkt.Frame
		pass bool
	}{
		{"broadcast udp", pkt.Frame{DstMAC: pkt.MAC{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, Src: src, Dst: pkt.LimitedBroadcast, SrcPort: 1, DstPort: 27015, Payload: []byte("x")}, true},
		{"unicast probe answer", pkt.Frame{DstMAC: probeRelayMAC, Src: src, Dst: dst, SrcPort: 27015, DstPort: ProbePort, Payload: []byte("x")}, true},
		{"unicast other port", pkt.Frame{DstMAC: probeRelayMAC, Src: src, Dst: dst, SrcPort: 27015, DstPort: 22, Payload: []byte("x")}, false},
	}
	for _, c := range cases {
		b := probeFrame(t, c.f)
		n, err := vm.Run(b)
		if err != nil {
			t.Fatal(err)
		}
		if (n > 0) != c.pass {
			t.Errorf("%s: pass=%v", c.name, n > 0)
		}
	}
	// unicast TCP to the probe port offset: rejected (protocol check)
	b := probeFrame(t, cases[1].f)
	b[23] = 6
	if n, _ := vm.Run(b); n != 0 {
		t.Error("unicast tcp passed")
	}
	// fragment rejected
	b = probeFrame(t, cases[1].f)
	b[20] |= 0x20
	if n, _ := vm.Run(b); n != 0 {
		t.Error("unicast fragment passed")
	}
}

// A host that answers every request with a challenge gets one follow-up per probe round, not a ping-pong.
func TestProbeFollowUpOncePerRound(t *testing.T) {
	var p Prober
	now := time.Unix(1000, 0)
	v := probeVLAN()
	p.Learn("bc:27015-27020", 27015, a2sInfo, now)
	p.Queries(now)
	challenge := probeReply(t, []byte{0xff, 0xff, 0xff, 0xff, 'A', 1, 2, 3, 4})
	if p.Reply(v, challenge, now) == nil {
		t.Fatal("no follow-up for the first challenge")
	}
	for i := 0; i < 3; i++ {
		if p.Reply(v, challenge, now) != nil {
			t.Fatal("second follow-up in the same round")
		}
	}
	p.Queries(now) // next round
	if p.Reply(v, challenge, now) == nil {
		t.Error("no follow-up in the next round")
	}
}

// After Forget, answers from the formerly probed port are ignored again.
func TestProbeForgetClosesPort(t *testing.T) {
	var p Prober
	now := time.Unix(1000, 0)
	v := probeVLAN()
	p.Learn("bc:27015-27020", 27015, a2sInfo, now)
	p.Queries(now)
	p.Forget(map[string]bool{})
	if p.Reply(v, probeReply(t, a2sInfoReply()), now); len(p.Servers()) != 0 {
		t.Error("answer accepted after the rule was forgotten")
	}
}

// Answers are only taken shortly after a round; learned queries expire unless clients send them again.
func TestProbeWindowAndExpiry(t *testing.T) {
	var p Prober
	now := time.Unix(1000, 0)
	v := probeVLAN()
	p.Learn("bc:27960", 27960, []byte("\xff\xff\xff\xffgetinfo"), now)
	p.Queries(now)
	late := probeFrame(t, pkt.Frame{DstMAC: probeRelayMAC, SrcMAC: probeServerMAC, Src: netip.MustParseAddr("10.10.1.10"),
		Dst: v.RelayIP, SrcPort: 27960, DstPort: ProbePort, Payload: []byte("x")})
	if p.Reply(v, late, now.Add(answerWindow+time.Second)); len(p.Servers()) != 0 {
		t.Fatal("answer outside the window accepted")
	}
	if p.Reply(v, late, now.Add(time.Second)); len(p.Servers()) != 1 {
		t.Fatal("answer inside the window rejected")
	}
	if qs := p.Queries(now.Add(queryTTL + time.Minute)); len(qs) != 0 {
		t.Errorf("expired query still replayed: %d", len(qs))
	}
}

// CoD and Quake 3 ports get the id Tech 3 query built in; the info answer gives name, map and players.
func TestProbeQ3(t *testing.T) {
	var p Prober
	now := time.Unix(1000, 0)
	v := probeVLAN()
	p.Seed("bc:28960-28963", []uint16{28960, 28961, 28962, 28963}, now)
	p.Seed("bc:27960-27963", []uint16{27960}, now)
	p.Seed("bc:23757", []uint16{23757}, now) // no built-in query
	qs := p.Queries(now)
	if len(qs) != 5 || !bytes.Equal(qs[0].Payload, q3GetInfo) {
		t.Fatalf("seed: %d queries", len(qs))
	}
	f, err := pkt.Parse(probeReply(t, []byte("\xff\xff\xff\xffinfoResponse\n\\hostname\\^1Netsession ^7CoD4\\mapname\\mp_crash\\clients\\3\\sv_maxclients\\18")))
	if err != nil {
		t.Fatal(err)
	}
	f.SrcPort = 28961
	b, _ := pkt.Build(nil, &f)
	if out := p.Reply(v, b, now); out != nil {
		t.Fatal("follow-up for an info answer")
	}
	srv := p.Servers()
	if len(srv) != 1 || srv[0].Port != 28961 || srv[0].Name != "Netsession CoD4" || srv[0].Map != "mp_crash" || srv[0].Num != 3 || srv[0].Max != 18 {
		t.Fatalf("servers %+v", srv)
	}
}
