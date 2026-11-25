package lab

import (
	"fmt"

	"github.com/Netsession-LAN/Netsession-Relay/internal/pkt"
)

// matches reports whether a received frame is one lab expect waits for: the destination port and, when set,
// the source and destination address.
func (o ExpectOptions) matches(f pkt.Frame) bool {
	if f.DstPort != o.DstPort {
		return false
	}
	if o.Src.IsValid() && f.Src != o.Src {
		return false
	}
	return !o.Dst.IsValid() || f.Dst == o.Dst
}

// want is the number of matching frames needed (at least one).
func (o ExpectOptions) want() int { return max(o.Count, 1) }

// enough reports whether waiting can stop early: only when neither a range (Max) nor silence (None) is expected
// and the needed number of frames arrived.
func (o ExpectOptions) enough(got []pkt.Frame) bool {
	return !o.None && o.Max == 0 && len(got) >= o.want()
}

// result judges the frames that arrived by the end of the wait.
func (o ExpectOptions) result(got []pkt.Frame) ([]pkt.Frame, error) {
	switch {
	case o.Max > 0:
		return got, o.rangeError(len(got))
	case o.None:
		if len(got) > 0 {
			return got, fmt.Errorf("expected nothing on %s port %d, got %d frame(s)", o.Iface, o.DstPort, len(got))
		}
		return nil, nil
	case len(got) >= o.want():
		return got, nil
	}
	return got, fmt.Errorf("timeout on %s: got %d of %d frame(s) for port %d", o.Iface, len(got), o.want(), o.DstPort)
}

// rangeError reports n frames outside the expected range want..Max.
func (o ExpectOptions) rangeError(n int) error {
	if want := o.want(); n < want || n > o.Max {
		return fmt.Errorf("%s port %d: got %d frame(s), want %d..%d", o.Iface, o.DstPort, n, want, o.Max)
	}
	return nil
}
