package store

// The configuration of the active event as one document (backup export, revision content) and the engine mode.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
)

// VLANConfig is one VLAN in the configuration export.
type VLANConfig struct {
	VID     int    `json:"vid" yaml:"vid"`
	Name    string `json:"name" yaml:"name"`
	Group   string `json:"group" yaml:"group"`
	CIDR    string `json:"cidr" yaml:"cidr"`
	RelayIP string `json:"relay_ip,omitempty" yaml:"relay_ip,omitempty"`
	Attach  string `json:"attach" yaml:"attach"`
	Parent  string `json:"parent,omitempty" yaml:"parent,omitempty"`
	Device  string `json:"device,omitempty" yaml:"device,omitempty"`
	RatePPS int    `json:"rate_pps,omitempty" yaml:"rate_pps,omitempty"`
	Burst   int    `json:"rate_burst,omitempty" yaml:"rate_burst,omitempty"`
	Enabled bool   `json:"enabled" yaml:"enabled"` // in the active event
}

// ProfileConfig is a flow profile.
type ProfileConfig struct {
	Name    string         `json:"name" yaml:"name"`
	Default bool           `json:"default,omitempty" yaml:"default,omitempty"`
	Edges   []compile.Edge `json:"edges" yaml:"edges"`
}

// EventGameConfig is a game in the active event.
type EventGameConfig struct {
	ID          string           `json:"id" yaml:"id"`
	FlowProfile string           `json:"flow_profile,omitempty" yaml:"flow_profile,omitempty"`
	Override    compile.Override `json:"override,omitempty" yaml:"override,omitempty"`
	// Sources is the optional source filter (compile.Selection.Sources); omitted when empty.
	Sources []string `json:"sources,omitempty" yaml:"sources,omitempty"`
	// Off: switched off, listed only to keep its settings (profile, overrides, sources) for switching on again.
	Off bool `json:"off,omitempty" yaml:"off,omitempty"`
}

// Config is the complete configuration of the active event (backup export, revision content).
type Config struct {
	Version  int               `json:"version" yaml:"version"`
	Event    string            `json:"event" yaml:"event"`
	Mode     string            `json:"mode" yaml:"mode"`
	VLANs    []VLANConfig      `json:"vlans" yaml:"vlans"`
	Profiles []ProfileConfig   `json:"profiles" yaml:"profiles"`
	Games    []EventGameConfig `json:"games" yaml:"games"`
}

// ExportConfig reads the active configuration.
func (s *Store) ExportConfig(ctx context.Context) (Config, error) {
	c := Config{Version: 1}
	ev, err := s.activeEventID(ctx)
	if err != nil {
		return c, err
	}
	if c.Event, err = s.ActiveEvent(ctx); err != nil {
		return c, err
	}
	if c.Mode, err = s.Setting(ctx, SettingMode, compile.ModeActive); err != nil {
		return c, err
	}
	if c.VLANs, err = s.exportVLANs(ctx, ev); err != nil {
		return c, err
	}
	if c.Profiles, err = s.exportProfiles(ctx); err != nil {
		return c, err
	}
	c.Games, err = s.exportGames(ctx, ev)
	return c, err
}

func (s *Store) exportVLANs(ctx context.Context, ev int64) ([]VLANConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT v.vid, v.name, v.grp, v.cidr, v.relay_ip, v.attach, v.parent, v.device, v.rate_pps, v.rate_burst,
		COALESCE(ev.enabled, 0) FROM vlan v LEFT JOIN event_vlan ev ON ev.vid = v.vid AND ev.event_id = ? ORDER BY v.vid`, ev)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VLANConfig
	for rows.Next() {
		var v VLANConfig
		if err := rows.Scan(&v.VID, &v.Name, &v.Group, &v.CIDR, &v.RelayIP, &v.Attach, &v.Parent, &v.Device, &v.RatePPS, &v.Burst, &v.Enabled); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) exportProfiles(ctx context.Context) ([]ProfileConfig, error) {
	out, err := s.profileNames(ctx)
	if err != nil {
		return nil, err
	}
	// the database has one connection: the list of names is read completely before the edges are
	for i := range out {
		if out[i].Edges, err = profileEdges(ctx, s.db, out[i].Name); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) exportGames(ctx context.Context, ev int64) ([]EventGameConfig, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT game_id, flow_profile, override, sources, enabled FROM event_game WHERE event_id = ? ORDER BY game_id`, ev)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EventGameConfig
	for rows.Next() {
		var g EventGameConfig
		var ov, sources string
		var enabled bool
		if err := rows.Scan(&g.ID, &g.FlowProfile, &ov, &sources, &enabled); err != nil {
			return nil, err
		}
		g.Sources = sourceList(sources)
		_ = json.Unmarshal([]byte(ov), &g.Override)
		g.Off = !enabled
		if g.Off && keepsNothing(g, ov) {
			continue
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// keepsNothing reports whether a game of the event has no settings of its own (flow profile, sources,
// override as stored): switched off, such a game is left out of the export.
func keepsNothing(g EventGameConfig, override string) bool {
	noOverride := override == "" || override == "{}" || override == "null"
	return g.FlowProfile == "" && len(g.Sources) == 0 && noOverride
}

// SetMode stores the engine mode for the next apply.
func (s *Store) SetMode(ctx context.Context, user, mode string) error {
	switch mode {
	case compile.ModeActive, compile.ModeObserve, compile.ModePaused:
	default:
		return fmt.Errorf("%w: mode %q (active|observe|paused)", ErrInvalid, mode)
	}
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		e := AuditEntry{User: user, Action: "mode.set", Object: "engine", Before: compile.ModeActive, After: mode}
		if err := tx.QueryRowContext(ctx, `SELECT value FROM setting WHERE key = ?`, SettingMode).Scan(&e.Before); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return e, err
		}
		_, err := tx.ExecContext(ctx, upsertSetting, SettingMode, mode)
		return e, err
	})
}
