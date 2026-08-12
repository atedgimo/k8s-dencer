// Package sqlstore implements the plan store, on SQLite or on Postgres.
//
// One implementation over both, differing only where the servers genuinely
// differ: placeholder spelling (rebind), a handful of DDL types
// (translateDDL), and where the schema version is kept. Every query, every
// migration and every test is shared, because a store is the product's memory
// and two memories that drift are worse than one that is a little more
// general.
//
// Both drivers are pure Go — modernc.org/sqlite rather than mattn/go-sqlite3,
// and pgx's database/sql driver — because the components ship on
// distroless/static with a CGO-free build, and a cgo driver would break that.
//
// SQLite is single-writer, and the chart enforces the consequences: ui-backend
// pinned to one replica, Recreate rollout strategy, planner co-scheduled onto
// the same node as the ReadWriteOnce volume, with values.schema.json rejecting
// a configuration that would violate them. Postgres is the reason those
// constraints can be lifted — it is not a second way to store the same data so
// much as the way to stop pinning the product to one node.
package sqlstore

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/atedgimo/k8s-dencer/internal/model"
	"github.com/atedgimo/k8s-dencer/internal/store"
)

// Store is the plan store, over SQLite or Postgres.
type Store struct {
	db *sql.DB
	d  dialect
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
	return &Store{db: db, d: sqliteDialect}, nil
}

// OpenPostgres connects to the Postgres server named by dsn.
//
// The DSN is passed through to the driver rather than assembled from parts,
// so sslmode, connect_timeout and the rest are the operator's to set and this
// code has no opinion it could get wrong. It carries a password, so it is
// never logged: the errors below name the failure, not the string.
func OpenPostgres(ctx context.Context, dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	// Unlike SQLite there is no single-writer rule to honour, which is the
	// point of supporting it. The pool is left small because this store's
	// traffic is one planner writing and a handful of readers, not because it
	// has to be.
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(time.Hour)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	return &Store{db: db, d: postgresDialect}, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

const schemaVersion = 15

// migrations are the schema versions in order; index i holds the statements
// that take the database from version i to i+1.
//
// A table rather than fourteen near-identical if-blocks, so adding v15 is one
// line and cannot be got subtly wrong — the previous shape had the version
// number written three times per step, which is three chances to typo a
// number that decides whether an operator's database is upgraded or skipped.
func (s *Store) migrations() []string {
	return []string{
		schemaV1, schemaV2, schemaV3, schemaV4, schemaV5, schemaV6, schemaV7,
		schemaV8, schemaV9, schemaV10, schemaV11, schemaV12, schemaV13, schemaV14,
		s.schemaV15(),
	}
}

// schemaV15 is Postgres-only: SQLite's INTEGER was always 64-bit, so there is
// nothing to widen there and an empty step keeps the version numbers aligned
// across both backends rather than letting them drift apart.
func (s *Store) schemaV15() string {
	if s.d == postgresDialect {
		return schemaV15Postgres
	}
	return ""
}

// migrateLockKey is an arbitrary but fixed identifier for the advisory lock
// that serialises migration across processes. "dencer" in ASCII hex; the value
// only has to be stable and unlikely to collide with another application
// sharing the database.
const migrateLockKey int64 = 0x64656e636572

// Migrate creates or upgrades the schema.
//
// Everything happens inside one transaction, and on Postgres that transaction
// opens by taking an advisory lock. The planner and the ui-backend both call
// this at startup and on a fresh install both pods start together, so without
// the lock they race: one creates schema_version while the other is doing the
// same, and the loser dies on a duplicate key in pg_type. It self-heals on
// restart, but a CrashLoopBackOff on first install reads as a broken deploy,
// and later interleavings fail on an unguarded ALTER TABLE instead. Raising
// uiBackend.replicaCount — which the Postgres backend exists to allow — makes
// it likelier, not less.
//
// SQLite needs no lock: one writer connection, serialised by the driver.
func (s *Store) Migrate(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if s.d == postgresDialect {
		// Held until this transaction ends, whether by commit or rollback,
		// so a crashing migrator cannot leave the lock behind.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, migrateLockKey); err != nil {
			return fmt.Errorf("take migration lock: %w", err)
		}
	}

	current, err := s.schemaVersionTx(ctx, tx)
	if err != nil {
		return err
	}
	if current >= schemaVersion {
		return nil
	}

	for i, ddl := range s.migrations() {
		v := i + 1
		if current >= v {
			continue
		}
		// A version may legitimately be a no-op on one backend — v15 widens
		// integers that were never narrow on SQLite — and an empty statement
		// is a syntax error rather than a no-op if handed to a driver.
		if strings.TrimSpace(ddl) != "" {
			if _, err := tx.ExecContext(ctx, translateDDL(s.d, ddl)); err != nil {
				return fmt.Errorf("apply schema v%d: %w", v, err)
			}
		}
		if v == 14 {
			if err := s.backfillSeq(ctx, tx); err != nil {
				return err
			}
		}
	}

	if err := s.setSchemaVersion(ctx, tx, schemaVersion); err != nil {
		return err
	}
	return tx.Commit()
}

