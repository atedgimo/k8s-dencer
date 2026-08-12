package sqlstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/atedgimo/k8s-dencer/internal/store"
)

// Enqueue records an authorized execution request.
func (s *Store) Enqueue(ctx context.Context, run store.Run) (string, error) {
	if run.ID == "" {
		run.ID = newRunID()
	}
	if run.Status == "" {
		run.Status = store.RunPending
	}
	if run.RequestedAt.IsZero() {
		run.RequestedAt = time.Now().UTC()
	}

	steps, err := json.Marshal(run.Steps)
	if err != nil {
		return "", err
	}
	groups, err := json.Marshal(run.ActorGroups)
	if err != nil {
		return "", err
	}
	// nil marshals to the string "null"; a steps run should store SQL NULL so
	// "no envelope" and "an envelope" stay distinguishable in the audit row.
	var envelope []byte
	if run.Envelope != nil {
		if envelope, err = json.Marshal(run.Envelope); err != nil {
			return "", err
		}
	}

	_, err = s.exec(ctx, `
		INSERT INTO runs (id, plan_id, steps, dry_run, status, actor, actor_groups, requested_at, mode, envelope, node, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		        (SELECT COALESCE(MAX(seq), 0) + 1 FROM runs))`,
		run.ID, run.PlanID, steps, boolToInt(run.DryRun), string(run.Status),
		run.Actor, groups, run.RequestedAt.UTC().Format(time.RFC3339Nano),
		run.Mode, envelope, run.Node)
	if err != nil {
		return "", fmt.Errorf("enqueue run: %w", err)
	}
	return run.ID, nil
}

// Claim atomically takes the oldest pending run.
//
// The UPDATE ... WHERE id = (SELECT ... LIMIT 1) form is what makes this safe:
// the select and the write are one statement, so two executors racing cannot
// both win. Doing it as SELECT-then-UPDATE would leave a window in which both
// see the same pending row and both start draining the same node.
func (s *Store) Claim(ctx context.Context, worker string) (store.Run, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)

	var id string
	err := s.queryRow(ctx, `
		UPDATE runs
		SET status = ?, worker = ?, started_at = ?
		WHERE id = (
			SELECT id FROM runs
			WHERE status = ?
			ORDER BY requested_at, seq
			LIMIT 1
		)
		RETURNING id`,
		string(store.RunRunning), worker, now, string(store.RunPending)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Run{}, store.ErrNotFound
	}
	if err != nil {
		return store.Run{}, fmt.Errorf("claim run: %w", err)
	}
	return s.RunByID(ctx, id)
}

// AppendEvent adds one audit entry, assigning its sequence number.
//
// The sequence comes from a subquery rather than the caller so that ordering
// holds even if the executor is restarted mid-run and resumes numbering.
func (s *Store) AppendEvent(ctx context.Context, ev store.RunEvent) error {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	if ev.Level == "" {
		ev.Level = store.EventInfo
	}
	_, err := s.exec(ctx, `
		INSERT INTO run_events (run_id, sequence, at, level, step, node, pod, action, rule, message)
		VALUES (
			?,
			(SELECT COALESCE(MAX(sequence), 0) + 1 FROM run_events WHERE run_id = ?),
			?, ?, ?, ?, ?, ?, ?, ?
		)`,
		ev.RunID, ev.RunID,
		ev.At.UTC().Format(time.RFC3339Nano), string(ev.Level),
		ev.Step, nullable(ev.Node), nullable(ev.Pod), ev.Action, nullable(ev.Rule), ev.Message)
	if err != nil {
		return fmt.Errorf("append run event: %w", err)
	}
	return nil
}

