package sqlstore

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/store"
)

// How long workloads take to come back. The executor watches this on every
// drain; this is where it stops being forgotten the moment the event scrolls.

// RecordRecovery notes one workload's time to Ready after an eviction.
func (s *Store) RecordRecovery(ctx context.Context, workload string, at time.Time, took time.Duration) error {
	_, err := s.exec(ctx, `
		INSERT INTO recoveries (workload, observed_at, took_seconds)
		VALUES (?, ?, ?)
		ON CONFLICT (workload, observed_at) DO NOTHING`,
		workload, at.UTC().Format(time.RFC3339Nano), int64(took.Seconds()))
	if err != nil {
		return fmt.Errorf("record recovery for %s: %w", workload, err)
	}
	return nil
}

// RecoveryFor returns what past drains say about these workloads.
//
// Workloads with no history are absent from the map rather than present with
// zeroes: never observed and instantaneous are different claims, and only one
// of them should ever reach a screen.
func (s *Store) RecoveryFor(ctx context.Context, workloads []string) (map[string]store.Recovery, error) {
	out := map[string]store.Recovery{}
	if len(workloads) == 0 {
		return out, nil
	}

	holders := strings.TrimSuffix(strings.Repeat("?,", len(workloads)), ",")
	args := make([]any, 0, len(workloads))
	for _, w := range workloads {
		args = append(args, w)
	}

	// Median via window function, the same shape as node stability: the
	// typical case is what to expect and the worst is what to plan for, and
	// a mean would let one pathological restart speak for both.
	rows, err := s.query(ctx, `
		WITH ranked AS (
			SELECT workload, took_seconds,
			       ROW_NUMBER()      OVER (PARTITION BY workload ORDER BY took_seconds) AS rn,
			       COUNT(*)          OVER (PARTITION BY workload)                       AS n,
			       MAX(took_seconds) OVER (PARTITION BY workload)                       AS worst,
			       MAX(observed_at)  OVER (PARTITION BY workload)                       AS last_seen
			FROM recoveries
			WHERE workload IN (`+holders+`)
		)
		SELECT workload, n, took_seconds AS typical, worst, last_seen
		FROM ranked
		WHERE rn = (n / 2) + 1`, args...)
	if err != nil {
		return nil, fmt.Errorf("read recoveries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var r store.Recovery
		var lastSeen string
		if err := rows.Scan(&r.Workload, &r.Observations, &r.TypicalSeconds, &r.WorstSeconds, &lastSeen); err != nil {
			return nil, err
		}
		r.LastSeen = parseTime(lastSeen)
		out[r.Workload] = r
	}
	return out, rows.Err()
}

// PruneRecoveries removes observations older than before.
//
// A year, by default caller. Recovery time is a property of the workload's
// image and probes, which change on a release cadence rather than a daily
// one, so old observations stay useful far longer than a utilisation point.
func (s *Store) PruneRecoveries(ctx context.Context, before time.Time) (int, error) {
	res, err := s.exec(ctx,
		`DELETE FROM recoveries WHERE observed_at < ?`,
		before.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("prune recoveries: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
