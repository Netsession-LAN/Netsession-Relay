package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// EventLogo is the optional logo of the event, shown next to the product mark. PNG is what the web part
// encoded from the upload (internal/eventlogo), Hash its SHA-256.
type EventLogo struct {
	PNG           []byte
	Hash          string
	Width, Height int
	Updated       string
}

// EventLogo returns the stored logo, ErrNotFound without one.
func (s *Store) EventLogo(ctx context.Context) (EventLogo, error) {
	var l EventLogo
	err := s.DB.QueryRowContext(ctx, `SELECT png, hash, width, height, updated FROM event_logo WHERE id = 1`).
		Scan(&l.PNG, &l.Hash, &l.Width, &l.Height, &l.Updated)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

// SetEventLogo stores or replaces the logo.
func (s *Store) SetEventLogo(ctx context.Context, user string, l EventLogo) error {
	if len(l.PNG) == 0 || l.Hash == "" || l.Width < 1 || l.Height < 1 {
		return fmt.Errorf("%w: empty logo", ErrInvalid)
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO event_logo(id, png, hash, width, height, updated) VALUES (1, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET png = excluded.png, hash = excluded.hash, width = excluded.width, height = excluded.height,
		updated = excluded.updated`, l.PNG, l.Hash, l.Width, l.Height, s.ts())
	if err != nil {
		return err
	}
	return s.Audit(ctx, user, "event.logo.set", fmt.Sprintf("%d × %d px", l.Width, l.Height), "", l.Hash)
}

// DeleteEventLogo removes the logo; ErrNotFound when there was none.
func (s *Store) DeleteEventLogo(ctx context.Context, user string) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM event_logo WHERE id = 1`)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return s.Audit(ctx, user, "event.logo.remove", "", "", "")
}
