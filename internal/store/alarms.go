package store

import (
	"context"
)

// Alarm instances reported as open, so a restart repeats no "open" and loses no "clear".

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
