//go:build linux

package hostprep

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/netif"
)

// TableName is the nftables table (family inet) the engine owns and rewrites on every apply. It is a different
// table than "nsrelay" of deploy/host/setup.sh; both may exist, a packet has to pass both.
const TableName = "nsrelay_engine"

// The engine owns this routing table and all policy rules with this priority (lower than 32766 of the main
// table, so they are asked first; a number nobody else is likely to use).
const (
	MgmtTable    = 7355
	MgmtPriority = 7355
)

// Apply prepares the host for the snapshot and returns what could not be done.
func Apply(s *compile.Snapshot) []Issue { return prepare(s, true) }

// Refresh repeats the preparation while the engine runs: somebody may have switched forwarding on for all
// interfaces (starting Docker does), flushed the firewall rules (a firewall reload on the host), or the
// management address arrived late (DHCP). The firewall table is only rewritten when it is missing or differs.
func Refresh(s *compile.Snapshot) []Issue { return prepare(s, false) }

func prepare(s *compile.Snapshot, rewrite bool) []Issue {
	var issues []Issue
	protected, err := netif.DefaultRouteLinks()
	if err != nil {
		// without the routes the management interface is unknown: do not risk shielding it
		return []Issue{{IssueFirewall, "cannot read the routes of the host: " + err.Error()}}
	}
	plan := PlanFor(s, protected, netif.HasIPv4)

	sysctlOK := true
	var sysErr, v6Err []string
	for _, name := range plan.Ifaces {
		if err := netif.HardenLink(name); err != nil {
			sysctlOK = false
			sysErr = append(sysErr, name+": "+err.Error())
		}
		if err := netif.DisableIPv6(name); err != nil {
			v6Err = append(v6Err, name+": "+err.Error())
		}
	}
	if len(sysErr) > 0 {
		issues = append(issues, Issue{IssueSysctl, strings.Join(sysErr, "; ")})
	}
	if len(v6Err) > 0 {
		issues = append(issues, Issue{IssueIPv6, strings.Join(v6Err, "; ")})
	}

	var fwErr error
	if rewrite || !firewallCurrent(plan.Ifaces) {
		fwErr = syncFirewall(plan.Ifaces)
	}
	if fwErr != nil {
		issues = append(issues, Issue{IssueFirewall, fwErr.Error()})
	}
	if len(plan.Ifaces) > 0 && !sysctlOK && fwErr != nil && forwardingOn() {
		issues = append(issues, Issue{IssueForward, "net.ipv4.ip_forward = 1"})
	}
	if is := syncMgmtRoutes(plan); is != nil {
		issues = append(issues, *is)
	}
	return issues
}

// Remove takes back what Apply set up on the host: the nftables table of the engine, the policy rules with the
// management priority and the routes of the management table. It returns what it removed, in words. The settings
// of the relay interfaces (no routing, ARP, IPv6) go away with the VLAN interfaces themselves (netif.Cleanup);
// on a trunk without an address and on an interface attached as a device they stay until the next boot.
func Remove() (removed []string, err error) {
	var errs []error
	if c, cerr := nftables.New(); cerr != nil {
		errs = append(errs, fmt.Errorf("nftables: %w", cerr))
	} else if t, _ := c.ListTableOfFamily(TableName, nftables.TableFamilyINet); t != nil {
		if werr := writeFirewall(nil, false); werr != nil {
			errs = append(errs, fmt.Errorf("nftables table %s: %w", TableName, werr))
		} else {
			removed = append(removed, "nftables table inet "+TableName)
		}
	}
	rules := 0
	if have, lerr := netlink.RuleList(netlink.FAMILY_V4); lerr != nil {
		errs = append(errs, fmt.Errorf("policy rules: %w", lerr))
	} else {
		for i := range have {
			if have[i].Priority == MgmtPriority {
				rules++
			}
		}
		m, rerr := removeStaleMgmtRules(map[string]*netlink.Rule{})
		errs = append(errs, rerr)
		errs = append(errs, m.errs...)
	}
	if rules > 0 {
		removed = append(removed, fmt.Sprintf("%d policy rule(s) with priority %d", rules, MgmtPriority))
	}
	routes, _ := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{Table: MgmtTable}, netlink.RT_FILTER_TABLE)
	errs = append(errs, syncMgmtTable(nil)...)
	if len(routes) > 0 {
		removed = append(removed, fmt.Sprintf("%d route(s) in table %d", len(routes), MgmtTable))
	}
	return removed, errors.Join(errs...)
}

