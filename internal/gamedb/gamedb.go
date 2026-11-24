// Package gamedb loads and validates the game database (games/games.yaml) and syncs it into the store.
package gamedb

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Netsession-LAN/Netsession-Relay/games"
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
