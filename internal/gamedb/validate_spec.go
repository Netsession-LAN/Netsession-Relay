package gamedb

import (
	"net/netip"

	"github.com/Netsession-LAN/Netsession-Relay/internal/portspec"
)

// The checks of one discovery spec: ports, kind and multicast group, the enumerated fields, rate and payload limit.

// specProblems checks one discovery spec.
func specProblems(s Spec) problems {
	var p problems
	p.checkSpecPorts(s.Ports)
	p.checkKind(s)
	for _, f := range specEnums {
		f.check(&p, s)
	}
	if !validRate(s.RatePPS, s.RateBurst) {
		p.add("rate_pps/rate_burst invalid (%d/%d)", s.RatePPS, s.RateBurst)
	}
	if s.PayloadMax < 0 || s.PayloadMax > DefaultPayloadMax {
		p.add("payload_max %d out of range 1..%d", s.PayloadMax, DefaultPayloadMax)
	}
	return p
}

// checkSpecPorts parses the ports of a spec and refuses the blocked ones.
func (p *problems) checkSpecPorts(ports string) {
	ps, err := portspec.Parse(ports)
	if err != nil {
		*p = append(*p, err)
		return
	}
	for _, b := range blockedPorts {
		if ps.Contains(b) {
			p.add("port %d: %w", b, ErrBlockedPort)
		}
	}
}

// checkKind checks the kind of a spec and the multicast group that goes with it.
func (p *problems) checkKind(s Spec) {
	switch s.Kind {
	case KindBroadcast:
		if s.Group != "" {
			p.add("broadcast spec must not have a group")
		}
	case KindMulticast:
		if !isIPv4Multicast(s.Group) {
			p.add("group %q is not an IPv4 multicast address", s.Group)
		}
	default:
		p.add("kind %q invalid", s.Kind)
	}
}

func isIPv4Multicast(s string) bool {
	a, err := netip.ParseAddr(s)
	return err == nil && a.Is4() && a.IsMulticast()
}

// validRate reports whether a rate limit makes sense: nothing negative, and a burst (when set) at least the rate.
func validRate(pps, burst int) bool {
	if pps < 0 || burst < 0 {
		return false
	}
	return burst == 0 || burst >= pps
}
