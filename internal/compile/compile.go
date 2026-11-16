// Package compile turns the configuration of the active event into an engine snapshot: merged port table,
// conflicts and warnings (docs/02-plan.md, 5.3).
//
// Admins think in games, the engine thinks in ports. Several games share ports (WC3 and StarCraft on 6112,
// all Source games on 27015-27020, DirectPlay on 47624). The compiler merges all enabled specs per
// (kind, multicast group, port), resolves conflicts deterministically and reports every conflict as a warning.
package compile

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/Netsession2024/Netsession-Relay/internal/gamedb"
	"github.com/Netsession2024/Netsession-Relay/internal/portspec"
)

// SnapshotVersion is the format version of Snapshot.
const SnapshotVersion = 1

// Engine modes (docs/02-plan.md, 4.3).
const (
	ModeActive  = "active"
	ModeObserve = "observe"
	ModePaused  = "paused"
	ModeStandby = "standby"
)

// Attach modes of a VLAN.
const (
	AttachTrunk  = "trunk"  // engine creates <parent>.<vid>
	AttachDevice = "device" // interface exists already (e.g. one vNIC per VLAN)
)

// VLAN is one game network the relay is attached to.
type VLAN struct {
	VID     int          `json:"vid"`
	Name    string       `json:"name"`
	Group   string       `json:"group"`
	CIDR    netip.Prefix `json:"cidr"`
	RelayIP netip.Addr   `json:"relay_ip"`
	Attach  string       `json:"attach"`
	Parent  string       `json:"parent,omitempty"` // trunk interface for AttachTrunk
	Device  string       `json:"device,omitempty"` // interface name for AttachDevice
	Enabled bool         `json:"-"`
	RatePPS int          `json:"rate_pps,omitempty"` // 0 = Limits.VLANPPS
	Burst   int          `json:"rate_burst,omitempty"`
}

// Edge is one direction of the VLAN matrix: packets from VLANs of group Src go to VLANs of group Dst.
type Edge struct {
	Src string `json:"src"`
	Dst string `json:"dst"`
}

// FlowProfile is a named VLAN matrix.
type FlowProfile struct {
	Name  string
	Edges []Edge
}

// Override changes spec attributes for one game in one event.
type Override struct {
	SourceMode string `json:"source_mode,omitempty"`
	DstMode    string `json:"dst_mode,omitempty"`
}

// Selection is one game switched on in the active event.
type Selection struct {
	GameID      string
	FlowProfile string // empty = Input.DefaultProfile
	Override    Override
}

// Limits are global engine limits.
type Limits struct {
	VLANPPS   int `json:"vlan_pps"`
	VLANBurst int `json:"vlan_burst"`
	StormPPS  int `json:"storm_pps"`
	DedupMS   int `json:"dedup_ms"`
}

// DefaultLimits are the values from docs/02-plan.md, 4.3.
var DefaultLimits = Limits{VLANPPS: 200, VLANBurst: 400, StormPPS: 5000, DedupMS: 200}

// Peer is a known partner relay (HA stage 2); its frames are dropped in step 1 of the data path.
type Peer struct {
	Name string   `json:"name"`
	MACs []string `json:"macs"`
}

// Input is everything the compiler needs.
type Input struct {
	Mode           string
	DB             *gamedb.DB
	VLANs          []VLAN
	Profiles       []FlowProfile
	DefaultProfile string
	Games          []Selection
	Limits         Limits // zero value = DefaultLimits
	Peers          []Peer
}

// Rule is one merged entry of the port table.
type Rule struct {
	ID         string   `json:"id"`
	Kind       string   `json:"kind"`
	Group      string   `json:"group,omitempty"`
	Ports      string   `json:"ports"`
	Specs      []string `json:"specs"` // game ids
	SourceMode string   `json:"source_mode"`
	DstMode    string   `json:"dst_mode"`
	TTLMode    string   `json:"ttl_mode"`
	Flows      []Edge   `json:"flows"`
	RatePPS    int      `json:"rate_pps"`
	RateBurst  int      `json:"rate_burst"`
	PayloadMax int      `json:"payload_max"`
}

// Snapshot is the complete engine configuration. It is written to /var/lib/nsrelay/active.json.
type Snapshot struct {
	Version  int      `json:"version"`
	Hash     string   `json:"hash"`
	Mode     string   `json:"mode"`
	SeedHash string   `json:"seed_hash"`
	VLANs    []VLAN   `json:"vlans"`
	Rules    []Rule   `json:"rules"`
	Limits   Limits   `json:"limits"`
	Peers    []Peer   `json:"peers"`
	Warnings []string `json:"warnings,omitempty"`
}

