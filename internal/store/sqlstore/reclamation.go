package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/store"
)

// Reclamation tracking. See store.Reclamation for why this exists: the product
// reported "reclaimable" as though it were "reclaimed", and nothing checked.

// RecordDrain notes that a node was drained and now awaits reclamation.
func (s *Store) RecordDrain(ctx context.Context, r store.Reclamation) error {
	_, err := s.exec(ctx, `
		INSERT INTO reclamations (node, drained_at, run_id, plan_id, step, cpu_milli, mem_bytes,
		                          external, instance_type, capacity_type)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (node, drained_at) DO NOTHING`,
		r.Node, r.DrainedAt.UTC().Format(time.RFC3339Nano), r.RunID, r.PlanID, r.Step,
		r.CPUMilli, r.MemBytes, boolToInt(r.External), r.InstanceType, r.CapacityType)
	if err != nil {
		return fmt.Errorf("record drain of %s: %w", r.Node, err)
	}
	return nil
}

// PendingReclamations returns every node still awaiting an outcome.
func (s *Store) PendingReclamations(ctx context.Context) ([]store.Reclamation, error) {
	return s.queryReclamations(ctx, `
		SELECT node, drained_at, run_id, plan_id, step, resolved_at, outcome, cpu_milli, mem_bytes, external, instance_type, capacity_type
		FROM reclamations WHERE resolved_at IS NULL
		ORDER BY drained_at`)
}

// Reclamations returns recent records, newest first.
func (s *Store) Reclamations(ctx context.Context, limit int) ([]store.Reclamation, error) {
	if limit <= 0 {
		limit = 50
	}
	return s.queryReclamations(ctx, `
		SELECT node, drained_at, run_id, plan_id, step, resolved_at, outcome, cpu_milli, mem_bytes, external, instance_type, capacity_type
		FROM reclamations ORDER BY drained_at DESC LIMIT ?`, limit)
}

// ResolveReclamation closes out one pending record.
//
// Scoped to pending rows: an outcome is observed once and never revised. A node
// that is deleted, recreated with the same name and drained again gets a new
// row by virtue of a different drained_at, so re-resolving an old one would be
// attributing a fresh event to a stale record.
func (s *Store) ResolveReclamation(ctx context.Context, node string, drainedAt time.Time,
	outcome store.ReclamationOutcome, at time.Time) error {
	res, err := s.exec(ctx, `
		UPDATE reclamations SET resolved_at = ?, outcome = ?
		WHERE node = ? AND drained_at = ? AND resolved_at IS NULL`,
		at.UTC().Format(time.RFC3339Nano), string(outcome),
		node, drainedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("resolve reclamation for %s: %w", node, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Not an error worth failing a planning cycle over — two observers, or
		// a restart replaying a cycle, would both land here — but the caller
		// should not report a transition it did not cause.
		return store.ErrNotFound
	}
	return nil
}

// ReclamationSummary aggregates records since the given time.
func (s *Store) ReclamationSummary(ctx context.Context, since time.Time) (store.ReclamationStats, error) {
	var out store.ReclamationStats

	// Pending is deliberately not filtered by `since`: a node drained two
	// months ago and still sitting there is the single most interesting row in
	// this table, and windowing it out would hide exactly the failure this
	// mechanism exists to surface.
	if err := s.queryRow(ctx,
		`SELECT COUNT(*) FROM reclamations WHERE resolved_at IS NULL`).Scan(&out.Awaiting); err != nil {
		return out, fmt.Errorf("count pending reclamations: %w", err)
	}

	cutoff := since.UTC().Format(time.RFC3339Nano)
	rows, err := s.query(ctx, `
		SELECT outcome, drained_at, resolved_at, cpu_milli, mem_bytes, external FROM reclamations
		WHERE resolved_at IS NOT NULL AND resolved_at >= ?`, cutoff)
	if err != nil {
		return out, fmt.Errorf("summarise reclamations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var durations []time.Duration
	for rows.Next() {
		var outcome, drainedAt, resolvedAt string
		var cpuMilli, memBytes int64
		var external bool
		if err := rows.Scan(&outcome, &drainedAt, &resolvedAt, &cpuMilli, &memBytes, &external); err != nil {
			return out, err
		}
		if external {
			// Counted, but never as ours: it is not a saving this product
			// produced, and the ledger's whole point is that it does not
			// overstate.
			if store.ReclamationOutcome(outcome) == store.ReclaimedGone {
				out.ExternallyReclaimed++
			}
			continue
		}
		switch store.ReclamationOutcome(outcome) {
		case store.ReclaimedGone:
			out.Reclaimed++
			// The ledger. Rows from before capacity capture carry zeroes;
			// counting them as savings of zero would silently under-report,
			// so they are counted as what they are — uncounted.
			if cpuMilli > 0 || memBytes > 0 {
				out.ReclaimedCPUMilli += cpuMilli
				out.ReclaimedMemBytes += memBytes
			} else {
				out.UncountedNodes++
			}
			// Only genuine reclamations contribute to the timing. A node that
			// was uncordoned tells us how long someone took to change their
			// mind, which is a different question and would skew the answer to
			// "how long does reclamation take".
			// parseTime yields the zero time on anything unparseable, matching
			// the rest of this file. A pair that did not parse would produce a
			// nonsense duration, so skip it rather than poison the median.
			d, r := parseTime(drainedAt), parseTime(resolvedAt)
			if !d.IsZero() && !r.IsZero() {
				durations = append(durations, r.Sub(d))
			}
		case store.ReclaimedReturned:
			out.Returned++
		}
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.MedianTime = median(durations)
	return out, nil
}

func median(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	mid := len(d) / 2
	if len(d)%2 == 1 {
		return d[mid]
	}
	return (d[mid-1] + d[mid]) / 2
}

func (s *Store) queryReclamations(ctx context.Context, query string, args ...any) ([]store.Reclamation, error) {
	rows, err := s.query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	// Non-nil so the API serialises an empty result as [] rather than null —
	// the same trap planResponse already guards against.
	out := []store.Reclamation{}
	for rows.Next() {
		var (
			r                      store.Reclamation
			drainedAt              string
			runID, planID, outcome sql.NullString
			step                   sql.NullInt64
			resolvedAt             sql.NullString
		)
		if err := rows.Scan(&r.Node, &drainedAt, &runID, &planID, &step, &resolvedAt, &outcome,
			&r.CPUMilli, &r.MemBytes, &r.External, &r.InstanceType, &r.CapacityType); err != nil {
			return nil, err
		}
		r.DrainedAt = parseTime(drainedAt)
		r.RunID, r.PlanID = runID.String, planID.String
		r.Step = int(step.Int64)
		if resolvedAt.Valid {
			t := parseTime(resolvedAt.String)
			r.ResolvedAt = &t
			r.Outcome = store.ReclamationOutcome(outcome.String)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
