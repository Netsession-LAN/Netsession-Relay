package store

import (
	"context"
	"database/sql"
	"fmt"
	"net/netip"
	"strings"
)

// ServiceHost is a known host that sends discovery packets without being a player (game server scanner,
// monitoring).
type ServiceHost struct {
	IP, Name, Note, User, TS string
}

// Services returns all known service hosts, sorted by IP.
func (s *Store) Services(ctx context.Context) ([]ServiceHost, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT ip, name, note, user, ts FROM service_host ORDER BY ip`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceHost
	for rows.Next() {
		var h ServiceHost
		if err := rows.Scan(&h.IP, &h.Name, &h.Note, &h.User, &h.TS); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// SetService adds or renames a service host.
func (s *Store) SetService(ctx context.Context, user, ip, name, note string) error {
	a, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil || !a.Is4() {
		return fmt.Errorf("%w: address %q", ErrInvalid, ip)
	}
	name, note = strings.TrimSpace(name), strings.TrimSpace(note)
	if name == "" || len(name) > 60 {
		return fmt.Errorf("%w: name", ErrInvalid)
	}
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		_, err := tx.ExecContext(ctx, `INSERT INTO service_host(ip, name, note, user, ts) VALUES (?,?,?,?,?)
			ON CONFLICT(ip) DO UPDATE SET name = excluded.name, note = excluded.note, user = excluded.user, ts = excluded.ts`,
			a.String(), name, note, user, s.ts())
		return AuditEntry{User: user, Action: "service.set", Object: a.String(), After: name}, err
	})
}

// DeleteService removes a service host.
func (s *Store) DeleteService(ctx context.Context, user, ip string) error {
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		e := AuditEntry{User: user, Action: "service.delete", Object: ip}
		res, err := tx.ExecContext(ctx, `DELETE FROM service_host WHERE ip = ?`, ip)
		if err != nil {
			return e, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return e, fmt.Errorf("%w: service %s", ErrNotFound, ip)
		}
		return e, nil
	})
}
