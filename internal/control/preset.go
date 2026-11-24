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
	vlan := func(vid int, name, group string, third, host int) store.VLANConfig {
		return store.VLANConfig{VID: vid, Name: name, Group: group, CIDR: fmt.Sprintf("10.10.%d.0/24", third),
			RelayIP: fmt.Sprintf("10.10.%d.%d", third, host), Attach: compile.AttachTrunk, Parent: parent, Enabled: true}
	}
	var event string
	var vlans []store.VLANConfig
	switch name {
	case PresetLAN:
		event = "LAN"
		vlans = append(vlans, vlan(100, "Gameserver", compile.GroupGameserver, 0, 250))
		for n := 1; n <= 4; n++ {
			vlans = append(vlans, vlan(100+n, fmt.Sprintf("Tisch %d", n), compile.GroupTables, n, 250))
		}
	case PresetNetsession:
		event = "Netsession LAN"
		vlans = append(vlans, vlan(1100, "Gameserver", compile.GroupGameserver, 0, 55))
		for n := 1; n <= 14; n++ {
			vlans = append(vlans, vlan(1100+n, fmt.Sprintf("Tisch %d", n), compile.GroupTables, n, 55))
		}
		vlans = append(vlans, vlan(1253, "User-WiFi", compile.GroupWiFi, 253, 55), vlan(1254, "Orga", compile.GroupOrga, 254, 55))
	case PresetLab:
		event = "Labor"
		vlans = append(vlans, vlan(1100, "Gameserver", compile.GroupGameserver, 0, 55))
		for n := 1; n <= 3; n++ {
			vlans = append(vlans, vlan(1100+n, fmt.Sprintf("Tisch %d", n), compile.GroupTables, n, 55))
		}
	default:
		return store.Config{}, fmt.Errorf("%w %q (%s)", ErrUnknownPreset, name, Presets)
	}
	return SetupConfig(event, compile.ModeObserve, vlans, db), nil
}

// SetupConfig is the configuration the first-run setup in the browser creates: the given networks, the default
// matrix (tables, game servers and orga see each other; only groups that have a network) and the games the
// database switches on by default.
func SetupConfig(event, mode string, vlans []store.VLANConfig, db *gamedb.DB) store.Config {
	c := store.Config{Version: 1, Event: event, Mode: mode, VLANs: vlans}
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
	c.Profiles = []store.ProfileConfig{p}
	for _, g := range db.Games {
		if g.DefaultEnabled {
			c.Games = append(c.Games, store.EventGameConfig{ID: g.ID})
		}
	}
	return c
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
	if _, err := st.ActiveEvent(ctx); err == nil && !force {
		return res, errors.New("configuration exists already (use -force to replace it)")
	}
	c, err := Preset(preset, parent, seed)
	if err != nil {
		return res, err
	}
	return res, st.ImportConfig(ctx, user, c)
}
