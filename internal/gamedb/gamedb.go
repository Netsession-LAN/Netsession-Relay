// Package gamedb loads and validates the game database (games/games.yaml) and syncs it into the store.
package gamedb

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Netsession-LAN/Netsession-Relay/games"
	"github.com/Netsession-LAN/Netsession-Relay/internal/portspec"
)

// Enumerations used in games.yaml and in the snapshot.
const (
	CategoryGame     = "game"
	CategoryPlatform = "platform"
	CategoryNoise    = "noise"

	RelayYes     = "yes"
	RelayNo      = "no"
	RelayUnknown = "unknown"

	KindBroadcast = "broadcast"
	KindMulticast = "multicast"

	SourceOriginal = "original"
	SourceRelay    = "relay"

	DstKeep    = "keep"
	DstSubnet  = "subnet"
	DstLimited = "limited"

	TTLKeep      = "keep"
	TTLDecrement = "decrement"
)

// Defaults applied when a spec leaves the field empty.
const (
	DefaultRatePPS    = 5
	DefaultRateBurst  = 10
	DefaultPayloadMax = 1472
)

// Game is one entry of games.yaml.
type Game struct {
	ID             string            `yaml:"id" json:"id"`
	Name           string            `yaml:"name" json:"name"`
	Aliases        []string          `yaml:"aliases,omitempty" json:"aliases,omitempty"`
	Category       string            `yaml:"category" json:"category"`
	RelayNeeded    string            `yaml:"relay_needed" json:"relay_needed"`
	DefaultEnabled bool              `yaml:"default_enabled" json:"default_enabled"`
	Confidence     string            `yaml:"confidence" json:"confidence"`
	SourceIP       string            `yaml:"source_ip" json:"source_ip"`
	Discovery      []Spec            `yaml:"discovery,omitempty" json:"discovery,omitempty"`
	GamePorts      map[string]string `yaml:"game_ports,omitempty" json:"game_ports,omitempty"`
	// Pinned lists the game first inside its group of the games list: the titles an event is known for.
	Pinned  bool     `yaml:"pinned,omitempty" json:"pinned,omitempty"`
	Notes   string   `yaml:"notes,omitempty" json:"notes,omitempty"`
	Sources []string `yaml:"sources,omitempty" json:"sources,omitempty"`
}

// Spec is one discovery rule of a game. Empty optional fields mean "use the default" (see Effective).
type Spec struct {
	Kind       string `yaml:"kind" json:"kind"`
	Ports      string `yaml:"ports" json:"ports"`
	Group      string `yaml:"group,omitempty" json:"group,omitempty"`
	Direction  string `yaml:"direction,omitempty" json:"direction,omitempty"`
	SourceMode string `yaml:"source_mode,omitempty" json:"source_mode,omitempty"`
	DstMode    string `yaml:"dst_mode,omitempty" json:"dst_mode,omitempty"`
	TTLMode    string `yaml:"ttl_mode,omitempty" json:"ttl_mode,omitempty"`
	RatePPS    int    `yaml:"rate_pps,omitempty" json:"rate_pps,omitempty"`
	RateBurst  int    `yaml:"rate_burst,omitempty" json:"rate_burst,omitempty"`
	PayloadMax int    `yaml:"payload_max,omitempty" json:"payload_max,omitempty"`
}

// Effective returns the spec with defaults filled in. Source mode defaults to original for every game:
// games that older relay setups ran with a rewritten source address work with the original address, too.
func (s Spec) Effective() Spec {
	if s.SourceMode == "" {
		s.SourceMode = SourceOriginal
	}
	if s.DstMode == "" {
		s.DstMode = DstKeep
	}
	if s.TTLMode == "" {
		s.TTLMode = TTLKeep
	}
	if s.RatePPS == 0 {
		s.RatePPS = DefaultRatePPS
	}
	if s.RateBurst == 0 {
		s.RateBurst = DefaultRateBurst
	}
	if s.PayloadMax == 0 {
		s.PayloadMax = DefaultPayloadMax
	}
	return s
}

// Blocked ports are never relayed, whatever is configured (DHCP).
var blockedPorts = []uint16{67, 68}

// DB is a validated game database.
type DB struct {
	Games []Game
	byID  map[string]int
	hash  string
}

// Get returns the game with the given id.
func (db *DB) Get(id string) (Game, bool) {
	i, ok := db.byID[id]
	if !ok {
		return Game{}, false
	}
	return db.Games[i], true
}

// Hash is the SHA-256 over the games (see New); it appears as seed_hash in snapshots.
func (db *DB) Hash() string { return db.hash }

// Seed loads the embedded games/games.yaml.
func Seed() (*DB, error) { return Load(bytes.NewReader(games.SeedYAML)) }

// Load parses and validates a games.yaml document. Unknown fields are errors.
func Load(r io.Reader) (*DB, error) {
	src, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(src))
	dec.KnownFields(true)
	var list []Game
	if err := dec.Decode(&list); err != nil {
		return nil, fmt.Errorf("gamedb: %w", err)
	}
	return New(list)
}

// New builds a validated DB from a list (games stored in the database, possibly edited locally).
// Hash covers the JSON encoding of the games sorted by ID, so the same games give the same hash whether
// they come from games.yaml or from the store.
func New(list []Game) (*DB, error) {
	if err := Validate(list); err != nil {
		return nil, err
	}
	sorted := slices.Clone(list)
	slices.SortFunc(sorted, func(a, b Game) int { return strings.Compare(a.ID, b.ID) })
	b, err := json.Marshal(sorted)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	db := &DB{Games: list, byID: make(map[string]int, len(list)), hash: hex.EncodeToString(sum[:])}
	for i, g := range list {
		db.byID[g.ID] = i
	}
	return db, nil
}

