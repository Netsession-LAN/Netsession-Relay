package store

// VLANs: the game networks and whether they are on in the active event.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
)

// SetVLANEnabled switches a VLAN on or off in the active event (number of tables changes per event).
func (s *Store) SetVLANEnabled(ctx context.Context, user string, vid int, on bool) error {
	ev, err := s.activeEventID(ctx)
	if err != nil {
		return err
	}
	action := "vlan.disable"
	if on {
		action = "vlan.enable"
	}
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		e := AuditEntry{User: user, Action: action, Object: fmt.Sprint(vid)}
		res, err := tx.ExecContext(ctx, `UPDATE event_vlan SET enabled = ? WHERE event_id = ? AND vid = ?`, on, ev, vid)
		if err != nil {
			return e, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return e, fmt.Errorf("%w: VLAN %d", ErrNotFound, vid)
		}
		return e, nil
	})
}

// VLAN returns one VLAN with its state in the active event.
func (s *Store) VLAN(ctx context.Context, vid int) (VLANConfig, error) {
	c, err := s.ExportConfig(ctx)
	if err != nil {
		return VLANConfig{}, err
	}
	for _, v := range c.VLANs {
		if v.VID == vid {
			return v, nil
		}
	}
	return VLANConfig{}, fmt.Errorf("%w: VLAN %d", ErrNotFound, vid)
}

// AddVLAN stores a new VLAN and adds it to the active event (switched on or off as v.Enabled says).
// An existing VID is never overwritten (ErrExists).
func (s *Store) AddVLAN(ctx context.Context, user string, v VLANConfig) error {
	ev, err := s.activeEventID(ctx)
	if err != nil {
		return err
	}
	a, _ := json.Marshal(v)
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		e := AuditEntry{User: user, Action: "vlan.create", Object: fmt.Sprint(v.VID), After: string(a)}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM vlan WHERE vid = ?`, v.VID).Scan(&n); err != nil {
			return e, err
		}
		if n > 0 {
			return e, fmt.Errorf("%w: VLAN %d", ErrExists, v.VID)
		}
		if err := insertVLAN(ctx, tx, v); err != nil {
			return e, err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO event_vlan(event_id, vid, enabled) VALUES (?,?,?)`, ev, v.VID, v.Enabled)
		return e, err
	})
}

// UpdateVLAN changes name, group, addresses, interface and rate of an existing VLAN. The VID and the
// on/off state in the event stay as they are.
func (s *Store) UpdateVLAN(ctx context.Context, user string, v VLANConfig) error {
	before, err := s.VLAN(ctx, v.VID)
	if err != nil {
		return err
	}
	v.Enabled = before.Enabled
	b, _ := json.Marshal(before)
	a, _ := json.Marshal(v)
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		_, err := tx.ExecContext(ctx, `UPDATE vlan SET name = ?, grp = ?, cidr = ?, relay_ip = ?, attach = ?, parent = ?, device = ?, rate_pps = ?, rate_burst = ? WHERE vid = ?`,
			v.Name, v.Group, v.CIDR, v.RelayIP, v.Attach, v.Parent, v.Device, v.RatePPS, v.Burst, v.VID)
		return AuditEntry{User: user, Action: "vlan.update", Object: fmt.Sprint(v.VID), Before: string(b), After: string(a)}, err
	})
}

// DeleteVLAN removes a VLAN from the configuration and from all events.
func (s *Store) DeleteVLAN(ctx context.Context, user string, vid int) error {
	before, err := s.VLAN(ctx, vid)
	if err != nil {
		return err
	}
	b, _ := json.Marshal(before)
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		_, err := tx.ExecContext(ctx, `DELETE FROM vlan WHERE vid = ?`, vid) // event_vlan: ON DELETE CASCADE
		return AuditEntry{User: user, Action: "vlan.delete", Object: fmt.Sprint(vid), Before: string(b)}, err
	})
}

// insertVLAN stores the definition of a VLAN (not its state in an event).
func insertVLAN(ctx context.Context, tx *sql.Tx, v VLANConfig) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO vlan(vid, name, grp, cidr, relay_ip, attach, parent, device, rate_pps, rate_burst) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		v.VID, v.Name, v.Group, v.CIDR, v.RelayIP, v.Attach, v.Parent, v.Device, v.RatePPS, v.Burst)
	return err
}
