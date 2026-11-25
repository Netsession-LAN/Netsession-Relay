//go:build linux

package engine

import (
	"net/netip"
	"testing"
)

func TestParseFinderSrc(t *testing.T) {
	if a, err := parseFinderSrc(""); err != nil || a.IsValid() {
		t.Errorf("empty: %v %v", a, err)
	}
	if a, err := parseFinderSrc("10.10.1.10"); err != nil || a != netip.MustParseAddr("10.10.1.10") {
		t.Errorf("IPv4: %v %v", a, err)
	}
	for _, s := range []string{"fe80::1", "10.10.1", "x"} {
		if _, err := parseFinderSrc(s); err == nil || err.Error() != `bad sender address "`+s+`"` {
			t.Errorf("%q: %v", s, err)
		}
	}
}