// Finish closes a run.
func (s *Store) Finish(ctx context.Context, runID string, status store.RunStatus, summary string) error {
	if !status.Terminal() {
		return fmt.Errorf("cannot finish run %s with non-terminal status %s", runID, status)
	}
	res, err := s.exec(ctx, `
		UPDATE runs SET status = ?, summary = ?, finished_at = ? WHERE id = ?`,
		string(status), summary, time.Now().UTC().Format(time.RFC3339Nano), runID)
	if err != nil {
		return fmt.Errorf("finish run: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// RunByID returns one run.
func (s *Store) RunByID(ctx context.Context, runID string) (store.Run, error) {
	row := s.queryRow(ctx, `
		SELECT id, plan_id, steps, dry_run, status, actor, actor_groups,
		       requested_at, started_at, finished_at, worker, summary, mode, envelope, node, stop_requested, stop_requested_by
		FROM runs WHERE id = ?`, runID)
	return scanRun(row)
}

// ActiveRun returns the pending or running execution, if any.
func (s *Store) ActiveRun(ctx context.Context) (store.Run, error) {
	row := s.queryRow(ctx, `
		SELECT id, plan_id, steps, dry_run, status, actor, actor_groups,
		       requested_at, started_at, finished_at, worker, summary, mode, envelope, node, stop_requested, stop_requested_by
		FROM runs WHERE status IN (?, ?)
		ORDER BY requested_at LIMIT 1`,
		string(store.RunPending), string(store.RunRunning))
	return scanRun(row)
}

// RecentRuns lists the newest runs regardless of plan, newest first.
func (s *Store) RecentRuns(ctx context.Context, limit int) ([]store.Run, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.query(ctx, `
		SELECT id, plan_id, steps, dry_run, status, actor, actor_groups,
		       requested_at, started_at, finished_at, worker, summary, mode, envelope, node, stop_requested, stop_requested_by
		FROM runs ORDER BY requested_at DESC, seq DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []store.Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// RunsForPlan lists runs against a plan, newest first.
func (s *Store) RunsForPlan(ctx context.Context, planID string, limit int) ([]store.Run, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.query(ctx, `
		SELECT id, plan_id, steps, dry_run, status, actor, actor_groups,
		       requested_at, started_at, finished_at, worker, summary, mode, envelope, node, stop_requested, stop_requested_by
		FROM runs WHERE plan_id = ? ORDER BY requested_at DESC, seq DESC LIMIT ?`,
		planID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.Run
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// Events returns a run's audit trail in order.
func (s *Store) Events(ctx context.Context, runID string) ([]store.RunEvent, error) {
	rows, err := s.query(ctx, `
		SELECT sequence, at, level, step, node, pod, action, rule, message
		FROM run_events WHERE run_id = ? ORDER BY sequence`, runID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.RunEvent
	for rows.Next() {
		ev := store.RunEvent{RunID: runID}
		var at, level string
		var node, pod, rule sql.NullString
		if err := rows.Scan(&ev.Sequence, &at, &level, &ev.Step, &node, &pod,
			&ev.Action, &rule, &ev.Message); err != nil {
			return nil, err
		}
		ev.At = parseTime(at)
		ev.Level = store.EventLevel(level)
		ev.Node, ev.Pod, ev.Rule = node.String, pod.String, rule.String
		out = append(out, ev)
	}
	return out, rows.Err()
}

// scanner covers both *sql.Row and *sql.Rows.
type scanner interface{ Scan(dest ...any) error }

func scanRun(row scanner) (store.Run, error) {
	var (
		run                   store.Run
		steps, groups         []byte
		envelope              []byte
		dryRun                int
		status, requestedAt   string
		startedAt, finishedAt sql.NullString
		worker, summary       sql.NullString
		stopRequested         int
	)
	err := row.Scan(&run.ID, &run.PlanID, &steps, &dryRun, &status, &run.Actor, &groups,
		&requestedAt, &startedAt, &finishedAt, &worker, &summary, &run.Mode, &envelope, &run.Node,
		&stopRequested, &run.StopRequestedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Run{}, store.ErrNotFound
	}
	if err != nil {
		return store.Run{}, err
	}

	if err := json.Unmarshal(steps, &run.Steps); err != nil {
		return store.Run{}, fmt.Errorf("decode run steps: %w", err)
	}
	if len(groups) > 0 {
		_ = json.Unmarshal(groups, &run.ActorGroups)
	}
	if len(envelope) > 0 {
		var env store.Envelope
		if err := json.Unmarshal(envelope, &env); err != nil {
			// An unreadable consent record must fail loudly, not degrade into
			// an unbounded run.
			return store.Run{}, fmt.Errorf("decode run envelope: %w", err)
		}
		run.Envelope = &env
	}
	run.DryRun = dryRun != 0
	run.StopRequested = stopRequested != 0
	run.Status = store.RunStatus(status)
	run.RequestedAt = parseTime(requestedAt)
	run.Worker, run.Summary = worker.String, summary.String
	if startedAt.Valid {
		t := parseTime(startedAt.String)
		run.StartedAt = &t
	}
	if finishedAt.Valid {
		t := parseTime(finishedAt.String)
		run.FinishedAt = &t
	}
	return run, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func newRunID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is not recoverable and not something a run ID
		// should paper over with a predictable fallback.
		panic("crypto/rand unavailable: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// RequestStop asks a run to end at its next safe point.
//
// Scoped to runs that have not finished: asking a completed run to stop is a
// slow click, not an error, and reporting it as one would be pedantry in a
// place where people are already anxious.
//
// A request rather than a kill. The executor honours it at step boundaries,
// which is the only place it honestly can — a pod already evicted cannot be
// un-evicted.
func (s *Store) RequestStop(ctx context.Context, runID, by string) error {
	_, err := s.exec(ctx, `
		UPDATE runs SET stop_requested = 1, stop_requested_by = ?
		WHERE id = ? AND status IN (?, ?)`,
		by, runID, string(store.RunPending), string(store.RunRunning))
	if err != nil {
		return fmt.Errorf("request stop of run %s: %w", runID, err)
	}
	return nil
}
