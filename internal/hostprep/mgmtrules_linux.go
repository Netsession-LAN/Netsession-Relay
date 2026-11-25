//go:build linux

package hostprep

import (
	"fmt"
	"net"

	"github.com/vishvananda/netlink"
)

// The policy rules of the management side: one per management address and relayed network, all with
// MgmtPriority, pointing into MgmtTable.

// policyRule is the kernel policy rule "from Src to Dst lookup MgmtTable" with the management priority.
func policyRule(mr mgmtRule) *netlink.Rule {
	r := netlink.NewRule()
	r.Family = netlink.FAMILY_V4
	r.Priority = MgmtPriority
	r.Table = MgmtTable
	r.Src = &net.IPNet{IP: mr.Src.AsSlice(), Mask: net.CIDRMask(32, 32)}
	r.Dst = &net.IPNet{IP: mr.Dst.Addr().AsSlice(), Mask: net.CIDRMask(mr.Dst.Bits(), 32)}
	return r
}

// mgmtRuleKey identifies a rule by source and destination ("" for a rule without both).
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
		if have[i].Priority == MgmtPriority {
			m.settle(have[i])
		}
	}
	return m, nil
}

// settle handles a rule of the host with the management priority: a wanted one is no longer missing, any other
// one is removed.
func (m *missingRules) settle(r netlink.Rule) {
	key := mgmtRuleKey(&r)
	if _, ok := m.rules[key]; ok && r.Table == MgmtTable {
		delete(m.rules, key)
		return
	}
	r.Family = netlink.FAMILY_V4
	if err := netlink.RuleDel(&r); err != nil {
		m.errs = append(m.errs, fmt.Errorf("remove rule: %w", err))
	}
}