// GameHash is a stable hash of one game (seed_hash in the store).
func GameHash(g Game) string {
	b, _ := json.Marshal(g)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:16])
}

var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_]*$`)

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// ErrInvalid matches every error of Validate (errors.Is): something is wrong with the entries themselves. The
// text of the error lists the problems.
var ErrInvalid = errors.New("invalid game")

// invalidError is the error of Validate: the joined problems, which also is an ErrInvalid.
type invalidError struct{ problems error }

func (e invalidError) Error() string        { return e.problems.Error() }
func (e invalidError) Unwrap() error        { return e.problems }
func (e invalidError) Is(target error) bool { return target == ErrInvalid }

// Problems of Validate a caller can tell apart with errors.Is (the web UI words them); every other problem is
// only described by its text.
var (
	ErrInvalidID   = errors.New("invalid id")
	ErrNameMissing = errors.New("name missing")
	ErrBlockedPort = errors.New("blocked port (DHCP is never relayed)")
)

// Validate checks every game and returns all problems joined into one error.
func Validate(list []Game) error {
	var errs []error
	seen := map[string]bool{}
	add := func(g Game, format string, args ...any) {
		errs = append(errs, fmt.Errorf("game %q: %w", g.ID, fmt.Errorf(format, args...)))
	}
	for _, g := range list {
		if !idRE.MatchString(g.ID) {
			add(g, "%w", ErrInvalidID)
		}
		if seen[g.ID] {
			add(g, "duplicate id")
		}
		seen[g.ID] = true
		if strings.TrimSpace(g.Name) == "" {
			add(g, "%w", ErrNameMissing)
		}
		if !oneOf(g.Category, CategoryGame, CategoryPlatform, CategoryNoise) {
			add(g, "category %q invalid", g.Category)
		}
		if !oneOf(g.RelayNeeded, RelayYes, RelayNo, RelayUnknown) {
			add(g, "relay_needed %q invalid", g.RelayNeeded)
		}
		if !oneOf(g.Confidence, "high", "medium", "low") {
			add(g, "confidence %q invalid", g.Confidence)
		}
		if !oneOf(g.SourceIP, SourceOriginal, SourceRelay, "unknown") {
			add(g, "source_ip %q invalid", g.SourceIP)
		}
		switch {
		case g.RelayNeeded == RelayYes && len(g.Discovery) == 0:
			add(g, "relay_needed=yes but no discovery spec")
		case g.RelayNeeded != RelayYes && len(g.Discovery) > 0:
			add(g, "discovery specs present but relay_needed=%s", g.RelayNeeded)
		}
		if g.DefaultEnabled && g.RelayNeeded != RelayYes {
			add(g, "default_enabled requires relay_needed=yes")
		}
		for proto, ps := range g.GamePorts {
			if !oneOf(proto, "udp", "tcp") {
				add(g, "game_ports: unknown protocol %q", proto)
			}
			if _, err := portspec.Parse(ps); err != nil {
				add(g, "game_ports.%s: %v", proto, err)
			}
		}
		for i, s := range g.Discovery {
			for _, e := range validateSpec(s) {
				add(g, "discovery[%d]: %w", i, e)
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return invalidError{errors.Join(errs...)}
}

func validateSpec(s Spec) []error {
	var errs []error
	ps, err := portspec.Parse(s.Ports)
	if err != nil {
		errs = append(errs, err)
	} else {
		for _, b := range blockedPorts {
			if ps.Contains(b) {
				errs = append(errs, fmt.Errorf("port %d: %w", b, ErrBlockedPort))
			}
		}
	}
	switch s.Kind {
	case KindBroadcast:
		if s.Group != "" {
			errs = append(errs, errors.New("broadcast spec must not have a group"))
		}
	case KindMulticast:
		g, err := netip.ParseAddr(s.Group)
		if err != nil || !g.Is4() || !g.IsMulticast() {
			errs = append(errs, fmt.Errorf("group %q is not an IPv4 multicast address", s.Group))
		}
	default:
		errs = append(errs, fmt.Errorf("kind %q invalid", s.Kind))
	}
	if s.Direction != "" && !oneOf(s.Direction, "query", "announce", "both") {
		errs = append(errs, fmt.Errorf("direction %q invalid", s.Direction))
	}
	if s.SourceMode != "" && !oneOf(s.SourceMode, SourceOriginal, SourceRelay) {
		errs = append(errs, fmt.Errorf("source_mode %q invalid", s.SourceMode))
	}
	if s.DstMode != "" && !oneOf(s.DstMode, DstKeep, DstSubnet, DstLimited) {
		errs = append(errs, fmt.Errorf("dst_mode %q invalid", s.DstMode))
	}
	if s.TTLMode != "" && !oneOf(s.TTLMode, TTLKeep, TTLDecrement) {
		errs = append(errs, fmt.Errorf("ttl_mode %q invalid", s.TTLMode))
	}
	if s.RatePPS < 0 || s.RateBurst < 0 || (s.RateBurst > 0 && s.RateBurst < s.RatePPS) {
		errs = append(errs, fmt.Errorf("rate_pps/rate_burst invalid (%d/%d)", s.RatePPS, s.RateBurst))
	}
	if s.PayloadMax < 0 || s.PayloadMax > DefaultPayloadMax {
		errs = append(errs, fmt.Errorf("payload_max %d out of range 1..%d", s.PayloadMax, DefaultPayloadMax))
	}
	return errs
}
