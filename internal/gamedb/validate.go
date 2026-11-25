package gamedb

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/portspec"
)

// Blocked ports are never relayed, whatever is configured (DHCP).
var blockedPorts = []uint16{67, 68}

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
	for _, g := range list {
		for _, e := range gameProblems(g, seen[g.ID]) {
			errs = append(errs, fmt.Errorf("game %q: %w", g.ID, e))
		}
		seen[g.ID] = true
	}
	if len(errs) == 0 {
		return nil
	}
	return invalidError{errors.Join(errs...)}
}

// problems collects what is wrong with one game or one spec, in the order the checks find it.
type problems []error

func (p *problems) add(format string, args ...any) { *p = append(*p, fmt.Errorf(format, args...)) }

// enumField is a field of a game or spec that only takes the listed values.
type enumField[T any] struct {
	name     string
	value    func(T) string
	allowed  []string
	optional bool // empty is allowed and means "use the default" (see Spec.Effective)
}

func (f enumField[T]) check(p *problems, x T) {
	v := f.value(x)
	if f.optional && v == "" || oneOf(v, f.allowed...) {
		return
	}
	p.add("%s %q invalid", f.name, v)
}

var gameEnums = []enumField[Game]{
	{name: "category", value: func(g Game) string { return g.Category }, allowed: []string{CategoryGame, CategoryPlatform, CategoryNoise}},
	{name: "relay_needed", value: func(g Game) string { return g.RelayNeeded }, allowed: []string{RelayYes, RelayNo, RelayUnknown}},
	{name: "confidence", value: func(g Game) string { return g.Confidence }, allowed: []string{"high", "medium", "low"}},
	{name: "source_ip", value: func(g Game) string { return g.SourceIP }, allowed: []string{SourceOriginal, SourceRelay, "unknown"}},
}

var specEnums = []enumField[Spec]{
	{name: "direction", value: func(s Spec) string { return s.Direction }, allowed: []string{"query", "announce", "both"}, optional: true},
	{name: "source_mode", value: func(s Spec) string { return s.SourceMode }, allowed: []string{SourceOriginal, SourceRelay}, optional: true},
	{name: "dst_mode", value: func(s Spec) string { return s.DstMode }, allowed: []string{DstKeep, DstSubnet, DstLimited}, optional: true},
	{name: "ttl_mode", value: func(s Spec) string { return s.TTLMode }, allowed: []string{TTLKeep, TTLDecrement}, optional: true},
}

// gameProblems checks one game; duplicate says an earlier game of the list has the same ID.
func gameProblems(g Game, duplicate bool) problems {
	var p problems
	if !idRE.MatchString(g.ID) {
		p.add("%w", ErrInvalidID)
	}
	if duplicate {
		p.add("duplicate id")
	}
	if strings.TrimSpace(g.Name) == "" {
		p.add("%w", ErrNameMissing)
	}
	for _, f := range gameEnums {
		f.check(&p, g)
	}
	p.checkRelay(g)
	p.checkGamePorts(g.GamePorts)
	p.checkDiscovery(g.Discovery)
	return p
}

// checkDiscovery checks every discovery spec; a problem names the spec's index.
func (p *problems) checkDiscovery(specs []Spec) {
	for i, s := range specs {
		for _, e := range specProblems(s) {
			p.add("discovery[%d]: %w", i, e)
		}
	}
}

// checkRelay checks that discovery specs and relay_needed=yes go together, and that only a game that needs the
// relay is switched on by default.
func (p *problems) checkRelay(g Game) {
	relays := g.RelayNeeded == RelayYes
	hasSpecs := len(g.Discovery) > 0
	switch {
	case relays == hasSpecs:
	case relays:
		p.add("relay_needed=yes but no discovery spec")
	default:
		p.add("discovery specs present but relay_needed=%s", g.RelayNeeded)
	}
	if g.DefaultEnabled && !relays {
		p.add("default_enabled requires relay_needed=yes")
	}
}

// checkGamePorts checks the informational game ports (protocol -> port spec).
func (p *problems) checkGamePorts(ports map[string]string) {
	for proto, ps := range ports {
		if !oneOf(proto, "udp", "tcp") {
			p.add("game_ports: unknown protocol %q", proto)
		}
		if _, err := portspec.Parse(ps); err != nil {
			p.add("game_ports.%s: %v", proto, err)
		}
	}
}
