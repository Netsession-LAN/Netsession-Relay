package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// Monitoring targets: where alarms and audit entries are pushed (syslog, OTLP, SNMP traps). Not part of the
// event configuration: no revisions, no rollback, no YAML export.

// Target kinds.
const (
	TargetSyslog   = "syslog"
	TargetOTLP     = "otlp"
	TargetSNMPTrap = "snmptrap"
)

// MaxTargets limits the number of targets (memory: one queue each).
const MaxTargets = 20

// ErrTooManyTargets is returned by SaveTarget when MaxTargets targets exist already (it is an ErrInvalid).
var ErrTooManyTargets = fmt.Errorf("%w: too many monitoring targets", ErrInvalid)

// Target is one push destination. Config holds the non-secret fields of its kind, Secret the secret ones
// (community, passwords, headers); both are JSON objects.
type Target struct {
	ID       int64
	Kind     string
	Name     string
	Enabled  bool
	Endpoint string
	Events   []string // subset of "alarm", "audit"
	Config   map[string]any
	Secret   map[string]string
	Created  string
	Updated  string
}

// SecretDelete as a secret value in SaveTarget removes that secret (empty keeps it).
const SecretDelete = "\x00delete"

// Wants reports whether the target takes events of this type ("test" always).
func (t Target) Wants(typ string) bool { return typ == "test" || slices.Contains(t.Events, typ) }

