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
