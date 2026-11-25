// Package compile turns the configuration of the active event into an engine snapshot: merged port table,
// conflicts and warnings.
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

	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
)

// SnapshotVersion is the format version of Snapshot.
const SnapshotVersion = 1

// Engine modes.
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

// Built-in network groups. A group is a free name chosen per installation; these four are the ones the first-run
// setup and the presets offer, and the UI has display names and key caps for them. The identifiers are German
// for historical reasons and stay that way: they are stored in every existing database, in revisions, backups
// and YAML exports, so renaming them would mean migrating all of those for a cosmetic gain.
const (
	GroupTables     = "tische"
	GroupGameserver = "gameserver"
	GroupWiFi       = "wlan"
	GroupOrga       = "orga"
)

// DefaultFlowProfile is the matrix a new configuration starts with (first-run setup, presets, lab): tables, game
// servers and orga reach each other in all directions; WiFi is not part of it.
func DefaultFlowProfile() FlowProfile {
	groups := []string{GroupTables, GroupGameserver, GroupOrga}
	var edges []Edge
	for _, a := range groups {
		for _, b := range groups {
			edges = append(edges, Edge{Src: a, Dst: b})
		}
	}
	return FlowProfile{Name: "spiele", Edges: edges}
}

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
	// Sources optionally restricts senders (IPv4 addresses or networks). A prefix restricts only the VLAN whose
	// network contains it; VLANs without a prefix stay unrestricted. Empty = no filter.
	Sources []netip.Prefix
}

// Limits are global engine limits.
type Limits struct {
	VLANPPS   int `json:"vlan_pps"`
	VLANBurst int `json:"vlan_burst"`
	StormPPS  int `json:"storm_pps"`
	DedupMS   int `json:"dedup_ms"`
}

// DefaultLimits apply when the configuration sets no limits of its own.
var DefaultLimits = Limits{VLANPPS: 200, VLANBurst: 400, StormPPS: 5000, DedupMS: 200}

// Peer is a partner relay whose frames the engine ignores like its own (step 1 of the data path). The list is
// part of the snapshot format and the engine honours it; the compiler leaves it empty, because nothing configures
// partners yet (two relays find each other through the beacon instead).
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
	// Probe lets the engine replay learned client queries from its own VLAN addresses to find servers that
	// only answer (Steam/Source and similar); see engine/probe.go.
	Probe bool
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
	// Probe: every game on these ports only queries (direction query), so a captured client packet is a
	// server query and may be replayed by the server probe. Never set for announce/both.
	Probe bool `json:"probe,omitempty"`
	// SrcAllow is the source filter, sorted: a packet from VLAN v is relayed only if no prefix lies inside v's
	// network or its source is inside one of those that do. Empty = no filter (field omitted, hash unchanged).
	SrcAllow []netip.Prefix `json:"src_allow,omitempty"`
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
	Probe    bool     `json:"probe,omitempty"` // server probe on (engine/probe.go)
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
	WarnSourceNoVLAN       = "source_no_vlan"     // a source filter prefix lies in no enabled VLAN
	WarnSourceFilterOpen   = "source_filter_open" // games on one rule disagree, the VLAN stays unrestricted
)

// Warning is a non-fatal finding shown in the UI and stored with the revision.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (w Warning) String() string { return w.Code + ": " + w.Message }

// compiler is the state of one Compile call: the checked input, what the games have claimed per port so far,
// and the findings.
type compiler struct {
	in       Input
	vlans    []VLAN         // enabled, sorted by VID
	groups   map[string]int // enabled VLANs per group
	profiles map[string]FlowProfile
	acc      map[portKey]*portAcc
	seenGame map[string]bool
	errs     []error
	warns    []Warning
}

func (c *compiler) warn(code, format string, args ...any) {
	c.warns = append(c.warns, Warning{code, fmt.Sprintf(format, args...)})
}

func (c *compiler) fail(format string, args ...any) {
	c.errs = append(c.errs, fmt.Errorf(format, args...))
}

// Compile builds the snapshot. Errors are configuration mistakes that make a snapshot impossible
// (all are reported together); warnings are returned alongside a valid snapshot.
func Compile(in Input) (*Snapshot, []Warning, error) {
	if in.DB == nil {
		return nil, nil, errors.New("compile: no game database")
	}
	c := &compiler{in: in, profiles: map[string]FlowProfile{}, acc: map[portKey]*portAcc{}, seenGame: map[string]bool{}}

	mode := cmp.Or(in.Mode, ModeActive)
	if !slices.Contains([]string{ModeActive, ModeObserve, ModePaused, ModeStandby}, mode) {
		c.fail("mode %q invalid", mode)
	}

	var vErrs []error
	c.vlans, c.groups, vErrs = checkVLANs(in.VLANs)
	c.errs = append(c.errs, vErrs...)
	c.addProfiles()
	for _, sel := range in.Games {
		c.addGame(sel)
	}
	c.warnEmptyGroups()

	if err := errors.Join(c.errs...); err != nil {
		return nil, c.warns, err
	}
	snap, warns := c.snapshot(mode)
	return snap, warns, nil
}

// snapshot builds the rules of a configuration without mistakes and returns the hashed snapshot with the
// sorted warnings. Without limits of its own the configuration gets DefaultLimits.
func (c *compiler) snapshot(mode string) (*Snapshot, []Warning) {
	rules := buildRules(c.acc, c.vlans, c.warn)
	warns := sortedWarnings(c.warns)
	snap := &Snapshot{
		Version:  SnapshotVersion,
		Mode:     mode,
		SeedHash: c.in.DB.Hash(),
		VLANs:    c.vlans,
		Rules:    rules,
		Limits:   cmp.Or(c.in.Limits, DefaultLimits),
		Peers:    []Peer{},
		Probe:    c.in.Probe,
	}
	for _, w := range warns {
		snap.Warnings = append(snap.Warnings, w.String())
	}
	snap.Hash = hashOf(snap)
	return snap, warns
}

// addProfiles indexes the flow profiles by name; a name defined twice is a mistake (the last one is kept).
func (c *compiler) addProfiles() {
	for _, p := range c.in.Profiles {
		if _, dup := c.profiles[p.Name]; dup {
			c.fail("flow profile %q defined twice", p.Name)
		}
		c.profiles[p.Name] = p
	}
}

// warnEmptyGroups warns about groups whose VLANs are all switched off.
func (c *compiler) warnEmptyGroups() {
	for grp, n := range c.groups {
		if n == 0 {
			c.warn(WarnGroupEmpty, "group %q has no enabled VLAN", grp)
		}
	}
}

// sortedWarnings sorts the warnings by code and message and drops repeated ones.
func sortedWarnings(warns []Warning) []Warning {
	slices.SortFunc(warns, func(a, b Warning) int { return cmp.Or(cmp.Compare(a.Code, b.Code), cmp.Compare(a.Message, b.Message)) })
	return slices.Compact(warns)
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