// backfillSeq gives rows that predate v14 the order the old queries sorted
// them by, so an upgrade cannot silently reorder anybody's history.
//
// SQLite-only, and not as an optimisation: `rowid` is not a column in
// Postgres, so the statement fails when the query is parsed — on an empty
// table as readily as a full one. It is never needed there. A Postgres
// database is created by this code at the current version and cannot predate
// the column, so there is nothing older to backfill from.
func (s *Store) backfillSeq(ctx context.Context, tx *sql.Tx) error {
	if s.d == postgresDialect {
		return nil
	}
	for _, t := range []string{"plans", "runs"} {
		if _, err := tx.ExecContext(ctx, "UPDATE "+t+" SET seq = rowid WHERE seq = 0"); err != nil {
			return fmt.Errorf("backfill %s.seq: %w", t, err)
		}
	}
	return nil
}

// schemaVersionTx reads the version the database is currently at, inside the
// caller's transaction so it is covered by the same advisory lock as the
// migration that follows it.
//
// SQLite keeps the version in the file header, which is free and needs no
// table. Postgres has no equivalent, so it lives in a table of its own —
// created here, because it has to exist before the first migration can ask
// what version the database is.
func (s *Store) schemaVersionTx(ctx context.Context, tx *sql.Tx) (int, error) {
	if s.d == postgresDialect {
		if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version BIGINT NOT NULL)`); err != nil {
			return 0, fmt.Errorf("create schema_version: %w", err)
		}
		var v sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_version`).Scan(&v); err != nil {
			return 0, fmt.Errorf("read schema version: %w", err)
		}
		return int(v.Int64), nil
	}
	var current int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&current); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return current, nil
}

// schemaVersionOf is the same question asked outside a transaction, for tests
// and callers that only want to read.
func (s *Store) schemaVersionOf(ctx context.Context) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	return s.schemaVersionTx(ctx, tx)
}

func (s *Store) setSchemaVersion(ctx context.Context, tx *sql.Tx, v int) error {
	if s.d == postgresDialect {
		if _, err := s.txExec(ctx, tx, `DELETE FROM schema_version`); err != nil {
			return fmt.Errorf("set schema version: %w", err)
		}
		if _, err := s.txExec(ctx, tx, `INSERT INTO schema_version (version) VALUES (?)`, v); err != nil {
			return fmt.Errorf("set schema version: %w", err)
		}
		return nil
	}
	// PRAGMA does not accept a bound parameter.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", v)); err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}
	return nil
}

