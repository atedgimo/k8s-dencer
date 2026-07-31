// Package sqlite implements the plan store on SQLite.
//
// modernc.org/sqlite is used rather than mattn/go-sqlite3 because it is pure
// Go: the components ship on distroless/static with a CGO-free build, and a
// cgo driver would break that.
//
// SQLite is single-writer. The chart enforces the consequences — ui-backend
// pinned to one replica, Recreate rollout strategy, planner co-scheduled onto
// the same node as the ReadWriteOnce volume — and values.schema.json rejects a
// configuration that would violate them.
package sqlite

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/store"
)

// Store is a SQLite-backed plan store.
type Store struct {
	db *sql.DB
}

// Open connects to the database at path, creating it if absent.
func Open(path string) (*Store, error) {
	// WAL keeps the UI's reads from blocking the planner's writes. busy_timeout
	// covers the brief overlap when the planner commits while a read is in
	// flight; without it those reads fail outright rather than waiting.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	// A single writer connection avoids SQLITE_BUSY between our own
	// goroutines; readers are served from the same pool under WAL.
	db.SetMaxOpenConns(1)
	db.SetConnMaxLifetime(0)

	if err := db.PingContext(context.Background()); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

const schemaVersion = 3

// Migrate creates or upgrades the schema.
func (s *Store) Migrate(ctx context.Context) error {
	var current int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}
	if current >= schemaVersion {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if current < 1 {
		if _, err := tx.ExecContext(ctx, schemaV1); err != nil {
			return fmt.Errorf("apply schema v1: %w", err)
		}
	}
	if current < 2 {
		if _, err := tx.ExecContext(ctx, schemaV2); err != nil {
			return fmt.Errorf("apply schema v2: %w", err)
		}
	}
	if current < 3 {
		if _, err := tx.ExecContext(ctx, schemaV3); err != nil {
			return fmt.Errorf("apply schema v3: %w", err)
		}
	}

	// PRAGMA does not accept a bound parameter.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return tx.Commit()
}

const schemaV1 = `
CREATE TABLE IF NOT EXISTS plans (
    id                 TEXT PRIMARY KEY,
    generated_at       TEXT NOT NULL,
    snapshot_taken_at  TEXT NOT NULL,
    status             TEXT NOT NULL,
    strategy           TEXT NOT NULL,
    nodes_before       INTEGER NOT NULL,
    nodes_after        INTEGER NOT NULL,
    snapshot           BLOB NOT NULL,
    analysis           BLOB NOT NULL,
    stored_at          TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS plans_stored_at ON plans (stored_at DESC);

-- Steps are rows rather than a blob on the plan: doc §5 requires them to be
-- independently addressable and independently executable, and Phase 2 writes
-- per-step audit results back here.
CREATE TABLE IF NOT EXISTS plan_steps (
    plan_id                     TEXT NOT NULL REFERENCES plans(id) ON DELETE CASCADE,
    step_id                     TEXT NOT NULL,
    sequence_number             INTEGER NOT NULL,
    target_node                 TEXT,
    moves                       BLOB NOT NULL,
    impact                      TEXT NOT NULL,
    rationale                   TEXT NOT NULL,
    reasons                     BLOB,
    requires_maintenance_window INTEGER NOT NULL DEFAULT 0,
    executed_at                 TEXT,
    executed_by                 TEXT,
    result                      TEXT,
    PRIMARY KEY (plan_id, sequence_number)
);
`

// Phase 2. Execution requests live here rather than in a CRD for the same
// reason plans do (doc §6): they are written continuously, read almost
// entirely by the UI, and nothing external "desires" a particular run.
//
// The runs table doubles as the work queue between ui-backend and the
// executor. That keeps the executor free of any inbound network surface —
// the only workload holding pods/eviction cannot be reached from the network
// at all — and makes a run survive an executor restart.
const schemaV2 = `
CREATE TABLE IF NOT EXISTS runs (
    id            TEXT PRIMARY KEY,
    -- No ON DELETE CASCADE, deliberately: see Prune. An audit record that
    -- vanishes with its plan is not an audit record.
    plan_id       TEXT NOT NULL,
    steps         BLOB NOT NULL,
    dry_run       INTEGER NOT NULL DEFAULT 0,
    status        TEXT NOT NULL,
    actor         TEXT NOT NULL,
    actor_groups  BLOB,
    requested_at  TEXT NOT NULL,
    started_at    TEXT,
    finished_at   TEXT,
    worker        TEXT,
    summary       TEXT
);

CREATE INDEX IF NOT EXISTS runs_status ON runs (status, requested_at);
CREATE INDEX IF NOT EXISTS runs_plan ON runs (plan_id, requested_at DESC);

CREATE TABLE IF NOT EXISTS run_events (
    run_id    TEXT NOT NULL REFERENCES runs(id) ON DELETE CASCADE,
    sequence  INTEGER NOT NULL,
    at        TEXT NOT NULL,
    level     TEXT NOT NULL,
    step      INTEGER NOT NULL DEFAULT 0,
    node      TEXT,
    pod       TEXT,
    action    TEXT NOT NULL,
    rule      TEXT,
    message   TEXT NOT NULL,
    PRIMARY KEY (run_id, sequence)
);
`

// Phase 4. Closing the reclamation loop: what happened to a node after it was
// drained.
//
// No foreign key to runs, and no cascade. A reclamation outlives the run that
// caused it — the interesting records are precisely the ones still pending
// weeks later, and Prune must not silently delete the evidence that a node was
// drained and never removed.
const schemaV3 = `
CREATE TABLE IF NOT EXISTS reclamations (
    node         TEXT NOT NULL,
    drained_at   TEXT NOT NULL,
    run_id       TEXT,
    plan_id      TEXT,
    step         INTEGER,
    -- NULL while the node is still drained and still present.
    resolved_at  TEXT,
    outcome      TEXT,
    -- Keyed on the attempt, not the node: the same node can be drained,
    -- uncordoned and drained again, and each attempt is its own observation.
    PRIMARY KEY (node, drained_at)
);

CREATE INDEX IF NOT EXISTS reclamations_pending ON reclamations (resolved_at);
CREATE INDEX IF NOT EXISTS reclamations_recent ON reclamations (drained_at DESC);
`

// Save persists a record unless it duplicates the latest plan.
func (s *Store) Save(ctx context.Context, rec store.Record) (bool, error) {
	if rec.Plan == nil {
		return false, errors.New("nil plan")
	}

	// Defaulted before the dedup branch below reads it: callers routinely
	// leave StoredAt zero and let the store stamp it.
	storedAt := rec.StoredAt
	if storedAt.IsZero() {
		storedAt = time.Now().UTC()
	}

	var latestID string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM plans ORDER BY stored_at DESC, rowid DESC LIMIT 1`).Scan(&latestID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read latest plan: %w", err)
	}
	if latestID == rec.Plan.ID {
		// Same content hash as the last write: the cluster has not changed in
		// any way that alters the plan.
		//
		// stored_at is still touched, because it means "last confirmed against
		// the cluster" and that is what a reader needs. Leaving it alone made
		// a plan the planner had just re-verified read as nineteen hours old,
		// so the UI's staleness warning fired on the healthiest possible
		// state. An unchanged plan is the strongest evidence it is current.
		if _, err := s.db.ExecContext(ctx,
			`UPDATE plans SET stored_at = ? WHERE id = ?`,
			storedAt.UTC().Format(time.RFC3339Nano), rec.Plan.ID); err != nil {
			return false, fmt.Errorf("touch plan: %w", err)
		}
		return false, nil
	}

	snapshotJSON, err := encodeBlob(rec.Snapshot)
	if err != nil {
		return false, fmt.Errorf("encode snapshot: %w", err)
	}
	analysisJSON, err := encodeBlob(rec.Analysis)
	if err != nil {
		return false, fmt.Errorf("encode analysis: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	// A plan ID is a content hash, so an existing row with this ID describes
	// the same plan; replace it so the newest occurrence carries the current
	// timestamp rather than resurfacing a stale one.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO plans (id, generated_at, snapshot_taken_at, status, strategy,
		                   nodes_before, nodes_after, snapshot, analysis, stored_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		    generated_at = excluded.generated_at,
		    stored_at    = excluded.stored_at,
		    status       = excluded.status,
		    snapshot     = excluded.snapshot,
		    analysis     = excluded.analysis`,
		rec.Plan.ID,
		rec.Plan.GeneratedAt.UTC().Format(time.RFC3339Nano),
		rec.Plan.SnapshotTakenAt.UTC().Format(time.RFC3339Nano),
		string(rec.Plan.Status),
		rec.Strategy,
		rec.Plan.NodesBefore,
		rec.Plan.NodesAfter,
		snapshotJSON,
		analysisJSON,
		storedAt.Format(time.RFC3339Nano),
	); err != nil {
		return false, fmt.Errorf("insert plan: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM plan_steps WHERE plan_id = ?`, rec.Plan.ID); err != nil {
		return false, fmt.Errorf("clear steps: %w", err)
	}

	for _, step := range rec.Plan.Steps {
		movesJSON, err := json.Marshal(step.Moves)
		if err != nil {
			return false, fmt.Errorf("encode moves: %w", err)
		}
		reasonsJSON, err := json.Marshal(step.Reasons)
		if err != nil {
			return false, fmt.Errorf("encode reasons: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO plan_steps (plan_id, step_id, sequence_number, target_node, moves,
			                        impact, rationale, reasons, requires_maintenance_window)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			rec.Plan.ID, step.ID, step.SequenceNumber, step.TargetNode, movesJSON,
			string(step.Impact), step.Rationale, reasonsJSON,
			boolToInt(step.RequiresMaintenanceWindow()),
		); err != nil {
			return false, fmt.Errorf("insert step %d: %w", step.SequenceNumber, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// Latest returns the most recently stored record.
func (s *Store) Latest(ctx context.Context) (store.Record, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM plans ORDER BY stored_at DESC, rowid DESC LIMIT 1`).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Record{}, store.ErrNotFound
	}
	if err != nil {
		return store.Record{}, err
	}
	return s.ByID(ctx, id)
}

// ByID returns a specific record.
func (s *Store) ByID(ctx context.Context, id string) (store.Record, error) {
	var (
		rec                          store.Record
		generatedAt, snapshotTakenAt string
		storedAt, status             string
		snapshotJSON, analysisJSON   []byte
		nodesBefore, nodesAfter      int
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT generated_at, snapshot_taken_at, status, strategy,
		       nodes_before, nodes_after, snapshot, analysis, stored_at
		FROM plans WHERE id = ?`, id).
		Scan(&generatedAt, &snapshotTakenAt, &status, &rec.Strategy,
			&nodesBefore, &nodesAfter, &snapshotJSON, &analysisJSON, &storedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Record{}, store.ErrNotFound
	}
	if err != nil {
		return store.Record{}, err
	}

	plan := &model.Plan{
		ID:              id,
		GeneratedAt:     parseTime(generatedAt),
		SnapshotTakenAt: parseTime(snapshotTakenAt),
		Status:          model.PlanStatus(status),
		NodesBefore:     nodesBefore,
		NodesAfter:      nodesAfter,
	}
	rec.StoredAt = parseTime(storedAt)

	if err := decodeBlob(snapshotJSON, &rec.Snapshot); err != nil {
		return store.Record{}, fmt.Errorf("decode snapshot: %w", err)
	}
	if err := decodeBlob(analysisJSON, &rec.Analysis); err != nil {
		return store.Record{}, fmt.Errorf("decode analysis: %w", err)
	}

	steps, err := s.stepsFor(ctx, id)
	if err != nil {
		return store.Record{}, err
	}
	plan.Steps = steps
	rec.Plan = plan
	return rec, nil
}

func (s *Store) stepsFor(ctx context.Context, planID string) ([]model.PlanStep, error) {
	// Non-nil from the outset. A nil slice marshals to JSON `null`, and a
	// client that reasonably does steps.filter(...) then crashes on a plan
	// with nothing to do — which is a perfectly ordinary state for an already
	// consolidated cluster.
	steps := []model.PlanStep{}

	rows, err := s.db.QueryContext(ctx, `
		SELECT step_id, sequence_number, target_node, moves, impact, rationale, reasons,
		       executed_at, executed_by, result
		FROM plan_steps WHERE plan_id = ? ORDER BY sequence_number`, planID)
	if err != nil {
		return nil, fmt.Errorf("read steps: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var (
			step                   model.PlanStep
			targetNode             sql.NullString
			movesJSON, reasonsJSON []byte
			executedAt, executedBy sql.NullString
			result                 sql.NullString
		)
		if err := rows.Scan(&step.ID, &step.SequenceNumber, &targetNode, &movesJSON,
			&step.Impact, &step.Rationale, &reasonsJSON,
			&executedAt, &executedBy, &result); err != nil {
			return nil, err
		}
		step.TargetNode = targetNode.String
		step.ExecutedBy = executedBy.String
		step.Result = result.String
		if executedAt.Valid && executedAt.String != "" {
			t := parseTime(executedAt.String)
			step.ExecutedAt = &t
		}
		if err := json.Unmarshal(movesJSON, &step.Moves); err != nil {
			return nil, fmt.Errorf("decode moves for step %d: %w", step.SequenceNumber, err)
		}
		if len(reasonsJSON) > 0 {
			if err := json.Unmarshal(reasonsJSON, &step.Reasons); err != nil {
				return nil, fmt.Errorf("decode reasons for step %d: %w", step.SequenceNumber, err)
			}
		}
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

// List returns summaries newest first.
func (s *Store) List(ctx context.Context, limit int) ([]store.Summary, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.id, p.generated_at, p.snapshot_taken_at, p.status, p.strategy,
		       p.nodes_before, p.nodes_after, p.stored_at,
		       (SELECT COUNT(*) FROM plan_steps s WHERE s.plan_id = p.id),
		       (SELECT COUNT(*) FROM plan_steps s WHERE s.plan_id = p.id AND s.impact = 'Green'),
		       (SELECT COUNT(*) FROM plan_steps s WHERE s.plan_id = p.id AND s.impact = 'Yellow'),
		       (SELECT COUNT(*) FROM plan_steps s WHERE s.plan_id = p.id AND s.impact = 'Red')
		FROM plans p ORDER BY p.stored_at DESC, p.rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.Summary
	for rows.Next() {
		var (
			sum                          store.Summary
			generatedAt, snapshotTakenAt string
			storedAt, status             string
			green, yellow, red           int
		)
		if err := rows.Scan(&sum.ID, &generatedAt, &snapshotTakenAt, &status, &sum.Strategy,
			&sum.NodesBefore, &sum.NodesAfter, &storedAt,
			&sum.Steps, &green, &yellow, &red); err != nil {
			return nil, err
		}
		sum.GeneratedAt = parseTime(generatedAt)
		sum.SnapshotTakenAt = parseTime(snapshotTakenAt)
		sum.StoredAt = parseTime(storedAt)
		sum.Status = model.PlanStatus(status)
		sum.Ratings = map[string]int{"Green": green, "Yellow": yellow, "Red": red}
		out = append(out, sum)
	}
	return out, rows.Err()
}

// Prune keeps the newest records and deletes older ones.
func (s *Store) Prune(ctx context.Context, keep int) (int, error) {
	if keep <= 0 {
		return 0, nil
	}
	// A plan that some run executed is never pruned, however old it is.
	// Doc §9 requires the audit log to be tied to the plan version and the
	// step IDs that authorized each action — so deleting the plan would leave
	// the audit trail pointing at nothing, which is worse than keeping a few
	// extra rows on the volume.
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM plans WHERE id NOT IN (
			SELECT id FROM plans ORDER BY stored_at DESC, rowid DESC LIMIT ?
		)
		AND id NOT IN (SELECT DISTINCT plan_id FROM runs)`, keep)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func parseTime(s string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// compile-time check
var _ store.Store = (*Store)(nil)

// Snapshot and analysis blobs are gzipped.
//
// They are the bulk of a plan row — a 5,000-pod snapshot is 1.6 MB of JSON,
// written on every plan change and retained. Pod specs compress about 13x
// because the field names repeat on every object, so this is close to free
// space back for a little CPU on a path that runs at most once per resync.
//
// BestSpeed rather than BestCompression: the difference in ratio is small and
// the planner should not spend its cycle here.
func encodeBlob(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(raw); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeBlob reads a blob written by encodeBlob, or a plain JSON one.
//
// Rows written before compression are still readable: gzip has a two-byte
// magic number, so the format is self-describing and no migration is needed.
// A schema bump would have meant rewriting every historical row, and plan
// history is an audit trail worth not rewriting.
func decodeBlob(data []byte, into any) error {
	if len(data) >= 2 && data[0] == 0x1f && data[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return err
		}
		defer func() { _ = zr.Close() }()
		return json.NewDecoder(zr).Decode(into)
	}
	return json.Unmarshal(data, into)
}

// ExecForTest runs a statement directly. Test-only: it exists so a test can
// forge a pre-compression row and prove it is still readable.
func (s *Store) ExecForTest(ctx context.Context, query string, args ...any) error {
	_, err := s.db.ExecContext(ctx, query, args...)
	return err
}

// QueryRowForTest reads one row directly. Test-only.
func (s *Store) QueryRowForTest(ctx context.Context, query string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, query, args...)
}
