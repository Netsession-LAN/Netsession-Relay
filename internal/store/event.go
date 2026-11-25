package store

// The active event and the state of its games: switched on, flow profile, source filter.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
)

func (s *Store) activeEventID(ctx context.Context) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM event WHERE is_active = 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%w: no active event (run `nsrelay admin init`)", ErrNotFound)
	}
	return id, err
}

// ActiveEvent returns the name of the active event.
func (s *Store) ActiveEvent(ctx context.Context) (string, error) {
	var name string
	err := s.db.QueryRowContext(ctx, `SELECT name FROM event WHERE is_active = 1`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("%w: no active event", ErrNotFound)
	}
	return name, err
}

// SetGameEnabled switches a game on or off in the active event.
func (s *Store) SetGameEnabled(ctx context.Context, user, gameID string, on bool) error {
	ev, err := s.activeEventID(ctx)
	if err != nil {
		return err
	}
	action := "game.disable"
	if on {
		action = "game.enable"
	}
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		e := AuditEntry{User: user, Action: action, Object: gameID}
		if err := requireGame(ctx, tx, gameID); err != nil {
			return e, err
		}
		var err error
		if on {
			_, err = tx.ExecContext(ctx, `INSERT INTO event_game(event_id, game_id) VALUES (?, ?)
				ON CONFLICT(event_id, game_id) DO UPDATE SET enabled = 1`, ev, gameID)
		} else { // keep the row: profile, overrides and source filter are back when the game is switched on again
			_, err = tx.ExecContext(ctx, `UPDATE event_game SET enabled = 0 WHERE event_id = ? AND game_id = ?`, ev, gameID)
		}
		return e, err
	})
}

// SetGameProfile sets the flow profile of a switched-on game in the active event ("" = default profile).
func (s *Store) SetGameProfile(ctx context.Context, user, gameID, profile string) error {
	ev, err := s.activeEventID(ctx)
	if err != nil {
		return err
	}
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		e := AuditEntry{User: user, Action: "game.profile", Object: gameID, After: profile}
		if profile != "" {
			if err := requireProfile(ctx, tx, profile); err != nil {
				return e, err
			}
		}
		return e, setEventGameProfile(ctx, tx, ev, gameID, profile)
	})
}

// requireGame returns ErrNotFound when the game table has no game with this ID.
func requireGame(ctx context.Context, tx *sql.Tx, gameID string) error {
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM game WHERE id = ?`, gameID).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("%w: game %q", ErrNotFound, gameID)
	}
	return nil
}

// setEventGameProfile sets the flow profile of a game that is switched on in event ev.
func setEventGameProfile(ctx context.Context, tx *sql.Tx, ev int64, gameID, profile string) error {
	res, err := tx.ExecContext(ctx, `UPDATE event_game SET flow_profile = ? WHERE event_id = ? AND game_id = ? AND enabled = 1`, profile, ev, gameID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: game %q is not switched on", ErrNotFound, gameID)
	}
	return nil
}

// sourceList splits the stored source filter (nil when empty, so exports omit it).
func sourceList(s string) []string {
	if f := strings.Fields(s); len(f) > 0 {
		return f
	}
	return nil
}

// SetGameSources sets the source filter of a switched-on game in the active event (empty = every sender).
func (s *Store) SetGameSources(ctx context.Context, user, gameID string, sources []netip.Prefix) error {
	ev, err := s.activeEventID(ctx)
	if err != nil {
		return err
	}
	sources = slices.Clone(sources)
	compile.SortSources(sources)
	val := strings.Join(compile.FormatSources(slices.Compact(sources)), " ")
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		var old string
		err := tx.QueryRowContext(ctx, `SELECT sources FROM event_game WHERE event_id = ? AND game_id = ? AND enabled = 1`, ev, gameID).Scan(&old)
		if errors.Is(err, sql.ErrNoRows) {
			return AuditEntry{}, fmt.Errorf("%w: game %q is not switched on", ErrNotFound, gameID)
		}
		if err != nil || val == old {
			return AuditEntry{}, err // unchanged: nothing to store, nothing to record
		}
		_, err = tx.ExecContext(ctx, `UPDATE event_game SET sources = ? WHERE event_id = ? AND game_id = ?`, val, ev, gameID)
		return AuditEntry{User: user, Action: "game.sources", Object: gameID, Before: old, After: val}, err
	})
}