// Schema v15 — integers that are actually 64-bit, on Postgres.
//
// SQLite's INTEGER is a 64-bit signed integer. Postgres's is 32-bit, and
// v0.6.0 translated one to the other unchanged, so every mem_*_bytes column
// capped at 2147483647 — about 2 GiB, which no real node is under. The
// reclamation ledger could not record a single drain there:
//
//	unable to encode 34359738368 into binary format for int4
//
// Fresh databases get BIGINT from translateDDL. This exists for the ones
// created by v0.6.0 in the window it was published, so they are corrected on
// the next start rather than carrying a silently narrower schema forever.
//
// Empty on SQLite, where INTEGER was always 64-bit and there is nothing to
// widen — see the dialect guard in Migrate.
const schemaV15Postgres = `
ALTER TABLE node_samples ALTER COLUMN cpu_used_milli TYPE BIGINT;
ALTER TABLE node_samples ALTER COLUMN cpu_alloc_milli TYPE BIGINT;
ALTER TABLE plan_steps ALTER COLUMN sequence_number TYPE BIGINT;
ALTER TABLE plan_steps ALTER COLUMN requires_maintenance_window TYPE BIGINT;
ALTER TABLE plans ALTER COLUMN nodes_before TYPE BIGINT;
ALTER TABLE plans ALTER COLUMN nodes_after TYPE BIGINT;
ALTER TABLE plans ALTER COLUMN seq TYPE BIGINT;
ALTER TABLE reclamations ALTER COLUMN step TYPE BIGINT;
ALTER TABLE reclamations ALTER COLUMN cpu_milli TYPE BIGINT;
ALTER TABLE reclamations ALTER COLUMN mem_bytes TYPE BIGINT;
ALTER TABLE reclamations ALTER COLUMN external TYPE BIGINT;
ALTER TABLE recoveries ALTER COLUMN took_seconds TYPE BIGINT;
ALTER TABLE run_events ALTER COLUMN sequence TYPE BIGINT;
ALTER TABLE run_events ALTER COLUMN step TYPE BIGINT;
ALTER TABLE runs ALTER COLUMN dry_run TYPE BIGINT;
ALTER TABLE runs ALTER COLUMN stop_requested TYPE BIGINT;
ALTER TABLE runs ALTER COLUMN seq TYPE BIGINT;
ALTER TABLE samples ALTER COLUMN nodes TYPE BIGINT;
ALTER TABLE samples ALTER COLUMN pods TYPE BIGINT;
ALTER TABLE samples ALTER COLUMN cpu_req_milli TYPE BIGINT;
ALTER TABLE samples ALTER COLUMN cpu_alloc_milli TYPE BIGINT;
ALTER TABLE samples ALTER COLUMN mem_req_bytes TYPE BIGINT;
ALTER TABLE samples ALTER COLUMN mem_alloc_bytes TYPE BIGINT;
ALTER TABLE samples ALTER COLUMN cpu_used_milli TYPE BIGINT;
ALTER TABLE samples ALTER COLUMN mem_used_bytes TYPE BIGINT;
ALTER TABLE samples ALTER COLUMN has_usage TYPE BIGINT;
ALTER TABLE samples ALTER COLUMN reclaimable TYPE BIGINT;
`

// v8: the plan records the packing ceiling it was computed under, so the
// UI's ceiling line can describe the plan on screen rather than the current
// config. A column, not part of the content hash: the same steps under a
// different ceiling are the same actions.
const schemaV8 = `
ALTER TABLE plans ADD COLUMN pack_ceiling REAL NOT NULL DEFAULT 0;
`

// Schema v9 — nodes this product did not drain. Every existing row was
// recorded by the executor, so the default is correct for all of them.
const schemaV9 = `
ALTER TABLE reclamations ADD COLUMN external INTEGER NOT NULL DEFAULT 0;
`

