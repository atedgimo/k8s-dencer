package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/store"
)

// Per-node utilisation over time. See store.NodeSample for why this is
// separate from the fleet timeline: "how big was the cluster" and "is this
// node stably idle" are different questions and only one of them can be
// answered from a total.

// SaveNodeSamples writes one point per node in a single transaction.
//
// A thousand nodes every thirty seconds is a thousand statements, and doing
// that outside a transaction turns a planning cycle into a thousand fsyncs.
func (s *Store) SaveNodeSamples(ctx context.Context, samples []store.NodeSample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO node_samples (node, taken_at, cpu_used_milli, cpu_alloc_milli)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (node, taken_at) DO NOTHING`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()

	for _, n := range samples {
		if _, err := stmt.ExecContext(ctx, n.Node,
			n.TakenAt.UTC().Format(time.RFC3339Nano), n.CPUUsedMilli, n.CPUAllocMilli); err != nil {
			return fmt.Errorf("save node sample for %s: %w", n.Node, err)
		}
	}
	return tx.Commit()
}

// NodeStabilitySince summarises each node's utilisation over a window.
//
// The median is computed in SQL rather than in Go because the alternative is
// shipping every point for every node to compute one number per node. On a
// large fleet that is the difference between a query and an outage.
//
// Rows with no allocatable are skipped rather than divided by: a node whose
// capacity was not recorded produces no percentage, which is the honest
// answer, instead of an infinity.
func (s *Store) NodeStabilitySince(ctx context.Context, since time.Time) ([]store.NodeStability, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH pct AS (
			SELECT node, taken_at,
			       (cpu_used_milli * 100.0) / cpu_alloc_milli AS p
			FROM node_samples
			WHERE taken_at >= ? AND cpu_alloc_milli > 0
		),
		ranked AS (
			SELECT node, p,
			       ROW_NUMBER()  OVER (PARTITION BY node ORDER BY p) AS rn,
			       COUNT(*)      OVER (PARTITION BY node)            AS n,
			       MAX(p)        OVER (PARTITION BY node)            AS peak,
			       MIN(taken_at) OVER (PARTITION BY node)            AS oldest
			FROM pct
		)
		SELECT node, n, oldest, peak, p AS median
		FROM ranked
		WHERE rn = (n / 2) + 1
		ORDER BY node`, since.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("summarise node stability: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := []store.NodeStability{}
	for rows.Next() {
		var (
			n            store.NodeStability
			oldest       string
			peak, median float64
		)
		if err := rows.Scan(&n.Node, &n.Samples, &oldest, &peak, &median); err != nil {
			return nil, err
		}
		n.Since = parseTime(oldest)
		n.PeakPct = int(peak + 0.5)
		n.MedianPct = int(median + 0.5)
		out = append(out, n)
	}
	return out, rows.Err()
}

// PruneNodeSamples removes per-node points older than before.
func (s *Store) PruneNodeSamples(ctx context.Context, before time.Time) (int, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM node_samples WHERE taken_at < ?`,
		before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("prune node samples: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
