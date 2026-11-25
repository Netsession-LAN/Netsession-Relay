package engine

import (
	"net/netip"
	"testing"
	"time"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

// The benchmarks below cover the per-frame helpers that BenchmarkProcessGuarded does not reach on its path:
// the port finder while it records, the server probe and the beacon of the dual-active guard.

func BenchmarkFinderObserve(b *testing.B) {
	now := time.Unix(1_700_000_000, 0)
	var f Finder
	if err := f.Start(FinderRequest{Duration: FinderMaxDuration}, now); err != nil {
		b.Fatal(err)
	}
	raw := frame("10.10.1.10", "255.255.255.255", 50000, 23757, make([]byte, 106))
	fr, err := pkt.Parse(raw)
	if err != nil {
		b.Fatal(err)
	}
	for range finderMaxFrames { // the capture buffer is full: from here on a frame is only counted
		f.Observe(1101, &fr, raw, "bc:23757", now)
	}
	b.ReportAllocs()
	for b.Loop() {
		f.Observe(1101, &fr, raw, "bc:23757", now)
	}
}

func BenchmarkProberLearn(b *testing.B) {
	now := time.Unix(1_700_000_000, 0)
	var p Prober
	payload := []byte("\xff\xff\xff\xffgetinfo xxx")
	p.Learn("bc:27960-27963", 27960, payload, now)
	b.ReportAllocs()
	for b.Loop() {
		p.Learn("bc:27960-27963", 27960, payload, now)
	}
}

func BenchmarkProberReply(b *testing.B) {
	now := time.Unix(1_700_000_000, 0)
	var p Prober
	p.Seed("bc:27015-27020", []uint16{27015}, now)
	p.Queries(now)
	v := probeVLAN()
	reply := pkt.Frame{DstMAC: probeRelayMAC, SrcMAC: probeServerMAC, TTL: 64, Src: netip.MustParseAddr("10.10.1.10"),
		Dst: netip.MustParseAddr("10.10.1.55"), SrcPort: 27015, DstPort: ProbePort, UDPChecksum: 1, Payload: a2sInfoReply()}
	fr, err := pkt.Build(nil, &reply)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		p.Reply(v, fr, now)
	}
	if s := p.Servers(); len(s) != 1 || s[0].Name == "" {
		b.Fatalf("servers %+v", s)
	}
}

func BenchmarkParseBeacon(b *testing.B) {
	g := NewGuard(nil)
	g.SetPorts(&compile.Snapshot{Rules: []compile.Rule{{Ports: "23757"}, {Ports: "2302-2470,47624"}, {Ports: "27015-27020"}}})
	f := BuildBeacon(pkt.MAC{2, 0, 0, 0, 0, 1}, g.MyBeacon(&compile.Snapshot{Mode: compile.ModeActive}))
	b.ReportAllocs()
	for b.Loop() {
		if _, ok := ParseBeacon(f); !ok {
			b.Fatal("beacon rejected")
		}
	}
}

func BenchmarkStormTick(b *testing.B) {
	now := time.Unix(1_700_000_000, 0)
	g := NewGuard(func() time.Time { return now })
	s := testSnapshot(defaultRules()...)
	s.Limits.StormPPS = maxStormPPS
	tb, _ := NewTable(s, nil)
	c := &Counters{}
	b.ReportAllocs()
	for b.Loop() {
		c.Sent.Add(1000)
		tb.rules[0].copies[t1].Add(400)
		tb.rules[1].copies[t2].Add(40)
		now = now.Add(time.Second)
		g.StormTick(tb, c, now)
	}
}