// Schema v14 — an explicit insertion order.
//
// Six queries sorted by SQLite's rowid to break ties between rows sharing a
// timestamp, and one of them decides whether a plan has changed at all. That
// works until the store has to speak to a server without a rowid, and it is
// not a Postgres problem: an implicit ordering nobody declared is a fragile
// thing to have been depending on anywhere.
//
// Assigned by the INSERT itself — `(SELECT COALESCE(MAX(seq), 0) + 1 …)` —
// rather than by an identity column, because AUTOINCREMENT and BIGSERIAL
// differ in spelling and this is the one column whose values must mean the
// same thing in both. One statement, so no read-then-write window: SQLite
// serialises writers outright, and on Postgres two inserts racing at the same
// snapshot would tie rather than skip, which degrades to the arbitrary order
// these queries already had before the column existed.
const schemaV14 = `
ALTER TABLE plans ADD COLUMN seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs  ADD COLUMN seq INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS plans_seq ON plans (seq DESC);
CREATE INDEX IF NOT EXISTS runs_seq  ON runs  (seq DESC);
`

// Schema v13 — a run can be asked to stop. Existing rows were never asked.
const schemaV13 = `
ALTER TABLE runs ADD COLUMN stop_requested INTEGER NOT NULL DEFAULT 0;
ALTER TABLE runs ADD COLUMN stop_requested_by TEXT NOT NULL DEFAULT '';
`

// Schema v12 — how long each workload took to come back after an eviction.
//
// Far smaller than node_samples: one row per workload per drain, not per
// resync. A busy cluster produces a handful a day.
const schemaV12 = `
CREATE TABLE IF NOT EXISTS recoveries (
    workload     TEXT NOT NULL,
    observed_at  TEXT NOT NULL,
    took_seconds INTEGER NOT NULL,
    PRIMARY KEY (workload, observed_at)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS recoveries_observed_at ON recoveries (observed_at);
`

// Schema v11 — utilisation per node over time, so the UI can distinguish a
// node that has been idle for a fortnight from one that spikes nightly.
//
// One row per node per resync. At a thousand nodes and a thirty-second cycle
// that is 2.9M rows a day, which is why retention here is days rather than the
// month the fleet-level timeline keeps, and why the read path aggregates in
// SQL rather than shipping points.
const schemaV11 = `
CREATE TABLE IF NOT EXISTS node_samples (
    node            TEXT NOT NULL,
    taken_at        TEXT NOT NULL,
    cpu_used_milli  INTEGER NOT NULL,
    cpu_alloc_milli INTEGER NOT NULL,
    PRIMARY KEY (node, taken_at)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS node_samples_taken_at ON node_samples (taken_at);
`

// Schema v10 — what the reclaimed node was, so the ledger can be priced.
// Empty on existing rows, which is honest: nobody recorded it at the time.
const schemaV10 = `
ALTER TABLE reclamations ADD COLUMN instance_type TEXT NOT NULL DEFAULT '';
ALTER TABLE reclamations ADD COLUMN capacity_type TEXT NOT NULL DEFAULT '';
`

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

// Schema v4 — M25: converge runs. A run gains a mode and, for converge, the
// envelope the operator consented to. Stored as columns-plus-blob in the
// established shape: mode is queried (the executor logs it), the envelope is
// opaque consent detail read back whole.
const schemaV4 = `
ALTER TABLE runs ADD COLUMN mode TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN envelope BLOB;
`

// Schema v5 — the savings ledger. A reclamation row gains the node's
// allocatable, captured at drain time by the executor: the last moment it can
// be, because a reclaimed node takes its capacity record with it. Old rows
// keep zeroes and the summary counts them as uncounted rather than silently
// under-reporting.
const schemaV5 = `
ALTER TABLE reclamations ADD COLUMN cpu_milli INTEGER NOT NULL DEFAULT 0;
ALTER TABLE reclamations ADD COLUMN mem_bytes INTEGER NOT NULL DEFAULT 0;
`

// Schema v6 — guarded drain: a run can target one named node.
const schemaV6 = `
ALTER TABLE runs ADD COLUMN node TEXT NOT NULL DEFAULT '';
`

