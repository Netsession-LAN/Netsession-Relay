package compile

import (
	"cmp"
	"errors"
	"fmt"
	"net/netip"
	"slices"
)

// ErrOverlap is part of the error of Compile when two enabled VLANs have networks that overlap (errors.Is).
var ErrOverlap = errors.New("networks overlap")

// checkVLANs validates the VLAN list and returns the enabled VLANs (sorted by VID) and the number of
// enabled VLANs per group (groups that only have disabled VLANs are present with 0).
func checkVLANs(in []VLAN) ([]VLAN, map[string]int, []error) {
	var errs []error
	groups := map[string]int{}
	seenVID := map[int]bool{}
	var out []VLAN
	for _, v := range in {
		name := fmt.Sprintf("VLAN %d (%s)", v.VID, v.Name)
		errs = append(errs, identityErrors(v, name, seenVID[v.VID])...)
		seenVID[v.VID] = true
		if !v.Enabled {
			groups[v.Group] += 0 // the group is known, even if none of its VLANs is on
			continue
		}
		groups[v.Group]++
		v, vErrs := normalizeVLAN(v, name)
		errs = append(errs, vErrs...)
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b VLAN) int { return cmp.Compare(a.VID, b.VID) })
	return out, groups, append(errs, overlapErrors(out)...)
}

// identityErrors checks what every VLAN needs, switched on or not: a valid, unique VID and a group.
func identityErrors(v VLAN, name string, vidSeen bool) []error {
	var errs []error
	if v.VID < 1 || v.VID > 4094 {
		errs = append(errs, fmt.Errorf("%s: VID out of range", name))
	}
	if vidSeen {
		errs = append(errs, fmt.Errorf("%s: VID used twice", name))
	}
	if v.Group == "" {
		errs = append(errs, fmt.Errorf("%s: no group", name))
	}
	return errs
}

// normalizeVLAN checks network, relay address and interface of a switched-on VLAN and returns it with the
// network masked and only the interface field of its attach mode kept.
func normalizeVLAN(v VLAN, name string) (VLAN, []error) {
	var errs []error
	if err := checkNetwork(v.CIDR, name); err != nil {
		errs = append(errs, err)
	}
	v.CIDR = v.CIDR.Masked()
	if !v.RelayIP.IsValid() || !v.CIDR.Contains(v.RelayIP) {
		errs = append(errs, fmt.Errorf("%s: relay IP %s not in %s", name, v.RelayIP, v.CIDR))
	}
	v, err := normalizeAttach(v, name)
	if err != nil {
		errs = append(errs, err)
	}
	return v, errs
}

// checkNetwork requires an IPv4 network with room for at least two hosts.
func checkNetwork(p netip.Prefix, name string) error {
	switch {
	case !p.IsValid() || !p.Addr().Is4():
		return fmt.Errorf("%s: IPv4 network missing", name)
	case p.Bits() > 30:
		return fmt.Errorf("%s: network %s too small", name, p)
	}
	return nil
}

// normalizeAttach checks the interface of the attach mode and clears the field the other mode uses.
func normalizeAttach(v VLAN, name string) (VLAN, error) {
	switch v.Attach {
	case AttachTrunk:
		v.Device = ""
		if v.Parent == "" {
			return v, fmt.Errorf("%s: trunk attach needs a parent interface", name)
		}
	case AttachDevice:
		v.Parent = ""
		if v.Device == "" {
			return v, fmt.Errorf("%s: device attach needs a device name", name)
		}
	default:
		return v, fmt.Errorf("%s: attach %q invalid", name, v.Attach)
	}
	return v, nil
}

// networksOverlap reports whether both VLANs have a network and the networks overlap.
func networksOverlap(a, b VLAN) bool {
	return a.CIDR.IsValid() && b.CIDR.IsValid() && a.CIDR.Overlaps(b.CIDR)
}

// overlapErrors reports every pair of VLANs (sorted by VID) whose networks overlap.
func overlapErrors(sorted []VLAN) []error {
	var errs []error
	for i, a := range sorted {
		for _, b := range sorted[i+1:] {
			if networksOverlap(a, b) {
				errs = append(errs, fmt.Errorf("VLAN %d and %d: %w: %s and %s", a.VID, b.VID, ErrOverlap, a.CIDR, b.CIDR))
			}
		}
	}
	return errs
}
