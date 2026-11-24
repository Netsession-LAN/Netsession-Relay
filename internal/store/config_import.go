package store

// Import of a configuration document (restore, rollback): it replaces the stored configuration in one transaction.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
)

// ImportConfig replaces mode, VLANs, profiles and the active event's VLANs and games with c (one
// transaction). The event is created if it does not exist and becomes the active one. Games referenced
// by c must exist in the game table.
func (s *Store) ImportConfig(ctx context.Context, user string, c Config) error {
	before, _ := s.ExportConfig(ctx)
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(c)
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		return AuditEntry{User: user, Action: "config.import", Object: c.Event, Before: string(b), After: string(a)}, s.importConfig(ctx, tx, c)
	})
}

// importConfig writes c in the order the foreign keys need: global settings and the VLAN and profile
// definitions first, then the event and what it switches on.
func (s *Store) importConfig(ctx context.Context, tx *sql.Tx, c Config) error {
	if err := importMode(ctx, tx, c.Mode); err != nil {
		return err
	}
	if err := clearDefinitions(ctx, tx); err != nil {
		return err
	}
	for _, v := range c.VLANs {
		if err := insertVLAN(ctx, tx, v); err != nil {
			return fmt.Errorf("VLAN %d: %w", v.VID, err)
		}
	}
	for _, p := range c.Profiles {
		if err := insertProfile(ctx, tx, p); err != nil {
			return err
		}
	}
	ev, err := s.activateEvent(ctx, tx, c.Event)
	if err != nil {
		return err
	}
	if err := insertEventVLANs(ctx, tx, ev, c.VLANs); err != nil {
		return err
	}
	return replaceEventGames(ctx, tx, ev, c.Games)
}

// importMode stores the engine mode of the document; a document without one keeps the stored mode.
func importMode(ctx context.Context, tx *sql.Tx, mode string) error {
	if mode == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, upsertSetting, SettingMode, mode)
	return err
}

// clearDefinitions removes all VLANs and flow profiles (and with them what events refer to).
func clearDefinitions(ctx context.Context, tx *sql.Tx) error {
	for _, stmt := range []string{`DELETE FROM flow_profile_edge`, `DELETE FROM flow_profile`, `DELETE FROM event_vlan`, `DELETE FROM vlan`} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func insertProfile(ctx context.Context, tx *sql.Tx, p ProfileConfig) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO flow_profile(name, is_default) VALUES (?, ?)`, p.Name, p.Default); err != nil {
		return err
	}
	return insertEdges(ctx, tx, p.Name, p.Edges)
}

// activateEvent makes the named event the active one, creating it when it does not exist, and returns its ID.
func (s *Store) activateEvent(ctx context.Context, tx *sql.Tx, name string) (int64, error) {
	if _, err := tx.ExecContext(ctx, `UPDATE event SET is_active = 0`); err != nil {
		return 0, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO event(name, is_active, created) VALUES (?, 1, ?) ON CONFLICT(name) DO UPDATE SET is_active = 1`,
		name, s.ts()); err != nil {
		return 0, err
	}
	var ev int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM event WHERE name = ?`, name).Scan(&ev)
	return ev, err
}

func insertEventVLANs(ctx context.Context, tx *sql.Tx, ev int64, vlans []VLANConfig) error {
	for _, v := range vlans {
		if _, err := tx.ExecContext(ctx, `INSERT INTO event_vlan(event_id, vid, enabled) VALUES (?,?,?)`, ev, v.VID, v.Enabled); err != nil {
			return err
		}
	}
	return nil
}

// replaceEventGames replaces the games of the event, keeping switched-off ones with their settings.
func replaceEventGames(ctx context.Context, tx *sql.Tx, ev int64, games []EventGameConfig) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM event_game WHERE event_id = ?`, ev); err != nil {
		return err
	}
	for _, g := range games {
		ov, _ := json.Marshal(g.Override)
		sources, err := gameSourcesOf(g)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO event_game(event_id, game_id, flow_profile, override, sources, enabled) VALUES (?,?,?,?,?,?)`,
			ev, g.ID, g.FlowProfile, string(ov), strings.Join(compile.FormatSources(sources), " "), !g.Off); err != nil {
			return fmt.Errorf("game %q: %w", g.ID, err)
		}
	}
	return nil
}
