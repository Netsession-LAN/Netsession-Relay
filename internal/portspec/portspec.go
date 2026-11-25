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
	body, step, err := cutStep(item)
	if err != nil {
		return Range{}, err
	}
	lo, hi, err := parseRange(body)
	if err != nil {
		return Range{}, err
	}
	return Range{Lo: lo, Hi: hi, Step: step}, nil
}

// cutStep splits "lo-hi/step" into the range and its step; without "/step" the step is 1.
func cutStep(item string) (string, uint16, error) {
	body, st, ok := strings.Cut(item, "/")
	if !ok {
		return item, 1, nil
	}
	step, err := parsePort(st)
	if err != nil {
		return "", 0, fmt.Errorf("step: %w", err)
	}
	if !strings.Contains(body, "-") {
		return "", 0, fmt.Errorf("step without range in %q", item)
	}
	return body, step, nil
}

// parseRange parses "lo-hi" or a single port (lo = hi).
func parseRange(s string) (lo, hi uint16, err error) {
	loS, hiS, isRange := strings.Cut(s, "-")
	if lo, err = parsePort(loS); err != nil || !isRange {
		return lo, lo, err
	}
	if hi, err = parsePort(hiS); err != nil {
		return 0, 0, err
	}
	if hi < lo {
		return 0, 0, fmt.Errorf("range %d-%d is reversed", lo, hi)
	}
	return lo, hi, nil
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