func forwardingOn() bool {
	b, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	return err == nil && strings.TrimSpace(string(b)) != "0"
}

// ---- nftables ----

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
	if len(ifaces) == 0 {
		return false
	}
	set, err := c.GetSetByName(t, "relay_if")
	if err != nil {
		return false
	}
	elems, err := c.GetSetElements(set)
	if err != nil || len(elems) != len(ifaces) {
		return false
	}
	have := map[string]bool{}
	for _, e := range elems {
		have[strings.TrimRight(string(e.Key), "\x00")] = true
	}
	for _, n := range ifaces {
		if !have[n] {
			return false
		}
	}
	for _, name := range []string{"input", "forward"} {
		ch, err := c.ListChain(t, name)
		if err != nil || ch == nil {
			return false
		}
		if rules, err := c.GetRules(t, ch); err != nil || len(rules) < 2 {
			return false
		}
	}
	return true
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
	return c.Flush()
}

// ---- source routing for the management address ----

// syncMgmtRoutes makes answers from a management address to a relayed network leave through the management
// gateway: one policy rule "from <mgmt address> to <relayed network> lookup MgmtTable" each, and MgmtTable
// holds the default route and the on-link networks of the management interface. Idempotent; rules and routes
// that are no longer needed are removed.
func syncMgmtRoutes(plan Plan) *Issue {
	fail := func(err error) *Issue { return &Issue{IssueMgmtRoute, err.Error()} }
	routes, rules, err := wantedMgmt(plan)
	if err != nil {
		return fail(err)
	}
	if len(rules) == 0 {
		routes = nil // without a rule nothing looks into the table
	}
	var errs []error
	// stale rules go first, then the routes, then the new rules: a rule must never point at an empty table
	missing, err := removeStaleMgmtRules(rules)
	if err != nil {
		return fail(err)
	}
	errs = append(errs, missing.errs...)
	errs = append(errs, syncMgmtTable(routes)...)
	for _, r := range missing.rules {
		if err := netlink.RuleAdd(r); err != nil {
			errs = append(errs, fmt.Errorf("rule from %s to %s: %w", r.Src, r.Dst, err))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fail(err)
	}
	return nil
}

// wantedMgmt works out what the management table and the rules into it have to look like: the default route
// and the on-link networks of the interface that carries the default route, and one rule per pair of management
// address and relay network (mgmtRules). Without a default route or without relay networks nothing is wanted.
func wantedMgmt(plan Plan) (routes []netlink.Route, rules map[string]*netlink.Rule, err error) {
	rules = map[string]*netlink.Rule{}
	def := defaultRoute()
	if def == nil || len(plan.Networks) == 0 {
		return nil, rules, nil
	}
	link, err := netlink.LinkByIndex(def.LinkIndex)
	if err != nil {
		return nil, nil, err
	}
	routes = append(routes, netlink.Route{LinkIndex: def.LinkIndex, Gw: def.Gw, Table: MgmtTable})
	var onLink []netip.Prefix
	if rs, err := netlink.RouteList(link, netlink.FAMILY_V4); err == nil {
		for _, r := range rs {
			if r.Dst == nil || r.Gw != nil || r.Scope != netlink.SCOPE_LINK {
				continue
			}
			if ones, _ := r.Dst.Mask.Size(); ones == 0 {
				continue
			}
			routes = append(routes, netlink.Route{LinkIndex: def.LinkIndex, Dst: r.Dst, Scope: netlink.SCOPE_LINK, Table: MgmtTable})
			if p, ok := prefixOf(r.Dst); ok {
				onLink = append(onLink, p)
			}
		}
	}
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return nil, nil, err
	}
	var mgmt []netip.Addr
	for _, a := range addrs {
		if a.Scope != int(netlink.SCOPE_UNIVERSE) {
			continue
		}
		if ip, ok := netip.AddrFromSlice(a.IP.To4()); ok {
			mgmt = append(mgmt, ip)
		}
	}
	for _, mr := range mgmtRules(mgmt, plan.Networks, onLink) {
		r := netlink.NewRule()
		r.Family = netlink.FAMILY_V4
		r.Priority = MgmtPriority
		r.Table = MgmtTable
		r.Src = &net.IPNet{IP: mr.Src.AsSlice(), Mask: net.CIDRMask(32, 32)}
		r.Dst = &net.IPNet{IP: mr.Dst.Addr().AsSlice(), Mask: net.CIDRMask(mr.Dst.Bits(), 32)}
		rules[mgmtRuleKey(r)] = r
	}
	return routes, rules, nil
}

