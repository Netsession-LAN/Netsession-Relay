//go:build linux

package hostprep

import (
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/vishvananda/netlink"
)

// Source routing for the management address: answers to a relayed network leave through the management gateway.

// The engine owns this routing table and all policy rules with this priority (lower than 32766 of the main
// table, so they are asked first; a number nobody else is likely to use).
const (
	MgmtTable    = 7355
	MgmtPriority = 7355
)

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
	errs = append(errs, addMgmtRules(missing.rules)...)
	if err := errors.Join(errs...); err != nil {
		return fail(err)
	}
	return nil
}

func addMgmtRules(rules map[string]*netlink.Rule) []error {
	var errs []error
	for _, r := range rules {
		if err := netlink.RuleAdd(r); err != nil {
			errs = append(errs, fmt.Errorf("rule from %s to %s: %w", r.Src, r.Dst, err))
		}
	}
	return errs
}

// wantedMgmt works out what the management table and the rules into it have to look like: the default route
// and the on-link networks of the interface that carries the default route, and one rule per pair of management
// address and relay network (mgmtRules). Without a default route or without relay networks nothing is wanted.
func wantedMgmt(plan Plan) (routes []netlink.Route, rules map[string]*netlink.Rule, err error) {
	def := defaultRoute()
	if def == nil || len(plan.Networks) == 0 {
		return nil, map[string]*netlink.Rule{}, nil
	}
	link, err := netlink.LinkByIndex(def.LinkIndex)
	if err != nil {
		return nil, nil, err
	}
	onLink := onLinkNetworks(link)
	mgmt, err := globalAddrs(link)
	if err != nil {
		return nil, nil, err
	}
	return mgmtTableRoutes(def, onLink), mgmtPolicyRules(mgmt, plan.Networks, onLink), nil
}

// mgmtTableRoutes are the routes of the management table: the default route and the on-link networks.
func mgmtTableRoutes(def *netlink.Route, onLink []*net.IPNet) []netlink.Route {
	routes := []netlink.Route{{LinkIndex: def.LinkIndex, Gw: def.Gw, Table: MgmtTable}}
	for _, n := range onLink {
		routes = append(routes, netlink.Route{LinkIndex: def.LinkIndex, Dst: n, Scope: netlink.SCOPE_LINK, Table: MgmtTable})
	}
	return routes
}

// mgmtPolicyRules are the kernel rules for mgmtRules, by mgmtRuleKey.
func mgmtPolicyRules(mgmt []netip.Addr, networks []netip.Prefix, onLink []*net.IPNet) map[string]*netlink.Rule {
	rules := map[string]*netlink.Rule{}
	for _, mr := range mgmtRules(mgmt, networks, prefixesOf(onLink)) {
		r := policyRule(mr)
		rules[mgmtRuleKey(r)] = r
	}
	return rules
}

// onLinkNetworks are the networks directly attached to the link (link-scope routes without a gateway, not the
// default route). A link whose routes cannot be read has none.
func onLinkNetworks(link netlink.Link) []*net.IPNet {
	rs, err := netlink.RouteList(link, netlink.FAMILY_V4)
	if err != nil {
		return nil
	}
	var out []*net.IPNet
	for _, r := range rs {
		if isOnLinkNetwork(r) {
			out = append(out, r.Dst)
		}
	}
	return out
}

func isOnLinkNetwork(r netlink.Route) bool {
	return r.Gw == nil && r.Scope == netlink.SCOPE_LINK && !isDefault(r.Dst)
}

// prefixesOf converts the IPv4 networks to prefixes, skipping any that are not IPv4.
func prefixesOf(nets []*net.IPNet) []netip.Prefix {
	var out []netip.Prefix
	for _, n := range nets {
		if p, ok := prefixOf(n); ok {
			out = append(out, p)
		}
	}
	return out
}

// globalAddrs are the IPv4 addresses of global scope on the link: the management addresses.
func globalAddrs(link netlink.Link) ([]netip.Addr, error) {
	addrs, err := netlink.AddrList(link, netlink.FAMILY_V4)
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, a := range addrs {
		if a.Scope != int(netlink.SCOPE_UNIVERSE) {
			continue
		}
		if ip, ok := netip.AddrFromSlice(a.IP.To4()); ok {
			out = append(out, ip)
		}
	}
	return out, nil
}

// syncMgmtTable makes the management table hold exactly the wanted routes.
func syncMgmtTable(want []netlink.Route) []error {
	var errs []error
	for i := range want {
		if err := netlink.RouteReplace(&want[i]); err != nil {
			errs = append(errs, fmt.Errorf("route in table %d: %w", MgmtTable, err))
		}
	}
	return append(errs, removeUnwantedRoutes(want)...)
}

// removeUnwantedRoutes deletes the routes of the management table that are not in want. A table that cannot be
// read is left alone.
func removeUnwantedRoutes(want []netlink.Route) []error {
	old, err := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{Table: MgmtTable}, netlink.RT_FILTER_TABLE)
	if err != nil {
		return nil
	}
	var errs []error
	for i := range old {
		if hasRoute(want, old[i]) {
			continue
		}
		if err := netlink.RouteDel(&old[i]); err != nil {
			errs = append(errs, fmt.Errorf("remove route: %w", err))
		}
	}
	return errs
}