// Schema v7 — the timeline. One small row per publish cycle; pruned to a
// window by the planner. Indexed on taken_at because every read is a range.
const schemaV7 = `
CREATE TABLE IF NOT EXISTS samples (
    taken_at        TEXT NOT NULL,
    nodes           INTEGER NOT NULL,
    pods            INTEGER NOT NULL,
    cpu_req_milli   INTEGER NOT NULL,
    cpu_alloc_milli INTEGER NOT NULL,
    mem_req_bytes   INTEGER NOT NULL,
    mem_alloc_bytes INTEGER NOT NULL,
    cpu_used_milli  INTEGER NOT NULL,
    mem_used_bytes  INTEGER NOT NULL,
    has_usage       INTEGER NOT NULL,
    reclaimable     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS samples_taken_at ON samples (taken_at);
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

	// One statement does both halves of the dedup: touch stored_at when the
	// incoming plan IS the latest one, and report via rows-affected whether
	// that happened. The previous SELECT-then-UPDATE pair had a gap a
	// concurrent writer could slip through, and cost a round-trip.
	//
	// stored_at is touched on the dedup path because it means "last confirmed
	// against the cluster" and that is what a reader needs. Leaving it alone
	// made a plan the planner had just re-verified read as nineteen hours
	// old, so the UI's staleness warning fired on the healthiest possible
	// state. An unchanged plan is the strongest evidence it is current.
	// When the snapshot carries usage data, the dedup touch refreshes the
	// snapshot blob too. Usage changes every cycle while the plan does not —
	// that is what makes a cluster steady — so a report reading the stored
	// snapshot would otherwise serve measurements from whenever the plan
	// last changed, aging indefinitely. The write is one compressed blob per
	// resync, paid only by installations that opted into usage collection.
	var touched interface{ RowsAffected() (int64, error) }
	var err error
	if rec.Snapshot != nil && rec.Snapshot.HasUsageData {
		snapshotJSON, encErr := encodeBlob(rec.Snapshot)
		if encErr != nil {
			return false, fmt.Errorf("encode snapshot: %w", encErr)
		}
		touched, err = s.exec(ctx,
			`UPDATE plans SET stored_at = ?, pack_ceiling = ?, nodes_before = ?, nodes_after = ?, snapshot = ?
			 WHERE id = ? AND id = (SELECT id FROM plans ORDER BY stored_at DESC, seq DESC LIMIT 1)`,
			storedAt.UTC().Format(time.RFC3339Nano), rec.Plan.PackCeiling,
			rec.Plan.NodesBefore, rec.Plan.NodesAfter, snapshotJSON, rec.Plan.ID)
	} else {
		// pack_ceiling rides the touch for the same reason stored_at does:
		// an upgraded planner re-verifying an identical plan under a new
		// ceiling must not leave the stored row describing the old policy —
		// found live, the Wells lens drawing no ceiling after the upgrade.
		//
		// nodes_before and nodes_after ride it for a sharper version of the
		// same problem. planID hashes the steps and nothing else, which is
		// right — a plan is its steps — but it means an empty plan keeps its
		// identity while the fleet changes size underneath it. On a real GKE
		// cluster the CLI reported "6 nodes now, 6 after" for twelve minutes
		// after two were removed, while the planner logged nodesBefore:4 on
		// every cycle. Fresh by timestamp and stale by content is the worst
		// of the available failures: the reader has no reason to doubt it.
		touched, err = s.exec(ctx,
			`UPDATE plans SET stored_at = ?, pack_ceiling = ?, nodes_before = ?, nodes_after = ?
			 WHERE id = ? AND id = (SELECT id FROM plans ORDER BY stored_at DESC, seq DESC LIMIT 1)`,
			storedAt.UTC().Format(time.RFC3339Nano), rec.Plan.PackCeiling,
			rec.Plan.NodesBefore, rec.Plan.NodesAfter, rec.Plan.ID)
	}
	if err != nil {
		return false, fmt.Errorf("touch plan: %w", err)
	}
	if n, err := touched.RowsAffected(); err != nil {
		return false, fmt.Errorf("touch plan: %w", err)
	} else if n > 0 {
		// Same content hash as the last write: the cluster has not changed in
		// any way that alters the plan.
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
	if _, err := s.txExec(ctx, tx, `
		INSERT INTO plans (id, generated_at, snapshot_taken_at, status, strategy,
		                   nodes_before, nodes_after, pack_ceiling, snapshot, analysis, stored_at, seq)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		        (SELECT COALESCE(MAX(seq), 0) + 1 FROM plans))
		ON CONFLICT(id) DO UPDATE SET
		    generated_at = excluded.generated_at,
		    stored_at    = excluded.stored_at,
		    status       = excluded.status,
		    pack_ceiling = excluded.pack_ceiling,
		    snapshot     = excluded.snapshot,
		    analysis     = excluded.analysis,
		    seq          = excluded.seq`,
		rec.Plan.ID,
		rec.Plan.GeneratedAt.UTC().Format(time.RFC3339Nano),
		rec.Plan.SnapshotTakenAt.UTC().Format(time.RFC3339Nano),
		string(rec.Plan.Status),
		rec.Strategy,
		rec.Plan.NodesBefore,
		rec.Plan.NodesAfter,
		rec.Plan.PackCeiling,
		snapshotJSON,
		analysisJSON,
		storedAt.Format(time.RFC3339Nano),
	); err != nil {
		return false, fmt.Errorf("insert plan: %w", err)
	}

	if _, err := s.txExec(ctx, tx, `DELETE FROM plan_steps WHERE plan_id = ?`, rec.Plan.ID); err != nil {
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
		if _, err := s.txExec(ctx, tx, `
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
	err := s.queryRow(ctx,
		`SELECT id FROM plans ORDER BY stored_at DESC, seq DESC LIMIT 1`).Scan(&id)
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
		packCeiling                  float64
	)
	err := s.queryRow(ctx, `
		SELECT generated_at, snapshot_taken_at, status, strategy,
		       nodes_before, nodes_after, pack_ceiling, snapshot, analysis, stored_at
		FROM plans WHERE id = ?`, id).
		Scan(&generatedAt, &snapshotTakenAt, &status, &rec.Strategy,
			&nodesBefore, &nodesAfter, &packCeiling, &snapshotJSON, &analysisJSON, &storedAt)
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
		PackCeiling:     packCeiling,
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

	rows, err := s.query(ctx, `
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
	rows, err := s.query(ctx, `
		SELECT p.id, p.generated_at, p.snapshot_taken_at, p.status, p.strategy,
		       p.nodes_before, p.nodes_after, p.stored_at,
		       (SELECT COUNT(*) FROM plan_steps s WHERE s.plan_id = p.id),
		       (SELECT COUNT(*) FROM plan_steps s WHERE s.plan_id = p.id AND s.impact = 'Green'),
		       (SELECT COUNT(*) FROM plan_steps s WHERE s.plan_id = p.id AND s.impact = 'Yellow'),
		       (SELECT COUNT(*) FROM plan_steps s WHERE s.plan_id = p.id AND s.impact = 'Red')
		FROM plans p ORDER BY p.stored_at DESC, p.seq DESC LIMIT ?`, limit)
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
	res, err := s.exec(ctx, `
		DELETE FROM plans WHERE id NOT IN (
			SELECT id FROM plans ORDER BY stored_at DESC, seq DESC LIMIT ?
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
	_, err := s.exec(ctx, query, args...)
	return err
}

// QueryRowForTest reads one row directly. Test-only.
func (s *Store) QueryRowForTest(ctx context.Context, query string, args ...any) *sql.Row {
	return s.queryRow(ctx, query, args...)
}
