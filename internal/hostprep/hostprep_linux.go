//go:build linux

package hostprep

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/google/nftables"
	"github.com/vishvananda/netlink"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/netif"
)

// Apply prepares the host for the snapshot and returns what could not be done.
func Apply(s *compile.Snapshot) []Issue { return prepare(s, true) }

// Refresh repeats the preparation while the engine runs: somebody may have switched forwarding on for all
// interfaces (starting Docker does), flushed the firewall rules (a firewall reload on the host), or the
// management address arrived late (DHCP). The firewall table is only rewritten when it is missing or differs.
func Refresh(s *compile.Snapshot) []Issue { return prepare(s, false) }

func prepare(s *compile.Snapshot, rewrite bool) []Issue {
	protected, err := netif.DefaultRouteLinks()
	if err != nil {
		// without the routes the management interface is unknown: do not risk shielding it
		return []Issue{{IssueFirewall, "cannot read the routes of the host: " + err.Error()}}
	}
	plan := PlanFor(s, protected, netif.HasIPv4)
	issues, sysctlOK := hardenLinks(plan.Ifaces)
	issues = append(issues, firewallIssues(plan.Ifaces, rewrite, sysctlOK)...)
	if is := syncMgmtRoutes(plan); is != nil {
		issues = append(issues, *is)
	}
	return issues
}

// firewallIssues brings the firewall table in place (with rewrite always, else only when it is missing or
// differs) and reports what failed.
func firewallIssues(ifaces []string, rewrite, sysctlOK bool) []Issue {
	if !rewrite && firewallCurrent(ifaces) {
		return nil
	}
	err := syncFirewall(ifaces)
	if err == nil {
		return nil
	}
	issues := []Issue{{IssueFirewall, err.Error()}}
	if mayRoute(ifaces, sysctlOK) {
		issues = append(issues, Issue{IssueForward, "net.ipv4.ip_forward = 1"})
	}
	return issues
}

// mayRoute reports, once the firewall failed, whether the host may route between relay interfaces: neither the
// sysctl settings nor the firewall stop it.
func mayRoute(ifaces []string, sysctlOK bool) bool {
	return len(ifaces) > 0 && !sysctlOK && forwardingOn()
}

// hardenLinks switches routing, ARP and IPv6 off on the relay interfaces. It returns the sysctl and IPv6 issues
// and whether every interface took its IPv4 settings.
func hardenLinks(ifaces []string) (issues []Issue, sysctlOK bool) {
	var sysErr, v6Err []string
	for _, name := range ifaces {
		if err := netif.HardenLink(name); err != nil {
			sysErr = append(sysErr, name+": "+err.Error())
		}
		if err := netif.DisableIPv6(name); err != nil {
			v6Err = append(v6Err, name+": "+err.Error())
		}
	}
	issues = append(joinedIssue(IssueSysctl, sysErr), joinedIssue(IssueIPv6, v6Err)...)
	return issues, len(sysErr) == 0
}

// joinedIssue is one issue with all errors of a kind, none without errors.
func joinedIssue(code string, errs []string) []Issue {
	if len(errs) == 0 {
		return nil
	}
	return []Issue{{code, strings.Join(errs, "; ")}}
}

// Remove takes back what Apply set up on the host: the nftables table of the engine, the policy rules with the
// management priority and the routes of the management table. It returns what it removed, in words. The settings
// of the relay interfaces (no routing, ARP, IPv6) go away with the VLAN interfaces themselves (netif.Cleanup);
// on a trunk without an address and on an interface attached as a device they stay until the next boot.
func Remove() (removed []string, err error) {
	var r removal
	r.firewallTable()
	r.mgmtRules()
	r.mgmtRoutes()
	return r.removed, errors.Join(r.errs...)
}

// removal collects what Remove took away, in words, and what went wrong.
type removal struct {
	removed []string
	errs    []error
}

// firewallTable removes the nftables table of the engine if it exists.
func (r *removal) firewallTable() {
	c, err := nftables.New()
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("nftables: %w", err))
		return
	}
	if t, _ := c.ListTableOfFamily(TableName, nftables.TableFamilyINet); t == nil {
		return
	}
	if err := writeFirewall(nil, false); err != nil {
		r.errs = append(r.errs, fmt.Errorf("nftables table %s: %w", TableName, err))
		return
	}
	r.removed = append(r.removed, "nftables table inet "+TableName)
}

// mgmtRules removes every policy rule with the management priority.
func (r *removal) mgmtRules() {
	have, err := netlink.RuleList(netlink.FAMILY_V4)
	if err != nil {
		r.errs = append(r.errs, fmt.Errorf("policy rules: %w", err))
		return
	}
	rules := 0
	for i := range have {
		if have[i].Priority == MgmtPriority {
			rules++
		}
	}
	m, err := removeStaleMgmtRules(map[string]*netlink.Rule{})
	r.errs = append(r.errs, err)
	r.errs = append(r.errs, m.errs...)
	if rules > 0 {
		r.removed = append(r.removed, fmt.Sprintf("%d policy rule(s) with priority %d", rules, MgmtPriority))
	}
}

// mgmtRoutes empties the management routing table.
func (r *removal) mgmtRoutes() {
	routes, _ := netlink.RouteListFiltered(netlink.FAMILY_V4, &netlink.Route{Table: MgmtTable}, netlink.RT_FILTER_TABLE)
	r.errs = append(r.errs, syncMgmtTable(nil)...)
	if len(routes) > 0 {
		r.removed = append(r.removed, fmt.Sprintf("%d route(s) in table %d", len(routes), MgmtTable))
	}
}

func forwardingOn() bool {
	b, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	return err == nil && strings.TrimSpace(string(b)) != "0"
}
