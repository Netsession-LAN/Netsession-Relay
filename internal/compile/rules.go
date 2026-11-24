package compile

import (
	"cmp"
	"fmt"
	"slices"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/portspec"
)

// portRule is the resolved rule for a single port before grouping.
type portRule struct {
	key       portKey
	rule      Rule     // without ID and Ports
	conflicts []string // human-readable conflict descriptions (field: who wants what; winner)
}

// buildRules resolves conflicts per port, groups consecutive ports with identical attributes into rules and
// emits one warning per rule (not per port) for conflicts and shared ports.
func buildRules(acc map[portKey]*portAcc, vlans []VLAN, warn func(code, format string, args ...any)) []Rule {
	list := make([]portRule, 0, len(acc))
	for _, k := range sortedKeys(acc) {
		list = append(list, resolvePort(k, acc[k], vlans))
	}
	var rules []Rule
	for len(list) > 0 {
		n := runLen(list)
		r := ruleOf(list[:n])
		warnRule(r, list[0].conflicts, warn)
		rules = append(rules, r)
		list = list[n:]
	}
	return rules
}

// sortedKeys returns the claimed ports ordered by kind, group and port.
func sortedKeys(acc map[portKey]*portAcc) []portKey {
	keys := make([]portKey, 0, len(acc))
	for k := range acc {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b portKey) int {
		return cmp.Or(cmp.Compare(a.kind, b.kind), cmp.Compare(a.group, b.group), cmp.Compare(a.port, b.port))
	})
	return keys
}

// resolvePort turns what the games claimed on one port into its rule, picking a winner for every conflict.
func resolvePort(k portKey, a *portAcc, vlans []VLAN) portRule {
	slices.Sort(a.games)
	r := Rule{
		Kind:       k.kind,
		Group:      k.group,
		Specs:      a.games,
		RatePPS:    a.rate,
		RateBurst:  a.burst,
		PayloadMax: a.payloadMax,
		Probe:      k.kind == gamedb.KindBroadcast && !a.notQuery,
		Flows:      sortedFlows(a.flows),
	}
	var srcC, dstC, ttlC, filterC string
	r.SourceMode, srcC = resolve(a.sourceMode, gamedb.SourceOriginal, "source_mode")
	r.DstMode, dstC = resolve(a.dstMode, gamedb.DstKeep, "dst_mode")
	r.TTLMode, ttlC = resolve(a.ttlMode, gamedb.TTLKeep, "ttl_mode")
	r.SrcAllow, filterC = mergeSources(a.games, a.sources, vlans)
	return portRule{key: k, rule: r, conflicts: nonEmpty(srcC, dstC, ttlC, filterC)}
}

// sortedFlows lists the edges of a port (nil when there are none).
func sortedFlows(flows map[Edge]bool) []Edge {
	var out []Edge
	for e := range flows {
		out = append(out, e)
	}
	slices.SortFunc(out, func(x, y Edge) int { return cmp.Or(cmp.Compare(x.Src, y.Src), cmp.Compare(x.Dst, y.Dst)) })
	return out
}

// nonEmpty returns the non-empty strings (nil when there are none).
func nonEmpty(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// runLen is the number of ports at the start of list that share the attributes of the first one.
func runLen(list []portRule) int {
	n := 1
	for n < len(list) && sameRule(list[0], list[n]) {
		n++
	}
	return n
}

// ruleOf merges a run of ports with identical attributes into one rule with its port spec and ID.
func ruleOf(run []portRule) Rule {
	ports := make([]uint16, 0, len(run))
	for _, pr := range run {
		ports = append(ports, pr.key.port)
	}
	r := run[0].rule
	r.Ports = portspec.FromPorts(ports).String()
	prefix := "bc"
	if r.Kind == gamedb.KindMulticast {
		prefix = "mc:" + r.Group
	}
	r.ID = prefix + ":" + r.Ports
	return r
}

// ruleWhere describes the ports of a rule in a warning.
func ruleWhere(r Rule) string {
	if r.Kind == gamedb.KindMulticast {
		return "multicast " + r.Group + " port " + r.Ports
	}
	return "broadcast port " + r.Ports
}

// warnRule emits the conflict warnings of a rule and the warning for a port several games share.
func warnRule(r Rule, conflicts []string, warn func(code, format string, args ...any)) {
	where := ruleWhere(r)
	for _, c := range conflicts {
		warn(conflictCode(c), "%s: %s", where, c)
	}
	if len(r.Specs) > 1 {
		warn(WarnSharedPort, "%s is used by %s", where, strings.Join(r.Specs, ", "))
	}
}

// conflictCodes maps the start of a conflict description to its warning code; anything else is a source mode
// conflict.
var conflictCodes = []struct{ prefix, code string }{
	{"dst_mode", WarnDstModeConflict},
	{"ttl_mode", WarnTTLModeConflict},
	{"source filter", WarnSourceFilterOpen},
}

func conflictCode(conflict string) string {
	for _, cc := range conflictCodes {
		if strings.HasPrefix(conflict, cc.prefix) {
			return cc.code
		}
	}
	return WarnSourceModeConflict
}

// ruleScalars are the attributes of a rule that compare with ==; kind and group are those of the port key.
type ruleScalars struct {
	kind, group, sourceMode, dstMode, ttlMode string
	rate, burst, payloadMax                   int
	probe                                     bool
}

func scalarsOf(r Rule) ruleScalars {
	return ruleScalars{r.Kind, r.Group, r.SourceMode, r.DstMode, r.TTLMode, r.RatePPS, r.RateBurst, r.PayloadMax, r.Probe}
}

// sameRule reports whether two ports can share one rule: same attributes, games, flows, source filter and
// conflicts.
func sameRule(a, b portRule) bool {
	x, y := a.rule, b.rule
	if scalarsOf(x) != scalarsOf(y) {
		return false
	}
	sameLists := slices.Equal(x.Specs, y.Specs) && slices.Equal(x.Flows, y.Flows) && slices.Equal(x.SrcAllow, y.SrcAllow)
	return sameLists && slices.Equal(a.conflicts, b.conflicts)
}

// resolve picks the winning value: the preferred one if any game wants it, otherwise the alphabetically
// first. More than one requested value is a conflict, described in the second return value.
func resolve(byValue map[string][]string, preferred, field string) (string, string) {
	values := make([]string, 0, len(byValue))
	for v := range byValue {
		values = append(values, v)
	}
	slices.Sort(values)
	if len(values) == 1 {
		return values[0], ""
	}
	winner := preferred
	if _, ok := byValue[preferred]; !ok {
		winner = values[0]
	}
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, fmt.Sprintf("%s wants %s", strings.Join(byValue[v], ", "), v))
	}
	return winner, fmt.Sprintf("%s conflict (%s), using %s", field, strings.Join(parts, "; "), winner)
}