// Targets returns all targets sorted by kind and name.
func (s *Store) Targets(ctx context.Context) ([]Target, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, name, enabled, endpoint, events, config, secret, created_ts, updated_ts
		FROM monitor_target ORDER BY kind, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Target
	for rows.Next() {
		t, err := scanTarget(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func scanTarget(rows *sql.Rows) (Target, error) {
	var t Target
	var events, cfg, sec string
	if err := rows.Scan(&t.ID, &t.Kind, &t.Name, &t.Enabled, &t.Endpoint, &events, &cfg, &sec, &t.Created, &t.Updated); err != nil {
		return t, err
	}
	t.Events = eventList(events)
	_ = json.Unmarshal([]byte(cfg), &t.Config)
	_ = json.Unmarshal([]byte(sec), &t.Secret)
	t.initMaps()
	return t, nil
}

// eventList splits the stored event types ("alarm,audit"); nil when there are none.
func eventList(s string) []string {
	var out []string
	for _, e := range strings.Split(s, ",") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// initMaps gives a target without config or secrets empty maps, so they are stored and shown as {}.
func (t *Target) initMaps() {
	if t.Config == nil {
		t.Config = map[string]any{}
	}
	if t.Secret == nil {
		t.Secret = map[string]string{}
	}
}

// Target returns one target.
func (s *Store) Target(ctx context.Context, id int64) (Target, error) {
	all, err := s.Targets(ctx)
	if err != nil {
		return Target{}, err
	}
	for _, t := range all {
		if t.ID == id {
			return t, nil
		}
	}
	return Target{}, fmt.Errorf("%w: target %d", ErrNotFound, id)
}

// auditTarget is what the audit log sees of a target: never the secret.
func auditTarget(t Target) string {
	b, _ := json.Marshal(map[string]any{"kind": t.Kind, "name": t.Name, "endpoint": t.Endpoint, "enabled": t.Enabled,
		"events": t.Events, "config": t.Config})
	return string(b)
}

// SaveTarget creates (ID 0) or updates a target. Secret values that are empty keep the stored value, so the
// UI never has to show them.
func (s *Store) SaveTarget(ctx context.Context, user string, t Target) (int64, error) {
	t.Name, t.Endpoint = strings.TrimSpace(t.Name), strings.TrimSpace(t.Endpoint)
	if err := checkTarget(t); err != nil {
		return 0, err
	}
	before, err := s.keepStoredSecrets(ctx, &t)
	if err != nil {
		return 0, err
	}
	t.initMaps()
	id, err := s.writeTarget(ctx, user, t, before)
	if isUniqueViolation(err) {
		return 0, fmt.Errorf("%w: target %q", ErrExists, t.Name)
	}
	if err != nil {
		return 0, err
	}
	return id, nil
}

// keepStoredSecrets merges the secrets of an existing target (ID != 0) into t and returns the stored target;
// for a new target it returns the zero Target.
func (s *Store) keepStoredSecrets(ctx context.Context, t *Target) (Target, error) {
	if t.ID == 0 {
		return Target{}, nil
	}
	before, err := s.Target(ctx, t.ID)
	if err != nil {
		return Target{}, err
	}
	t.Secret = mergeSecrets(before.Secret, t.Secret)
	return before, nil
}

// writeTarget updates or inserts the row of t with its audit entry and returns the target's ID.
func (s *Store) writeTarget(ctx context.Context, user string, t, before Target) (int64, error) {
	row := targetRowOf(t, s.ts())
	id := t.ID
	err := s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		e := AuditEntry{User: user, Action: "monitor.target.update", Object: t.Name, Before: auditTarget(before), After: auditTarget(t)}
		if t.ID != 0 {
			return e, row.update(ctx, tx, t.ID)
		}
		e.Action, e.Before = "monitor.target.create", ""
		var err error
		id, err = row.insert(ctx, tx)
		return e, err
	})
	return id, err
}

// checkTarget refuses an unknown kind, a missing or overlong name or endpoint and unknown event types.
func checkTarget(t Target) error {
	switch {
	case !slices.Contains([]string{TargetSyslog, TargetOTLP, TargetSNMPTrap}, t.Kind):
		return fmt.Errorf("%w: kind %q", ErrInvalid, t.Kind)
	case !filledUpTo(t.Name, 60):
		return fmt.Errorf("%w: name", ErrInvalid)
	case !filledUpTo(t.Endpoint, 300):
		return fmt.Errorf("%w: endpoint", ErrInvalid)
	}
	return checkTargetEvents(t.Events)
}

// filledUpTo reports whether s is not empty and at most limit bytes long.
func filledUpTo(s string, limit int) bool { return s != "" && len(s) <= limit }

func checkTargetEvents(events []string) error {
	for _, e := range events {
		if e != "alarm" && e != "audit" {
			return fmt.Errorf("%w: event %q", ErrInvalid, e)
		}
	}
	return nil
}

// targetRow is a target as the monitor_target table stores it.
type targetRow struct {
	t                   Target
	events, cfg, secret string
	now                 string
}

func targetRowOf(t Target, now string) targetRow {
	cfg, _ := json.Marshal(t.Config)
	sec, _ := json.Marshal(t.Secret)
	return targetRow{t: t, events: strings.Join(t.Events, ","), cfg: string(cfg), secret: string(sec), now: now}
}

func (r targetRow) update(ctx context.Context, tx *sql.Tx, id int64) error {
	t := r.t
	_, err := tx.ExecContext(ctx, `UPDATE monitor_target SET kind=?, name=?, enabled=?, endpoint=?, events=?, config=?, secret=?, updated_ts=? WHERE id=?`,
		t.Kind, t.Name, t.Enabled, t.Endpoint, r.events, r.cfg, r.secret, r.now, id)
	return err
}

// insert adds the target as a new row, unless MaxTargets targets exist already.
func (r targetRow) insert(ctx context.Context, tx *sql.Tx) (int64, error) {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM monitor_target`).Scan(&n); err != nil {
		return 0, err
	}
	if n >= MaxTargets {
		return 0, ErrTooManyTargets
	}
	t := r.t
	res, err := tx.ExecContext(ctx, `INSERT INTO monitor_target(kind, name, enabled, endpoint, events, config, secret, created_ts, updated_ts)
			VALUES (?,?,?,?,?,?,?,?,?)`, t.Kind, t.Name, t.Enabled, t.Endpoint, r.events, r.cfg, r.secret, r.now, r.now)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// mergeSecrets applies the secrets of a save to the stored ones: an empty value keeps the stored secret,
// SecretDelete removes it, anything else replaces it.
func mergeSecrets(stored, in map[string]string) map[string]string {
	merged := map[string]string{}
	for k, v := range stored {
		merged[k] = v
	}
	for k, v := range in {
		switch v {
		case "":
		case SecretDelete:
			delete(merged, k)
		default:
			merged[k] = v
		}
	}
	return merged
}

// DeleteTarget removes a target.
func (s *Store) DeleteTarget(ctx context.Context, user string, id int64) error {
	t, err := s.Target(ctx, id)
	if err != nil {
		return err
	}
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		_, err := tx.ExecContext(ctx, `DELETE FROM monitor_target WHERE id = ?`, id)
		return AuditEntry{User: user, Action: "monitor.target.delete", Object: t.Name, Before: auditTarget(t)}, err
	})
}

// ---- alarm instances reported as open ----

// AlarmKey identifies one alarm instance.
type AlarmKey struct{ Kind, Key string }

// OpenAlarms returns the alarm instances reported as open.
func (s *Store) OpenAlarms(ctx context.Context) (map[AlarmKey]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind, key, opened_ts FROM monitor_alarm_state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[AlarmKey]string{}
	for rows.Next() {
		var k AlarmKey
		var ts string
		if err := rows.Scan(&k.Kind, &k.Key, &ts); err != nil {
			return nil, err
		}
		out[k] = ts
	}
	return out, rows.Err()
}

// SetAlarmOpen records (open) or forgets (!open) an alarm instance.
func (s *Store) SetAlarmOpen(ctx context.Context, k AlarmKey, open bool) error {
	if open {
		_, err := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO monitor_alarm_state(kind, key, opened_ts) VALUES (?,?,?)`, k.Kind, k.Key, s.ts())
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM monitor_alarm_state WHERE kind = ? AND key = ?`, k.Kind, k.Key)
	return err
}