// Warning codes.
const (
	WarnSourceModeConflict = "source_mode_conflict"
	WarnDstModeConflict    = "dst_mode_conflict"
	WarnTTLModeConflict    = "ttl_mode_conflict"
	WarnSharedPort         = "shared_port"
	WarnGameNoRelay        = "game_no_relay"
	WarnGroupEmpty         = "group_empty"
	WarnRelayIPRewrite     = "relay_ip_rewrite"
)

// Warning is a non-fatal finding shown in the UI and stored with the revision.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (w Warning) String() string { return w.Code + ": " + w.Message }

type portKey struct {
	kind  string
	group string
	port  uint16
}

type portAcc struct {
	games      []string
	sourceMode map[string][]string // mode -> games
	dstMode    map[string][]string
	ttlMode    map[string][]string
	flows      map[Edge]bool
	rate       int
	burst      int
	payloadMax int
}

// Compile builds the snapshot. Errors are configuration mistakes that make a snapshot impossible
// (all are reported together); warnings are returned alongside a valid snapshot.
func Compile(in Input) (*Snapshot, []Warning, error) {
	if in.DB == nil {
		return nil, nil, errors.New("compile: no game database")
	}
	var errs []error
	var warns []Warning
	warn := func(code, format string, args ...any) {
		warns = append(warns, Warning{code, fmt.Sprintf(format, args...)})
	}

	mode := cmp.Or(in.Mode, ModeActive)
	if !slices.Contains([]string{ModeActive, ModeObserve, ModePaused, ModeStandby}, mode) {
		errs = append(errs, fmt.Errorf("mode %q invalid", mode))
	}
	limits := in.Limits
	if limits == (Limits{}) {
		limits = DefaultLimits
	}

	vlans, groups, vErrs := checkVLANs(in.VLANs)
	errs = append(errs, vErrs...)

	profiles := map[string]FlowProfile{}
	for _, p := range in.Profiles {
		if _, dup := profiles[p.Name]; dup {
			errs = append(errs, fmt.Errorf("flow profile %q defined twice", p.Name))
		}
		profiles[p.Name] = p
	}

	acc := map[portKey]*portAcc{}
	seenGame := map[string]bool{}
	for _, sel := range in.Games {
		g, ok := in.DB.Get(sel.GameID)
		if !ok {
			errs = append(errs, fmt.Errorf("game %q not in database", sel.GameID))
			continue
		}
		if seenGame[g.ID] {
			errs = append(errs, fmt.Errorf("game %q selected twice", g.ID))
			continue
		}
		seenGame[g.ID] = true
		if g.RelayNeeded != gamedb.RelayYes {
			warn(WarnGameNoRelay, "%s (%s) has no LAN discovery to relay (relay_needed=%s), skipped", g.Name, g.ID, g.RelayNeeded)
			continue
		}
		pname := cmp.Or(sel.FlowProfile, in.DefaultProfile)
		prof, ok := profiles[pname]
		if !ok {
			errs = append(errs, fmt.Errorf("game %q: flow profile %q not defined", g.ID, pname))
			continue
		}
		for _, e := range prof.Edges {
			for _, grp := range []string{e.Src, e.Dst} {
				if _, known := groups[grp]; !known {
					errs = append(errs, fmt.Errorf("flow profile %q: group %q has no VLAN", pname, grp))
				}
			}
		}
		if err := checkOverride(sel.Override); err != nil {
			errs = append(errs, fmt.Errorf("game %q: %w", g.ID, err))
			continue
		}
		for _, raw := range g.Discovery {
			s := raw.Effective()
			s.SourceMode = cmp.Or(sel.Override.SourceMode, s.SourceMode)
			s.DstMode = cmp.Or(sel.Override.DstMode, s.DstMode)
			if s.SourceMode != gamedb.SourceOriginal && g.SourceIP == gamedb.SourceOriginal {
				warn(WarnRelayIPRewrite, "%s needs the original sender IP, but source_mode=%s is set", g.ID, s.SourceMode)
			}
			ps, err := portspec.Parse(s.Ports)
			if err != nil { // impossible after gamedb validation; keep the guard
				errs = append(errs, fmt.Errorf("game %q: %w", g.ID, err))
				continue
			}
			for _, p := range ps.Ports() {
				k := portKey{s.Kind, s.Group, p}
				a := acc[k]
				if a == nil {
					a = &portAcc{sourceMode: map[string][]string{}, dstMode: map[string][]string{},
						ttlMode: map[string][]string{}, flows: map[Edge]bool{}}
					acc[k] = a
				}
				if !slices.Contains(a.games, g.ID) {
					a.games = append(a.games, g.ID)
				}
				a.sourceMode[s.SourceMode] = appendUnique(a.sourceMode[s.SourceMode], g.ID)
				a.dstMode[s.DstMode] = appendUnique(a.dstMode[s.DstMode], g.ID)
				a.ttlMode[s.TTLMode] = appendUnique(a.ttlMode[s.TTLMode], g.ID)
				for _, e := range prof.Edges {
					a.flows[e] = true
				}
				a.rate = max(a.rate, s.RatePPS)
				a.burst = max(a.burst, s.RateBurst)
				a.payloadMax = max(a.payloadMax, s.PayloadMax)
			}
		}
	}

	for grp, n := range groups {
		if n == 0 {
			warn(WarnGroupEmpty, "group %q has no enabled VLAN", grp)
		}
	}

	if err := errors.Join(errs...); err != nil {
		return nil, warns, err
	}

	rules := buildRules(acc, warn)
	slices.SortFunc(warns, func(a, b Warning) int { return cmp.Or(cmp.Compare(a.Code, b.Code), cmp.Compare(a.Message, b.Message)) })
	warns = slices.Compact(warns)

	snap := &Snapshot{
		Version:  SnapshotVersion,
		Mode:     mode,
		SeedHash: in.DB.Hash(),
		VLANs:    vlans,
		Rules:    rules,
		Limits:   limits,
		Peers:    append([]Peer{}, in.Peers...),
	}
	for _, w := range warns {
		snap.Warnings = append(snap.Warnings, w.String())
	}
	snap.Hash = hashOf(snap)
	return snap, warns, nil
}

