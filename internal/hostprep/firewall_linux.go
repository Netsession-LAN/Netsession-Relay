//go:build linux

package hostprep

import (
	"strings"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

// The nftables table of the engine: nothing is forwarded from or to the relay interfaces, and from them the host
// accepts only ping, IGMP and answers to its own connections.

// TableName is the nftables table (family inet) the engine owns and rewrites on every apply. It is a different
// table than "nsrelay" of deploy/host/setup.sh; both may exist, a packet has to pass both.
const TableName = "nsrelay_engine"

func ifname(n string) []byte {
	b := make([]byte, 16)
	copy(b, n)
	return b
}

// syncFirewall replaces the table of the engine in one transaction; without relay interfaces the table is
// removed. If the kernel has no connection tracking for nftables, the table is installed without the rule
// for answers.
func syncFirewall(ifaces []string) error {
	err := writeFirewall(ifaces, true)
	if err != nil && len(ifaces) > 0 {
		if err2 := writeFirewall(ifaces, false); err2 == nil {
			return nil
		}
	}
	return err
}

// firewallCurrent reports whether the table exists with exactly these interfaces (and both chains).
func firewallCurrent(ifaces []string) bool {
	c, err := nftables.New()
	if err != nil {
		return false
	}
	t, err := c.ListTableOfFamily(TableName, nftables.TableFamilyINet)
	if err != nil || t == nil {
		return len(ifaces) == 0
	}
	return len(ifaces) > 0 && setHoldsExactly(c, t, ifaces) && chainsComplete(c, t)
}

// setHoldsExactly reports whether the interface set of the table holds exactly these interfaces.
func setHoldsExactly(c *nftables.Conn, t *nftables.Table, ifaces []string) bool {
	have, err := relaySetNames(c, t)
	if err != nil || len(have) != len(ifaces) {
		return false
	}
	for _, n := range ifaces {
		if !have[n] {
			return false
		}
	}
	return true
}

// relaySetNames reads the interface names in the set of the table.
func relaySetNames(c *nftables.Conn, t *nftables.Table) (map[string]bool, error) {
	set, err := c.GetSetByName(t, "relay_if")
	if err != nil {
		return nil, err
	}
	elems, err := c.GetSetElements(set)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, e := range elems {
		have[strings.TrimRight(string(e.Key), "\x00")] = true
	}
	return have, nil
}

// chainsComplete reports whether the input and forward chains exist with at least their two rules each.
func chainsComplete(c *nftables.Conn, t *nftables.Table) bool {
	for _, name := range []string{"input", "forward"} {
		if !chainComplete(c, t, name) {
			return false
		}
	}
	return true
}

func chainComplete(c *nftables.Conn, t *nftables.Table, name string) bool {
	ch, err := c.ListChain(t, name)
	if err != nil || ch == nil {
		return false
	}
	rules, err := c.GetRules(t, ch)
	return err == nil && len(rules) >= 2
}

func writeFirewall(ifaces []string, conntrack bool) error {
	c, err := nftables.New()
	if err != nil {
		return err
	}
	t := &nftables.Table{Family: nftables.TableFamilyINet, Name: TableName}
	c.AddTable(t) // so that deleting never fails
	c.DelTable(t)
	if len(ifaces) == 0 {
		return c.Flush()
	}
	c.AddTable(t)
	// the byte order only matters to "nft list", which otherwise prints the names as empty strings
	set := &nftables.Set{Table: t, Name: "relay_if", KeyType: nftables.TypeIFName, KeyByteOrder: binaryutil.NativeEndian}
	elems := make([]nftables.SetElement, len(ifaces))
	for i, n := range ifaces {
		elems[i] = nftables.SetElement{Key: ifname(n)}
	}
	if err := c.AddSet(set, elems); err != nil {
		return err
	}
	addFirewallRules(c, t, set, conntrack)
	return c.Flush()
}

// addFirewallRules adds the input and forward chains with their rules for the interfaces in set.
func addFirewallRules(c *nftables.Conn, t *nftables.Table, set *nftables.Set, conntrack bool) {
	accept := nftables.ChainPolicyAccept
	input := c.AddChain(&nftables.Chain{Name: "input", Table: t, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookInput, Priority: nftables.ChainPriorityFilter, Policy: &accept})
	forward := c.AddChain(&nftables.Chain{Name: "forward", Table: t, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookForward, Priority: nftables.ChainPriorityFilter, Policy: &accept})

	from := func() []expr.Any {
		return []expr.Any{&expr.Meta{Key: expr.MetaKeyIIFNAME, Register: 1},
			&expr.Lookup{SourceRegister: 1, SetName: set.Name, SetID: set.ID}}
	}
	to := []expr.Any{&expr.Meta{Key: expr.MetaKeyOIFNAME, Register: 1},
		&expr.Lookup{SourceRegister: 1, SetName: set.Name, SetID: set.ID}}
	l4 := func(proto byte) []expr.Any {
		return []expr.Any{&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}}}
	}
	rule := func(ch *nftables.Chain, parts ...[]expr.Any) {
		var e []expr.Any
		for _, p := range parts {
			e = append(e, p...)
		}
		c.AddRule(&nftables.Rule{Table: t, Chain: ch, Exprs: e})
	}
	ok := []expr.Any{&expr.Verdict{Kind: expr.VerdictAccept}}
	no := func() []expr.Any { return []expr.Any{&expr.Counter{}, &expr.Verdict{Kind: expr.VerdictDrop}} }

	// input from a relay interface: ping, IGMP, answers; nothing else
	rule(input, from(), []expr.Any{&expr.Meta{Key: expr.MetaKeyNFPROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.NFPROTO_IPV4}}}, l4(unix.IPPROTO_ICMP),
		[]expr.Any{&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 0, Len: 1},
			&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{8}}}, ok) // echo request
	rule(input, from(), l4(unix.IPPROTO_IGMP), ok)
	if conntrack {
		rule(input, from(), []expr.Any{&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
			&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4,
				Mask: binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED), Xor: make([]byte, 4)},
			&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: make([]byte, 4)}}, ok)
	}
	rule(input, from(), no())
	// the host never routes from or to a relay interface
	rule(forward, from(), no())
	rule(forward, to, no())
}
