package engine

import (
	"errors"
	"fmt"
	"path"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/netif"
)

// ErrIfaceNotAllowed marks a snapshot that names an interface the engine must not use.
var ErrIfaceNotAllowed = errors.New("interface not allowed")

// ParseIfaceList parses NSRELAY_ALLOWED_IFACES: interface names or glob patterns (path.Match, e.g. "ens19",
// "ens2*"), separated by commas or spaces.
func ParseIfaceList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' })
}

func matchIface(patterns []string, name string) bool {
	for _, p := range patterns {
		if ok, err := path.Match(p, name); err == nil && ok {
			return true
		}
	}
	return false
}

// CheckIfaces decides whether the engine may use the interfaces a snapshot names. The snapshot comes from the
// web part, the interfaces belong to the host, so the engine has the last word:
//
//   - With a list (NSRELAY_ALLOWED_IFACES) only listed interfaces are used: a device must match, a trunk VLAN
//     is fine when its trunk or its own name (<trunk>.<vid>) matches.
//   - Without a list every interface is fine except the protected ones: the loopback and the interfaces that
//     carry a default route, i.e. the management side of the host. Neither as a device nor as a trunk. A relay
//     that really shall work on that interface names it in the list.
func CheckIfaces(s *compile.Snapshot, allowed []string, protected map[string]bool) error {
	for _, v := range s.VLANs {
		if err := checkIface(v, allowed, protected); err != nil {
			return err
		}
	}
	return nil
}

// checkIface applies the interface policy of CheckIfaces to one VLAN.
func checkIface(v compile.VLAN, allowed []string, protected map[string]bool) error {
	base := v.Device
	if v.Attach == compile.AttachTrunk {
		base = v.Parent
	}
	if len(allowed) > 0 {
		return checkListedIface(v, base, allowed)
	}
	if base == "lo" || protected[base] {
		return fmt.Errorf("VLAN %d: %w: %s carries the default route of the host (management); to relay on it anyway, list it in NSRELAY_ALLOWED_IFACES of the engine",
			v.VID, ErrIfaceNotAllowed, base)
	}
	return nil
}

// checkListedIface accepts a VLAN whose interface (base: the device or the trunk) or own name is in the list.
func checkListedIface(v compile.VLAN, base string, allowed []string) error {
	if matchIface(allowed, base) || matchIface(allowed, netif.LinkName(v)) {
		return nil
	}
	return fmt.Errorf("VLAN %d: %w: %s is not in NSRELAY_ALLOWED_IFACES", v.VID, ErrIfaceNotAllowed, base)
}