func appendUnique(s []string, v string) []string {
	if slices.Contains(s, v) {
		return s
	}
	return append(s, v)
}

func checkOverride(o Override) error {
	if o.SourceMode != "" && !slices.Contains([]string{gamedb.SourceOriginal, gamedb.SourceRelay}, o.SourceMode) {
		// relay_proxy needs the reply proxy (phase 14)
		return fmt.Errorf("override source_mode %q not supported", o.SourceMode)
	}
	if o.DstMode != "" && !slices.Contains([]string{gamedb.DstKeep, gamedb.DstSubnet, gamedb.DstLimited}, o.DstMode) {
		return fmt.Errorf("override dst_mode %q invalid", o.DstMode)
	}
	return nil
}

// checkVLANs validates the VLAN list and returns the enabled VLANs (sorted by VID) and the number of
// enabled VLANs per group (groups that only have disabled VLANs are present with 0).
func checkVLANs(in []VLAN) ([]VLAN, map[string]int, []error) {
	var errs []error
	groups := map[string]int{}
	seenVID := map[int]bool{}
	var out []VLAN
	for _, v := range in {
		name := fmt.Sprintf("VLAN %d (%s)", v.VID, v.Name)
		if v.VID < 1 || v.VID > 4094 {
			errs = append(errs, fmt.Errorf("%s: VID out of range", name))
		}
		if seenVID[v.VID] {
			errs = append(errs, fmt.Errorf("%s: VID used twice", name))
		}
		seenVID[v.VID] = true
		if v.Group == "" {
			errs = append(errs, fmt.Errorf("%s: no group", name))
		}
		if _, ok := groups[v.Group]; !ok {
			groups[v.Group] = 0
		}
		if !v.Enabled {
			continue
		}
		groups[v.Group]++
		if !v.CIDR.IsValid() || !v.CIDR.Addr().Is4() {
			errs = append(errs, fmt.Errorf("%s: IPv4 network missing", name))
		} else if v.CIDR.Bits() > 30 {
			errs = append(errs, fmt.Errorf("%s: network %s too small", name, v.CIDR))
		}
		v.CIDR = v.CIDR.Masked()
		if !v.RelayIP.IsValid() || !v.CIDR.Contains(v.RelayIP) {
			errs = append(errs, fmt.Errorf("%s: relay IP %s not in %s", name, v.RelayIP, v.CIDR))
		}
		switch v.Attach {
		case AttachTrunk:
			if v.Parent == "" {
				errs = append(errs, fmt.Errorf("%s: trunk attach needs a parent interface", name))
			}
			v.Device = ""
		case AttachDevice:
			if v.Device == "" {
				errs = append(errs, fmt.Errorf("%s: device attach needs a device name", name))
			}
			v.Parent = ""
		default:
			errs = append(errs, fmt.Errorf("%s: attach %q invalid", name, v.Attach))
		}
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b VLAN) int { return cmp.Compare(a.VID, b.VID) })
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[i].CIDR.IsValid() && out[j].CIDR.IsValid() && out[i].CIDR.Overlaps(out[j].CIDR) {
				errs = append(errs, fmt.Errorf("VLAN %d and %d: networks %s and %s overlap",
					out[i].VID, out[j].VID, out[i].CIDR, out[j].CIDR))
			}
		}
	}
	return out, groups, errs
}

