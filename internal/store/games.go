package store

// The game catalogue: seed sync and the games admins add, edit or remove.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/Netsession-LAN/Netsession-Relay/internal/gamedb"
)

// SeedResult reports what SyncSeed changed.
type SeedResult struct {
	Added, Updated, Pending int
}

// SyncSeed brings the seed database into the store: new games are added, unchanged seed games are updated,
// locally modified games keep their state and get seed_pending set (UI shows "seed differs").
func (s *Store) SyncSeed(ctx context.Context, seed *gamedb.DB) (SeedResult, error) {
	var res SeedResult
	err := s.tx(ctx, func(tx *sql.Tx) error {
		for _, g := range seed.Games {
			if err := syncSeedGame(ctx, tx, g, &res); err != nil {
				return err
			}
		}
		return nil
	})
	return res, err
}

// storedGame is what SyncSeed needs to know of a game in the store.
type storedGame struct {
	found    bool
	origin   string
	seedHash string
	modified bool
}

// followsSeed reports whether the stored game should move to the seed version with hash h: it comes from the
// seed and has another version.
func (st storedGame) followsSeed(h string) bool { return st.origin == "seed" && st.seedHash != h }

func readStoredGame(ctx context.Context, tx *sql.Tx, id string) (storedGame, error) {
	var st storedGame
	err := tx.QueryRowContext(ctx, `SELECT origin, seed_hash, locally_modified FROM game WHERE id = ?`, id).Scan(&st.origin, &st.seedHash, &st.modified)
	if errors.Is(err, sql.ErrNoRows) {
		return st, nil
	}
	st.found = err == nil
	return st, err
}

// syncSeedGame adds the seed game g, updates its unmodified copy, or marks a locally modified one as pending.
func syncSeedGame(ctx context.Context, tx *sql.Tx, g gamedb.Game, res *SeedResult) error {
	h := gamedb.GameHash(g)
	data, _ := json.Marshal(g)
	st, err := readStoredGame(ctx, tx, g.ID)
	switch {
	case err != nil:
		return err
	case !st.found:
		_, err = tx.ExecContext(ctx, `INSERT INTO game(id, name, category, relay_needed, default_enabled, confidence, origin, seed_hash, data)
			VALUES (?,?,?,?,?,?,'seed',?,?)`, g.ID, g.Name, g.Category, g.RelayNeeded, g.DefaultEnabled, g.Confidence, h, string(data))
		res.Added++
	case !st.followsSeed(h):
		return nil
	case st.modified:
		_, err = tx.ExecContext(ctx, `UPDATE game SET seed_pending = ? WHERE id = ?`, h, g.ID)
		res.Pending++
	default:
		_, err = tx.ExecContext(ctx, `UPDATE game SET name=?, category=?, relay_needed=?, default_enabled=?, confidence=?, seed_hash=?, seed_pending='', data=? WHERE id=?`,
			g.Name, g.Category, g.RelayNeeded, g.DefaultEnabled, g.Confidence, h, string(data), g.ID)
		res.Updated++
	}
	return err
}

// GameRow is a game with its store metadata and its state in the active event.
type GameRow struct {
	gamedb.Game
	Origin          string
	LocallyModified bool
	SeedPending     bool
	Enabled         bool     // switched on in the active event
	SrcFilter       []string // source filter in the active event (empty = every sender)
}