func mgmtRuleKey(r *netlink.Rule) string {
	if r.Src == nil || r.Dst == nil {
		return ""
	}
	return r.Src.String() + ">" + r.Dst.String()
}

// missingRules are the wanted rules the host does not have yet, and what went wrong while stale ones were removed.
type missingRules struct {
	rules map[string]*netlink.Rule
	errs  []error
}

// removeStaleMgmtRules removes the rules with the management priority that are not wanted (any more) and
// returns the wanted ones that still have to be added.
func removeStaleMgmtRules(want map[string]*netlink.Rule) (missingRules, error) {
	m := missingRules{rules: want}
	have, err := netlink.RuleList(netlink.FAMILY_V4)
	if err != nil {
		return m, err
	}
	for i := range have {
		r := have[i]
		if r.Priority != MgmtPriority {
			continue
		}
		key := mgmtRuleKey(&r)
		if _, ok := want[key]; ok && r.Table == MgmtTable {
			delete(want, key)
			continue
		}
		r.Family = netlink.FAMILY_V4
		if err := netlink.RuleDel(&r); err != nil {
			m.errs = append(m.errs, fmt.Errorf("remove rule: %w", err))
		}
	}
	return m, nil
}

// syncMgmtTable makes the management table hold exactly the wanted routes.
func syncMgmtTable(want []netlink.Route) []error {
	var errs []error
	for i := range want {
		if err := netlink.RouteReplace(&want[i]); err != nil {
			errs = append(errs, fmt.Errorf("route in table %d: %w", MgmtTable, err))
		}
	}
	if old, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{Table: MgmtTable}, netlink.RT_FILTER_TABLE); err == nil {
		for i := range old {
			if !hasRoute(want, old[i]) {
				if err := netlink.RouteDel(&old[i]); err != nil {
					errs = append(errs, fmt.Errorf("remove route: %w", err))
				}
			}
		}
	}
	return errs
}

// defaultRoute is the IPv4 default route of the main table with a gateway (lowest metric), or nil.
func defaultRoute() *netlink.Route {
	rs, err := netlink.RouteList(nil, netlink.FAMILY_V4)
	if err != nil {
		return nil
	}
	var best *netlink.Route
	for i := range rs {
		r := &rs[i]
		if r.Dst != nil {
			if ones, _ := r.Dst.Mask.Size(); ones != 0 {
				continue
			}
		}
		if r.Gw == nil || r.LinkIndex <= 0 {
			continue
		}
		if best == nil || r.Priority < best.Priority {
			best = r
		}
	}
	return best
}

func prefixOf(n *net.IPNet) (netip.Prefix, bool) {
	a, ok := netip.AddrFromSlice(n.IP.To4())
	if !ok {
		return netip.Prefix{}, false
	}
	ones, _ := n.Mask.Size()
	return netip.PrefixFrom(a, ones), true
}

func isDefault(n *net.IPNet) bool {
	if n == nil {
		return true
	}
	ones, _ := n.Mask.Size()
	return ones == 0
}

func hasRoute(list []netlink.Route, r netlink.Route) bool {
	for _, w := range list {
		if w.LinkIndex != r.LinkIndex || !w.Gw.Equal(r.Gw) || isDefault(w.Dst) != isDefault(r.Dst) {
			continue
		}
		if isDefault(w.Dst) || w.Dst.String() == r.Dst.String() {
			return true
		}
	}
	return false
}
