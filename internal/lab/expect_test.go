package lab

import (
	"bytes"
	"net/netip"
	"testing"

	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

func TestExpectMatches(t *testing.T) {
	a, b := netip.MustParseAddr("10.10.1.10"), netip.MustParseAddr("10.10.2.255")
	f := pkt.Frame{Src: a, Dst: b, DstPort: 23757}
	for _, c := range []struct {
		name string
		o    ExpectOptions
		want bool
	}{
		{"port only", ExpectOptions{DstPort: 23757}, true},
		{"other port", ExpectOptions{DstPort: 23758}, false},
		{"source fits", ExpectOptions{DstPort: 23757, Src: a}, true},
		{"other source", ExpectOptions{DstPort: 23757, Src: b}, false},
		{"destination fits", ExpectOptions{DstPort: 23757, Src: a, Dst: b}, true},
		{"other destination", ExpectOptions{DstPort: 23757, Dst: a}, false},
	} {
		if got := c.o.matches(f); got != c.want {
			t.Errorf("%s: matches = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestExpectResult(t *testing.T) {
	frames := func(n int) []pkt.Frame { return make([]pkt.Frame, n) }
	for _, c := range []struct {
		name   string
		o      ExpectOptions
		got    int
		enough bool
		ok     bool
	}{
		{"one wanted, one came", ExpectOptions{}, 1, true, true},
		{"timeout", ExpectOptions{Count: 2}, 1, false, false},
		{"range met", ExpectOptions{Count: 2, Max: 3}, 3, false, true},
		{"range exceeded", ExpectOptions{Max: 3}, 4, false, false},
		{"range not reached", ExpectOptions{Count: 2, Max: 3}, 1, false, false},
		{"nothing expected, nothing came", ExpectOptions{None: true}, 0, false, true},
		{"nothing expected, one came", ExpectOptions{None: true}, 1, false, false},
	} {
		got := frames(c.got)
		if e := c.o.enough(got); e != c.enough {
			t.Errorf("%s: enough = %v, want %v", c.name, e, c.enough)
		}
		if _, err := c.o.result(got); (err == nil) != c.ok {
			t.Errorf("%s: result error %v, want ok %v", c.name, err, c.ok)
		}
	}
}

func TestA2SAnswer(t *testing.T) {
	query := append([]byte{0xff, 0xff, 0xff, 0xff, 'T'}, "Source Engine Query\x00"...)
	ans, info, ok := a2sAnswer(query, "Lab")
	if !ok || info || !bytes.Equal(ans, []byte{0xff, 0xff, 0xff, 0xff, 'A', 0x4c, 0x61, 0x62, 0x21}) {
		t.Errorf("query without challenge: %x, info %v, ok %v", ans, info, ok)
	}
	ans, info, ok = a2sAnswer(append(append([]byte{}, query...), 0x4c, 0x61, 0x62, 0x21), "Lab")
	want := append([]byte{0xff, 0xff, 0xff, 0xff, 'I', 17}, "Lab\x00de_lab\x00cs2\x00Counter-Strike 2\x00"...)
	want = append(want, 0xda, 0x02, 3, 16, 0)
	if !ok || !info || !bytes.Equal(ans, want) {
		t.Errorf("query with challenge: %x, info %v, ok %v", ans, info, ok)
	}
	for _, p := range [][]byte{nil, []byte("hello"), append(append([]byte{}, query...), 1, 2, 3, 4)} {
		if _, _, ok := a2sAnswer(p, "Lab"); ok {
			t.Errorf("answered %x", p)
		}
	}
}