// Games returns all games sorted by name.
func (s *Store) Games(ctx context.Context) ([]GameRow, error) {
	ev, _ := s.activeEventID(ctx)
	rows, err := s.db.QueryContext(ctx, `SELECT g.data, g.origin, g.locally_modified, g.seed_pending != '',
		EXISTS(SELECT 1 FROM event_game eg WHERE eg.event_id = ? AND eg.game_id = g.id AND eg.enabled = 1),
		COALESCE((SELECT eg.sources FROM event_game eg WHERE eg.event_id = ? AND eg.game_id = g.id), '') FROM game g`, ev, ev)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GameRow
	for rows.Next() {
		var r GameRow
		var data, sources string
		if err := rows.Scan(&data, &r.Origin, &r.LocallyModified, &r.SeedPending, &r.Enabled, &sources); err != nil {
			return nil, err
		}
		r.SrcFilter = sourceList(sources)
		if err := json.Unmarshal([]byte(data), &r.Game); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, rows.Err()
}

// GameDB returns all stored games as a validated database for the compiler.
func (s *Store) GameDB(ctx context.Context) (*gamedb.DB, error) {
	rows, err := s.Games(ctx)
	if err != nil {
		return nil, err
	}
	list := make([]gamedb.Game, len(rows))
	for i, r := range rows {
		list[i] = r.Game
	}
	return gamedb.New(list)
}

// AddGame stores a new game (origin "custom" or "sniffer"); existing IDs are never overwritten.
func (s *Store) AddGame(ctx context.Context, user, origin string, g gamedb.Game) error {
	if err := gamedb.Validate([]gamedb.Game{g}); err != nil {
		return err
	}
	data, _ := json.Marshal(g)
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		e := AuditEntry{User: user, Action: "game.create", Object: g.ID, After: string(data)}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM game WHERE id = ?`, g.ID).Scan(&n); err != nil {
			return e, err
		}
		if n > 0 {
			return e, fmt.Errorf("%w: game %q", ErrExists, g.ID)
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO game(id, name, category, relay_needed, default_enabled, confidence, origin, data)
			VALUES (?,?,?,?,?,?,?,?)`, g.ID, g.Name, g.Category, g.RelayNeeded, g.DefaultEnabled, g.Confidence, origin, string(data))
		return e, err
	})
}

// Game returns one game with its store metadata.
func (s *Store) Game(ctx context.Context, id string) (GameRow, error) {
	games, err := s.Games(ctx)
	if err != nil {
		return GameRow{}, err
	}
	for _, g := range games {
		if g.ID == id {
			return g, nil
		}
	}
	return GameRow{}, fmt.Errorf("%w: game %q", ErrNotFound, id)
}

// UpdateGame replaces the data of an existing game. A seed game is marked locally modified, so a later seed
// sync does not overwrite it (it gets "seed differs" instead).
func (s *Store) UpdateGame(ctx context.Context, user string, g gamedb.Game) error {
	if err := gamedb.Validate([]gamedb.Game{g}); err != nil {
		return err
	}
	before, err := s.Game(ctx, g.ID)
	if err != nil {
		return err
	}
	data, _ := json.Marshal(g)
	b, _ := json.Marshal(before.Game)
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		_, err := tx.ExecContext(ctx, `UPDATE game SET name=?, category=?, relay_needed=?, default_enabled=?, confidence=?, data=?,
			locally_modified = CASE WHEN origin = 'seed' THEN 1 ELSE locally_modified END WHERE id=?`,
			g.Name, g.Category, g.RelayNeeded, g.DefaultEnabled, g.Confidence, string(data), g.ID)
		return AuditEntry{User: user, Action: "game.update", Object: g.ID, Before: string(b), After: string(data)}, err
	})
}

// ErrSeedGame is returned when a game from the seed database would be deleted (it would come back with the
// next seed sync; switch it off instead).
var ErrSeedGame = errors.New("game comes from the game database")

// DeleteGame removes a game that was created locally (custom, sniffer, import) from all events.
func (s *Store) DeleteGame(ctx context.Context, user, id string) error {
	before, err := s.Game(ctx, id)
	if err != nil {
		return err
	}
	if before.Origin == "seed" {
		return fmt.Errorf("%w: %s", ErrSeedGame, id)
	}
	b, _ := json.Marshal(before.Game)
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		_, err := tx.ExecContext(ctx, `DELETE FROM game WHERE id = ?`, id) // event_game: CASCADE
		return AuditEntry{User: user, Action: "game.delete", Object: id, Before: string(b)}, err
	})
}
