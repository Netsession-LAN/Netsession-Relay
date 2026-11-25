package control

import (
	"context"
	"errors"
	"fmt"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
	"github.com/Netsession-LAN/Netsession-Relay/internal/store"
)

// Presets of `nsrelay admin init`. The usual way to a first configuration is the setup in the browser; the
// presets are for scripted installations and the lab. Like the setup, every preset starts in the mode observe:
// the relay counts and warns, and sends only after `nsrelay admin mode active` and an apply.
const (
	// PresetLAN is a small example to start from: game servers in VLAN 100 (10.10.0.0/24), four tables in VLANs
	// 101-104 (10.10.n.0/24), relay IP .250 (what the setup in the browser suggests).
	PresetLAN = "lan"
	// PresetNetsession is the event network of the Netsession LAN party, as an example of a bigger event: game
	// servers 1100 = 10.10.0.0/24, table n = VLAN 1100+n = 10.10.n.0/24 (n = 1..14), WiFi 1253, orga 1254, relay
	// IP .55, WiFi not in the matrix.
	PresetNetsession = "netsession"
	// PresetLab is the layout of the network lab (lab/up.sh): VLANs 1100-1103.
	PresetLab = "lab"
)

// Presets lists the preset names for help texts.
const Presets = PresetLAN + "|" + PresetNetsession + "|" + PresetLab

// ErrUnknownPreset is returned by Preset and Init for a name that is not a preset.
var ErrUnknownPreset = errors.New("unknown preset")

// Preset returns an initial configuration: the networks of the preset on the trunk parent, the default matrix
// between the groups that have a network, and the games the database switches on by default.
func Preset(name, parent string, db *gamedb.DB) (store.Config, error) {
	l, ok := presetLayouts[name]
	if !ok {
		return store.Config{}, fmt.Errorf("%w %q (%s)", ErrUnknownPreset, name, Presets)
	}
	return SetupConfig(l.event, compile.ModeObserve, l.vlans(parent), db), nil
}

// presetLayout is the network of a preset: the game servers in VLAN base (10.10.0.0/24), table n in VLAN base+n
// (10.10.n.0/24), then the extra networks; the relay has host address host in each of them.
type presetLayout struct {
	event  string
	base   int
	tables int
	host   int
	extra  []presetNet
}

// presetNet is a network of a preset besides game servers and tables: 10.10.third.0/24.
type presetNet struct {
	vid         int
	name, group string
	third       int
}

var presetLayouts = map[string]presetLayout{
	PresetLAN: {event: "LAN", base: 100, tables: 4, host: 250},
	PresetNetsession: {event: "Netsession LAN", base: 1100, tables: 14, host: 55,
		extra: []presetNet{{1253, "User-WiFi", compile.GroupWiFi, 253}, {1254, "Orga", compile.GroupOrga, 254}}},
	PresetLab: {event: "Labor", base: 1100, tables: 3, host: 55},
}

// vlans returns the networks of the layout on the trunk parent.
func (l presetLayout) vlans(parent string) []store.VLANConfig {
	vlan := func(vid int, name, group string, third int) store.VLANConfig {
		return store.VLANConfig{VID: vid, Name: name, Group: group, CIDR: fmt.Sprintf("10.10.%d.0/24", third),
			RelayIP: fmt.Sprintf("10.10.%d.%d", third, l.host), Attach: compile.AttachTrunk, Parent: parent, Enabled: true}
	}
	vlans := []store.VLANConfig{vlan(l.base, "Gameserver", compile.GroupGameserver, 0)}
	for n := 1; n <= l.tables; n++ {
		vlans = append(vlans, vlan(l.base+n, fmt.Sprintf("Tisch %d", n), compile.GroupTables, n))
	}
	for _, x := range l.extra {
		vlans = append(vlans, vlan(x.vid, x.name, x.group, x.third))
	}
	return vlans
}

// SetupConfig is the configuration the first-run setup in the browser creates: the given networks, the default
// matrix (tables, game servers and orga see each other; only groups that have a network) and the games the
// database switches on by default.
func SetupConfig(event, mode string, vlans []store.VLANConfig, db *gamedb.DB) store.Config {
	return store.Config{Version: 1, Event: event, Mode: mode, VLANs: vlans,
		Profiles: []store.ProfileConfig{defaultProfile(vlans)}, Games: defaultGames(db)}
}

// defaultProfile is the default flow profile limited to the groups that have a network.
func defaultProfile(vlans []store.VLANConfig) store.ProfileConfig {
	used := map[string]bool{}
	for _, v := range vlans {
		used[v.Group] = true
	}
	prof := compile.DefaultFlowProfile()
	p := store.ProfileConfig{Name: prof.Name, Default: true}
	for _, e := range prof.Edges {
		if used[e.Src] && used[e.Dst] {
			p.Edges = append(p.Edges, e)
		}
	}
	return p
}

// defaultGames are the games the database switches on by default.
func defaultGames(db *gamedb.DB) []store.EventGameConfig {
	var games []store.EventGameConfig
	for _, g := range db.Games {
		if g.DefaultEnabled {
			games = append(games, store.EventGameConfig{ID: g.ID})
		}
	}
	return games
}

// Init creates a fresh configuration: seed games plus preset, mode observe. Refuses if a configuration exists
// unless force.
func Init(ctx context.Context, st *store.Store, user, preset, parent string, force bool) (store.SeedResult, error) {
	seed, err := gamedb.Seed()
	if err != nil {
		return store.SeedResult{}, err
	}
	res, err := st.SyncSeed(ctx, seed)
	if err != nil {
		return res, err
	}
	if err := refuseExisting(ctx, st, force); err != nil {
		return res, err
	}
	c, err := Preset(preset, parent, seed)
	if err != nil {
		return res, err
	}
	return res, st.ImportConfig(ctx, user, c)
}

// refuseExisting fails when an event is configured already, unless force.
func refuseExisting(ctx context.Context, st *store.Store, force bool) error {
	if _, err := st.ActiveEvent(ctx); err == nil && !force {
		return errors.New("configuration exists already (use -force to replace it)")
	}
	return nil
}