// portRule is the resolved rule for a single port before grouping.
type portRule struct {
	key       portKey
	rule      Rule     // without ID and Ports
	conflicts []string // human-readable conflict descriptions (field: who wants what; winner)
}

// buildRules resolves conflicts per port, groups consecutive ports with identical attributes into rules and
// emits one warning per rule (not per port) for conflicts and shared ports.
func buildRules(acc map[portKey]*portAcc, warn func(code, format string, args ...any)) []Rule {
	keys := make([]portKey, 0, len(acc))
	for k := range acc {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b portKey) int {
		return cmp.Or(cmp.Compare(a.kind, b.kind), cmp.Compare(a.group, b.group), cmp.Compare(a.port, b.port))
	})

	list := make([]portRule, 0, len(keys))
	for _, k := range keys {
		a := acc[k]
		slices.Sort(a.games)
		pr := portRule{key: k}
		pr.rule = Rule{
			Kind:       k.kind,
			Group:      k.group,
			Specs:      a.games,
			RatePPS:    a.rate,
			RateBurst:  a.burst,
			PayloadMax: a.payloadMax,
		}
		var c string
		pr.rule.SourceMode, c = resolve(a.sourceMode, gamedb.SourceOriginal, "source_mode")
		pr.conflicts = appendIf(pr.conflicts, c)
		pr.rule.DstMode, c = resolve(a.dstMode, gamedb.DstKeep, "dst_mode")
		pr.conflicts = appendIf(pr.conflicts, c)
		pr.rule.TTLMode, c = resolve(a.ttlMode, gamedb.TTLKeep, "ttl_mode")
		pr.conflicts = appendIf(pr.conflicts, c)
		for e := range a.flows {
			pr.rule.Flows = append(pr.rule.Flows, e)
		}
		slices.SortFunc(pr.rule.Flows, func(x, y Edge) int { return cmp.Or(cmp.Compare(x.Src, y.Src), cmp.Compare(x.Dst, y.Dst)) })
		list = append(list, pr)
	}

	var rules []Rule
	for i := 0; i < len(list); {
		j := i
		ports := []uint16{list[i].key.port}
		for j+1 < len(list) && sameRule(list[i], list[j+1]) {
			j++
			ports = append(ports, list[j].key.port)
		}
		r := list[i].rule
		r.Ports = portspec.FromPorts(ports).String()
		prefix := "bc"
		where := "broadcast port " + r.Ports
		if r.Kind == gamedb.KindMulticast {
			prefix = "mc:" + r.Group
			where = "multicast " + r.Group + " port " + r.Ports
		}
		r.ID = prefix + ":" + r.Ports
		for _, c := range list[i].conflicts {
			code := WarnSourceModeConflict
			switch {
			case strings.HasPrefix(c, "dst_mode"):
				code = WarnDstModeConflict
			case strings.HasPrefix(c, "ttl_mode"):
				code = WarnTTLModeConflict
			}
			warn(code, "%s: %s", where, c)
		}
		if len(r.Specs) > 1 {
			warn(WarnSharedPort, "%s is used by %s", where, strings.Join(r.Specs, ", "))
		}
		rules = append(rules, r)
		i = j + 1
	}
	return rules
}

func appendIf(s []string, v string) []string {
	if v == "" {
		return s
	}
	return append(s, v)
}

func sameRule(a, b portRule) bool {
	if a.key.kind != b.key.kind || a.key.group != b.key.group {
		return false
	}
	x, y := a.rule, b.rule
	return slices.Equal(x.Specs, y.Specs) && x.SourceMode == y.SourceMode && x.DstMode == y.DstMode &&
		x.TTLMode == y.TTLMode && slices.Equal(x.Flows, y.Flows) && x.RatePPS == y.RatePPS &&
		x.RateBurst == y.RateBurst && x.PayloadMax == y.PayloadMax && slices.Equal(a.conflicts, b.conflicts)
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

func hashOf(s *Snapshot) string {
	c := *s
	c.Hash = ""
	b, err := json.Marshal(c)
	if err != nil {
		panic(err) // all fields are plain data
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
