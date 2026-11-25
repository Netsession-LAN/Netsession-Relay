package portspec

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Formatting a spec: the canonical, compact form of a set of ports.

// String formats the spec as written; use FromPorts(s.Ports()).String() for the canonical form.
func (s Spec) String() string {
	parts := make([]string, 0, len(s))
	for _, r := range s {
		switch {
		case r.Lo == r.Hi:
			parts = append(parts, strconv.Itoa(int(r.Lo)))
		case r.Step == 1:
			parts = append(parts, fmt.Sprintf("%d-%d", r.Lo, r.Hi))
		default:
			parts = append(parts, fmt.Sprintf("%d-%d/%d", r.Lo, r.Hi, r.Step))
		}
	}
	return strings.Join(parts, ",")
}

// FromPorts builds the canonical, compact spec for a set of ports: consecutive runs become "lo-hi",
// arithmetic progressions with at least three members become "lo-hi/step", everything else single ports.
func FromPorts(ports []uint16) Spec {
	ps := slices.Clone(ports)
	slices.Sort(ps)
	ps = slices.Compact(ps)
	var out Spec
	for i := 0; i < len(ps); {
		var r Range
		r, i = nextRange(ps, i)
		out = append(out, r)
	}
	return out
}

// nextRange returns the range of the sorted ports ps that starts at index i, and the index after it: a run of
// consecutive ports, else a progression of at least three, else the single port.
func nextRange(ps []uint16, i int) (Range, int) {
	if j := runEnd(ps, i); j > i {
		return Range{ps[i], ps[j], 1}, j + 1
	}
	if j := progressionEnd(ps, i); j-i >= 2 {
		return Range{ps[i], ps[j], ps[i+1] - ps[i]}, j + 1
	}
	return Range{ps[i], ps[i], 1}, i + 1
}

// runEnd returns the index of the last port of the run of consecutive ports starting at i.
func runEnd(ps []uint16, i int) int {
	j := i
	for j+1 < len(ps) && ps[j+1] == ps[j]+1 {
		j++
	}
	return j
}

// progressionEnd returns the index of the last port of the arithmetic progression starting at i with the step
// ps[i+1]-ps[i]; i when fewer than three ports are left.
func progressionEnd(ps []uint16, i int) int {
	if i+2 >= len(ps) {
		return i
	}
	step := ps[i+1] - ps[i]
	j := i + 1
	for j+1 < len(ps) && ps[j+1]-ps[j] == step {
		j++
	}
	return j
}
