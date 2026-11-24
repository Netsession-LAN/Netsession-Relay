package compile

import (
	"cmp"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"unicode"

	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
)

// Source filters: which senders of a VLAN a game's rule relays.

// gameSources checks the source filter of a game and returns it masked.
func (c *compiler) gameSources(g gamedb.Game, in []netip.Prefix) []netip.Prefix {
	var sources []netip.Prefix
	for _, p := range in {
		if !p.IsValid() || !p.Addr().Is4() {
			c.fail("game %q: source %s is not an IPv4 address or network", g.ID, p)
			continue
		}
		p = p.Masked()
		if !slices.ContainsFunc(c.vlans, func(v VLAN) bool { return PrefixWithin(p, v.CIDR) }) {
			c.warn(WarnSourceNoVLAN, "%s: source filter %s lies in no network, ignored", g.ID, FormatSource(p))
		}
		sources = append(sources, p)
	}
	return sources
}

// mergeSources combines the source filters of the games sharing one port. Per VLAN: if every game restricts
// it, the union applies; if one game has no prefix there, the VLAN stays unrestricted ("open wins", so a
// filter never silently cuts off another game) and the second return value describes the conflict.
func mergeSources(games []string, byGame map[string][]netip.Prefix, vlans []VLAN) ([]netip.Prefix, string) {
	if len(byGame) == 0 {
		return nil, ""
	}
	var out []netip.Prefix
	var open []string
	for _, v := range vlans {
		f := filterIn(games, byGame, v.CIDR)
		switch {
		case len(f.restricted) == 0:
		case len(f.unrestricted) > 0:
			open = append(open, fmt.Sprintf("VLAN %d: %s filter, %s not", v.VID, strings.Join(f.restricted, ", "), strings.Join(f.unrestricted, ", ")))
		default:
			out = append(out, f.union...)
		}
	}
	SortSources(out)
	out = slices.Compact(out)
	if len(open) == 0 {
		return out, ""
	}
	return out, fmt.Sprintf("source filter ignored (%s), those VLANs stay unrestricted", strings.Join(open, "; "))
}

// vlanFilter is what the games of one port want inside one VLAN network.
type vlanFilter struct {
	union        []netip.Prefix // the prefixes of all games inside the network
	restricted   []string       // games with at least one prefix inside the network
	unrestricted []string       // games without
}

func filterIn(games []string, byGame map[string][]netip.Prefix, network netip.Prefix) vlanFilter {
	var f vlanFilter
	for _, g := range games {
		inside := prefixesWithin(byGame[g], network)
		if len(inside) == 0 {
			f.unrestricted = append(f.unrestricted, g)
			continue
		}
		f.union = append(f.union, inside...)
		f.restricted = append(f.restricted, g)
	}
	return f
}

// prefixesWithin returns the prefixes of ps that lie completely inside network n.
func prefixesWithin(ps []netip.Prefix, n netip.Prefix) []netip.Prefix {
	var out []netip.Prefix
	for _, p := range ps {
		if PrefixWithin(p, n) {
			out = append(out, p)
		}
	}
	return out
}

// PrefixWithin reports whether p lies completely inside network n.
func PrefixWithin(p, n netip.Prefix) bool {
	return p.IsValid() && n.IsValid() && n.Bits() <= p.Bits() && n.Contains(p.Addr())
}

// SortSources sorts prefixes by address, then by length.
func SortSources(ps []netip.Prefix) {
	slices.SortFunc(ps, func(a, b netip.Prefix) int {
		return cmp.Or(a.Addr().Compare(b.Addr()), cmp.Compare(a.Bits(), b.Bits()))
	})
}

// FormatSource writes a single address without "/32".
func FormatSource(p netip.Prefix) string {
	if p.IsSingleIP() {
		return p.Addr().String()
	}
	return p.String()
}

// FormatSources formats a source filter, one string per prefix (the inverse of ParseSources).
func FormatSources(ps []netip.Prefix) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, FormatSource(p))
	}
	return out
}

// ParseSources reads a source filter as typed by an admin: IPv4 addresses or networks, separated by commas,
// semicolons or whitespace. The result is masked, sorted and free of duplicates; empty text is no filter.
func ParseSources(text string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, f := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ';' || unicode.IsSpace(r) }) {
		p, ok := parseSource(f)
		if !ok {
			return nil, fmt.Errorf("%q is not an IPv4 address or network", f)
		}
		out = append(out, p.Masked())
	}
	SortSources(out)
	return slices.Compact(out), nil
}

// parseSource reads one IPv4 network, or one IPv4 address as a network of its own.
func parseSource(f string) (netip.Prefix, bool) {
	if strings.Contains(f, "/") {
		p, err := netip.ParsePrefix(f)
		return p, err == nil && p.Addr().Is4()
	}
	a, err := netip.ParseAddr(f)
	return netip.PrefixFrom(a, a.BitLen()), err == nil && a.Is4()
}
