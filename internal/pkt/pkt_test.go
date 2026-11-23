package pkt

import (
	"bytes"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

// sampleFrame is a FlatOut-style discovery broadcast 10.10.1.20:50000 -> 255.255.255.255:23757.
// Checksums are checked via the zero-sum property (verifyUDP, header fold == 0xffff).
func sampleFrame() Frame {
	return Frame{
		DstMAC:      BroadcastMAC,
		SrcMAC:      MAC{0x02, 0, 0, 0, 0, 0x14},
		TOS:         0,
		ID:          0x1234,
		TTL:         128,
		Src:         netip.MustParseAddr("10.10.1.20"),
		Dst:         netip.MustParseAddr("255.255.255.255"),
		SrcPort:     50000,
		DstPort:     23757,
		UDPChecksum: 1, // non-zero: compute
		Payload:     []byte("FO2?"),
	}
}

func TestBuildParseRoundTrip(t *testing.T) {
	f := sampleFrame()
	b, err := Build(nil, &f)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 14+20+8+4 {
		t.Fatalf("len = %d", len(b))
	}
	g, err := Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if g.Src != f.Src || g.Dst != f.Dst || g.SrcPort != f.SrcPort || g.DstPort != f.DstPort ||
		g.TTL != f.TTL || g.ID != f.ID || g.DstMAC != f.DstMAC || g.SrcMAC != f.SrcMAC ||
		!bytes.Equal(g.Payload, f.Payload) {
		t.Fatalf("round trip mismatch: %+v vs %+v", g, f)
	}
	// IPv4 header checksum: summing the header including the checksum yields 0xffff.
	if fold(sum(0, b[14:34])) != 0xffff {
		t.Error("IPv4 header checksum invalid")
	}
	// UDP checksum verifies over pseudo header + segment.
	if !verifyUDP(g.Src, g.Dst, b[34:]) {
		t.Error("UDP checksum invalid")
	}
}

func verifyUDP(src, dst netip.Addr, seg []byte) bool {
	s4, d4 := src.As4(), dst.As4()
	acc := sum(0, s4[:])
	acc = sum(acc, d4[:])
	acc += ProtoUDP + uint32(len(seg))
	acc = sum(acc, seg)
	return fold(acc) == 0xffff
}

// Reference frame 192.168.0.1:1234 -> 192.168.0.255:5678, payload "hello", DF set, TTL 64.
// IPv4 header checksum worked out by hand per RFC 1071:
// 4500+0021+0000+4000+4011+c0a8+0001+c0a8+00ff = 0x24782 -> fold 0x4784 -> complement 0xb87b.
const knownFrame = "ffffffffffff020000000001" + "0800" +
	"45000021000040004011b87bc0a80001c0a800ff" + // IPv4
	"04d2162e000d0000" + // UDP, checksum zero (not computed by sender)
	"68656c6c6f"

func TestKnownChecksums(t *testing.T) {
	raw, err := hex.DecodeString(strings.ReplaceAll(knownFrame, " ", ""))
	if err != nil {
		t.Fatal(err)
	}
	f, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if f.Src.String() != "192.168.0.1" || f.DstPort != 5678 || string(f.Payload) != "hello" || !f.DontFragment {
		t.Fatalf("parsed %+v", f)
	}
	// Rebuilding without changes must reproduce the frame byte for byte (UDP checksum stays zero).
	b, err := Build(nil, &f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b, raw) {
		t.Fatalf("rebuild differs:\n got %x\nwant %x", b, raw)
	}
	// With UDP checksum requested, the result must verify.
	f.UDPChecksum = 1
	if b, err = Build(nil, &f); err != nil {
		t.Fatal(err)
	}
	if !verifyUDP(f.Src, f.Dst, b[34:]) {
		t.Error("UDP checksum invalid")
	}
}

func TestZeroUDPChecksumKept(t *testing.T) {
	f := sampleFrame()
	f.UDPChecksum = 0
	b, _ := Build(nil, &f)
	if b[40] != 0 || b[41] != 0 {
		t.Errorf("UDP checksum = %02x%02x, want 0000", b[40], b[41])
	}
}

func TestBuildReusesBuffer(t *testing.T) {
	f := sampleFrame()
	buf := make([]byte, 0, 2048)
	b, _ := Build(buf, &f)
	if &b[0] != &buf[:1][0] {
		t.Error("Build did not reuse buffer")
	}
}

func TestParseErrors(t *testing.T) {
	f := sampleFrame()
	good, _ := Build(nil, &f)
	mut := func(fn func(b []byte)) []byte { b := bytes.Clone(good); fn(b); return b }
	tests := []struct {
		name string
		b    []byte
		want error
	}{
		{"short", good[:30], ErrShort},
		{"arp", mut(func(b []byte) { b[12], b[13] = 0x08, 0x06 }), ErrNotIPv4},
		{"ipv6 version", mut(func(b []byte) { b[14] = 0x65 }), ErrNotIPv4},
		{"tcp", mut(func(b []byte) { b[23] = 6 }), ErrNotUDP},
		{"more fragments", mut(func(b []byte) { b[20] = 0x20 }), ErrFragment},
		{"frag offset", mut(func(b []byte) { b[21] = 0x01 }), ErrFragment},
		{"total too big", mut(func(b []byte) { b[16], b[17] = 0x05, 0xdc }), ErrBadIPHeader},
		{"ihl too small", mut(func(b []byte) { b[14] = 0x44 }), ErrBadIPHeader},
		{"udp len too big", mut(func(b []byte) { b[38], b[39] = 0, 200 }), ErrBadUDP},
		{"udp len too small", mut(func(b []byte) { b[38], b[39] = 0, 4 }), ErrBadUDP},
	}
	for _, tt := range tests {
		if _, err := Parse(tt.b); !errors.Is(err, tt.want) {
			t.Errorf("%s: err = %v, want %v", tt.name, err, tt.want)
		}
	}
}

func TestParseIgnoresPaddingAndOptions(t *testing.T) {
	f := sampleFrame()
	b, _ := Build(nil, &f)
	padded := append(bytes.Clone(b), make([]byte, 20)...) // Ethernet padding
	g, err := Parse(padded)
	if err != nil || !bytes.Equal(g.Payload, f.Payload) {
		t.Fatalf("padding: %v %q", err, g.Payload)
	}
	// insert 4 bytes of IPv4 options (NOP x4), adjust IHL and total length
	opt := append(append(bytes.Clone(b[:34]), 1, 1, 1, 1), b[34:]...)
	opt[14] = 0x46
	opt[17] += 4
	g, err = Parse(opt)
	if err != nil || g.DstPort != 23757 || !bytes.Equal(g.Payload, f.Payload) {
		t.Fatalf("options: %v %+v", err, g)
	}
}

func TestSubnetBroadcast(t *testing.T) {
	tests := map[string]string{
		"10.10.1.0/24":   "10.10.1.255",
		"10.10.1.77/24":  "10.10.1.255",
		"10.8.0.0/14":    "10.11.255.255",
		"10.10.0.0/16":   "10.10.255.255",
		"192.168.1.4/30": "192.168.1.7",
		"10.0.0.1/32":    "10.0.0.1",
	}
	for p, want := range tests {
		if got := SubnetBroadcast(netip.MustParsePrefix(p)); got.String() != want {
			t.Errorf("SubnetBroadcast(%s) = %s, want %s", p, got, want)
		}
	}
	p := netip.MustParsePrefix("10.10.1.0/24")
	if !IsBroadcastFor(netip.MustParseAddr("10.10.1.255"), p) || !IsBroadcastFor(LimitedBroadcast, p) {
		t.Error("IsBroadcastFor false negative")
	}
	if IsBroadcastFor(netip.MustParseAddr("10.10.2.255"), p) || IsBroadcastFor(netip.MustParseAddr("10.10.1.254"), p) {
		t.Error("IsBroadcastFor false positive")
	}
}

func TestMulticastMAC(t *testing.T) {
	tests := map[string]string{
		"224.0.2.60":      "01:00:5e:00:02:3c",
		"239.255.255.250": "01:00:5e:7f:ff:fa",
		"224.0.0.251":     "01:00:5e:00:00:fb",
	}
	for g, want := range tests {
		m := MulticastMAC(netip.MustParseAddr(g))
		if m.String() != want || !m.IsGroup() {
			t.Errorf("MulticastMAC(%s) = %s, want %s", g, m, want)
		}
	}
	if (MAC{0x02, 0, 0, 0, 0, 1}).IsGroup() {
		t.Error("unicast MAC reported as group")
	}
}

func FuzzParse(f *testing.F) {
	fr := sampleFrame()
	b, _ := Build(nil, &fr)
	f.Add(b)
	f.Fuzz(func(t *testing.T, b []byte) {
		g, err := Parse(b)
		if err != nil {
			return
		}
		g.UDPChecksum = 1
		out, err := Build(nil, &g)
		if err != nil {
			t.Fatal(err)
		}
		h, err := Parse(out)
		if err != nil || h.DstPort != g.DstPort || !bytes.Equal(h.Payload, g.Payload) {
			t.Fatalf("rebuild not parseable: %v", err)
		}
	})
}
