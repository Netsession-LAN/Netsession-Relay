package store

// The edges of a flow profile: from which group to which group packets go.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/Netsession-LAN/Netsession-Relay/internal/compile"
)

// querier is what *sql.DB and *sql.Tx share for reading.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func profileEdges(ctx context.Context, q querier, profile string) ([]compile.Edge, error) {
	rows, err := q.QueryContext(ctx, `SELECT src, dst FROM flow_profile_edge WHERE profile = ? ORDER BY src, dst`, profile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []compile.Edge
	for rows.Next() {
		var e compile.Edge
		if err := rows.Scan(&e.Src, &e.Dst); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// insertEdges adds edges to a flow profile; edges it has already are skipped.
func insertEdges(ctx context.Context, tx *sql.Tx, profile string, edges []compile.Edge) error {
	for _, e := range edges {
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO flow_profile_edge(profile, src, dst) VALUES (?,?,?)`, profile, e.Src, e.Dst); err != nil {
			return err
		}
	}
	return nil
}

// SetProfileEdges replaces the edges of a flow profile.
func (s *Store) SetProfileEdges(ctx context.Context, user, name string, edges []compile.Edge) error {
	a, _ := json.Marshal(edges)
	return s.audited(ctx, func(tx *sql.Tx) (AuditEntry, error) {
		e := AuditEntry{User: user, Action: "profile.edges", Object: name, After: string(a)}
		if err := requireProfile(ctx, tx, name); err != nil {
			return e, err
		}
		before, err := profileEdges(ctx, tx, name)
		if err != nil {
			return e, err
		}
		b, _ := json.Marshal(before)
		e.Before = string(b)
		if _, err := tx.ExecContext(ctx, `DELETE FROM flow_profile_edge WHERE profile = ?`, name); err != nil {
			return e, err
		}
		return e, insertEdges(ctx, tx, name, edges)
	})
}

// SetProfileEdge switches one edge of a flow profile on or off.
func (s *Store) SetProfileEdge(ctx context.Context, user, name string, e compile.Edge, on bool) error {
	c, err := s.ExportConfig(ctx)
	if err != nil {
		return err
	}
	for _, p := range c.Profiles {
		if p.Name != name {
			continue
		}
		edges := slices.DeleteFunc(slices.Clone(p.Edges), func(x compile.Edge) bool { return x == e })
		if on {
			edges = append(edges, e)
		}
		return s.SetProfileEdges(ctx, user, name, edges)
	}
	return fmt.Errorf("%w: profile %q", ErrNotFound, name)
}
