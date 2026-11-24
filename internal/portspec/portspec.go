// Package portspec parses and formats port specifications such as "27015-27020", "2302-2470/12" and comma lists.
//
// Grammar:
//
//	spec  = item *("," item)
//	item  = port | port "-" port [ "/" step ]
//
// Ports are 1..65535. A range "lo-hi/step" contains lo, lo+step, ... up to and including hi when hi-lo is a
// multiple of step (otherwise up to the last member below hi). Whitespace around items is ignored.
package portspec

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Range is one item of a spec. Lo == Hi for a single port. Step is at least 1.
type Range struct {
	Lo, Hi, Step uint16
}

// Spec is a list of ranges. The zero value is an empty spec.
type Spec []Range

// Parse parses a portspec. An empty or blank string is an error.
func Parse(s string) (Spec, error) {
	if strings.TrimSpace(s) == "" {
		return nil, errors.New("portspec: empty")
	}
	var spec Spec
	for _, item := range strings.Split(s, ",") {
		r, err := parseItem(strings.TrimSpace(item))
		if err != nil {
			return nil, fmt.Errorf("portspec %q: %w", s, err)
		}
		spec = append(spec, r)
	}
	return spec, nil
}

func parseItem(item string) (Range, error) {
	if item == "" {
		return Range{}, errors.New("empty item")
	}
	step := uint16(1)
	if body, st, ok := strings.Cut(item, "/"); ok {
		v, err := parsePort(st)
		if err != nil {
			return Range{}, fmt.Errorf("step: %w", err)
		}
		if !strings.Contains(body, "-") {
			return Range{}, fmt.Errorf("step without range in %q", item)
		}
		step, item = v, body
	}
	loS, hiS, isRange := strings.Cut(item, "-")
	lo, err := parsePort(loS)
	if err != nil {
		return Range{}, err
	}
	hi := lo
	if isRange {
		if hi, err = parsePort(hiS); err != nil {
			return Range{}, err
		}
		if hi < lo {
			return Range{}, fmt.Errorf("range %d-%d is reversed", lo, hi)
		}
	}
	return Range{Lo: lo, Hi: hi, Step: step}, nil
}

func parsePort(s string) (uint16, error) {
	v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	if err != nil || v < 1 || v > 65535 {
		return 0, fmt.Errorf("invalid port %q (want 1..65535)", s)
	}
	return uint16(v), nil
}

// Ports returns all ports of the spec, sorted and without duplicates.
func (s Spec) Ports() []uint16 {
	var out []uint16
	for _, r := range s {
		for p := uint32(r.Lo); p <= uint32(r.Hi); p += uint32(r.Step) {
			out = append(out, uint16(p))
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// Contains reports whether port p is in the spec.
func (s Spec) Contains(p uint16) bool {
	for _, r := range s {
		if p >= r.Lo && p <= r.Hi && (p-r.Lo)%r.Step == 0 {
			return true
		}
	}
	return false
}

// Len returns the number of distinct ports.
func (s Spec) Len() int { return len(s.Ports()) }

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
		j := i
		for j+1 < len(ps) && ps[j+1] == ps[j]+1 {
			j++
		}
		if j > i {
			out = append(out, Range{ps[i], ps[j], 1})
			i = j + 1
			continue
		}
		if i+2 < len(ps) {
			step := ps[i+1] - ps[i]
			j = i + 1
			for j+1 < len(ps) && ps[j+1]-ps[j] == step {
				j++
			}
			if j-i >= 2 {
				out = append(out, Range{ps[i], ps[j], step})
				i = j + 1
				continue
			}
		}
		out = append(out, Range{ps[i], ps[i], 1})
		i++
	}
	return out
}
