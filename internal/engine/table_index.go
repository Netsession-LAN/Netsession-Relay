package engine

import (
	"fmt"
	"net/netip"

	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
)

// The index of the lookup table: the rule of a frame by its ports and multicast group.

type mcastKey struct {
	group netip.Addr
	port  uint16
}

// index makes the rule findable by its ports (and multicast group); a port may belong to one rule only.
func (t *Table) index(rt *ruleRT) error {
	ps, err := parsePorts(rt.Ports)
	if err != nil {
		return err
	}
	switch rt.Kind {
	case gamedb.KindBroadcast:
		return t.indexBroadcast(rt, ps)
	case gamedb.KindMulticast:
		return t.indexMulticast(rt, ps)
	}
	return fmt.Errorf("kind %q", rt.Kind)
}

func (t *Table) indexBroadcast(rt *ruleRT, ports []uint16) error {
	for _, p := range ports {
		if other := t.broadcast[p]; other != nil {
			return fmt.Errorf("port %d already used by %s", p, other.ID)
		}
		t.broadcast[p] = rt
	}
	return nil
}

func (t *Table) indexMulticast(rt *ruleRT, ports []uint16) error {
	g, err := parseMulticastGroup(rt.Group)
	if err != nil {
		return err
	}
	rt.group = g
	for _, p := range ports {
		k := mcastKey{g, p}
		if other := t.multicast[k]; other != nil {
			return fmt.Errorf("group %s port %d already used by %s", g, p, other.ID)
		}
		t.multicast[k] = rt
	}
	return nil
}

// parseMulticastGroup parses the IPv4 multicast group of a rule.
func parseMulticastGroup(s string) (netip.Addr, error) {
	g, err := netip.ParseAddr(s)
	if err != nil || !g.Is4() || !g.IsMulticast() {
		return netip.Addr{}, fmt.Errorf("bad group %q", s)
	}
	return g, nil
}
