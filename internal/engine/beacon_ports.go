package engine

import (
	"sort"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/portspec"
)

// The UDP ports in a beacon: two relays only conflict when the ports they relay overlap.

// PortRange is an inclusive range of UDP ports.
type PortRange struct{ Lo, Hi uint16 }

type beaconPorts struct {
	all    bool
	ranges []PortRange
}

// RelayedPorts returns the merged UDP destination port ranges of all rules of a snapshot.
func RelayedPorts(s *compile.Snapshot) (ranges []PortRange, all bool) {
	var ports []uint16
	for _, r := range s.Rules {
		ps, err := portspec.Parse(r.Ports)
		if err != nil {
			return nil, true // cannot tell: conflict with everybody
		}
		ports = append(ports, ps.Ports()...)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	ranges = mergePorts(ports)
	if len(ranges) > beaconMaxRanges {
		return nil, true
	}
	return ranges, false
}

// mergePorts turns sorted ports into ranges of consecutive ports.
func mergePorts(sorted []uint16) []PortRange {
	var ranges []PortRange
	for _, p := range sorted {
		n := len(ranges)
		if n == 0 || !ranges[n-1].reaches(p) {
			ranges = append(ranges, PortRange{p, p})
			continue
		}
		ranges[n-1].Hi = max(ranges[n-1].Hi, p)
	}
	return ranges
}

// reaches reports whether port p lies in the range or right after it (without wrapping past 65535).
func (r PortRange) reaches(p uint16) bool {
	return r.Hi != 65535 && p <= r.Hi+1
}

// portsOverlap reports whether two port sets share a port; "all" overlaps anything, also an empty set
// (an engine without rules sends nothing, but it may get rules any second).
func portsOverlap(aAll bool, a []PortRange, bAll bool, b []PortRange) bool {
	return aAll || bAll || rangesOverlap(a, b)
}

// rangesOverlap reports whether two sorted, merged range lists share a port.
func rangesOverlap(a, b []PortRange) bool {
	for i, j := 0, 0; i < len(a) && j < len(b); {
		if a[i].Hi < b[j].Lo {
			i++
		} else if b[j].Hi < a[i].Lo {
			j++
		} else {
			return true
		}
	}
	return false
}

// SetPorts records the ports of the applied snapshot (sent in the beacon, compared with peers).
func (g *Guard) SetPorts(s *compile.Snapshot) {
	r, all := RelayedPorts(s)
	g.ports.Store(&beaconPorts{all: all, ranges: r})
}

func (g *Guard) myPorts() *beaconPorts {
	if p := g.ports.Load(); p != nil {
		return p
	}
	return &beaconPorts{all: true} // before the first snapshot: conflict with everybody
}
