package store

// Compiler input: a configuration document turned into compile.Input.

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
)

// SettingProbeServers switches the server probe ("1" default, "0" off), see compile.Input.Probe.
const SettingProbeServers = "probe_servers"

// BuildInput turns the active configuration into compiler input.
func (s *Store) BuildInput(ctx context.Context) (compile.Input, error) {
	c, err := s.ExportConfig(ctx)
	if err != nil {
		return compile.Input{}, err
	}
	return s.InputFor(ctx, c)
}

// InputFor turns a configuration the caller already read (ExportConfig) into compiler input, with the stored
// games and settings.
func (s *Store) InputFor(ctx context.Context, c Config) (compile.Input, error) {
	db, err := s.GameDB(ctx)
	if err != nil {
		return compile.Input{}, err
	}
	probe, _ := s.Setting(ctx, SettingProbeServers, "1")
	return ConfigInput(c, db, probe != "0")
}

// ConfigInput turns a configuration into compiler input without storing it (BuildInput, and the first-run
// setup, which checks its configuration before it saves anything).
func ConfigInput(c Config, db *gamedb.DB, probe bool) (compile.Input, error) {
	in := compile.Input{DB: db, Mode: c.Mode, Probe: probe}
	var err error
	if in.VLANs, err = compileVLANs(c.VLANs); err != nil {
		return in, err
	}
	for _, p := range c.Profiles {
		in.Profiles = append(in.Profiles, compile.FlowProfile{Name: p.Name, Edges: p.Edges})
		if p.Default {
			in.DefaultProfile = p.Name
		}
	}
	in.Games, err = selectedGames(c.Games)
	return in, err
}

// compileVLANs parses the VLANs of a configuration; on an error it returns those before the faulty one.
func compileVLANs(vlans []VLANConfig) ([]compile.VLAN, error) {
	var out []compile.VLAN
	for _, v := range vlans {
		cv, err := compileVLAN(v)
		if err != nil {
			return out, fmt.Errorf("VLAN %d: %w", v.VID, err)
		}
		out = append(out, cv)
	}
	return out, nil
}

// selectedGames returns the switched-on games of a configuration; on an error it returns those before the
// faulty one.
func selectedGames(games []EventGameConfig) ([]compile.Selection, error) {
	var out []compile.Selection
	for _, g := range games {
		if g.Off {
			continue
		}
		sources, err := gameSourcesOf(g)
		if err != nil {
			return out, err
		}
		out = append(out, compile.Selection{GameID: g.ID, FlowProfile: g.FlowProfile, Override: g.Override, Sources: sources})
	}
	return out, nil
}

// compileVLAN parses the addresses of a VLAN; empty ones stay unset (the compiler reports them).
func compileVLAN(v VLANConfig) (compile.VLAN, error) {
	cv := compile.VLAN{VID: v.VID, Name: v.Name, Group: v.Group, Attach: v.Attach, Parent: v.Parent, Device: v.Device,
		Enabled: v.Enabled, RatePPS: v.RatePPS, Burst: v.Burst}
	var err error
	if v.CIDR != "" {
		if cv.CIDR, err = netip.ParsePrefix(v.CIDR); err != nil {
			return cv, err
		}
	}
	if v.RelayIP != "" {
		cv.RelayIP, err = netip.ParseAddr(v.RelayIP)
	}
	return cv, err
}

// gameSourcesOf parses the source filter of a game in a configuration document.
func gameSourcesOf(g EventGameConfig) ([]netip.Prefix, error) {
	sources, err := compile.ParseSources(strings.Join(g.Sources, " "))
	if err != nil {
		return nil, fmt.Errorf("game %q: sources: %w", g.ID, err)
	}
	return sources, nil
}
