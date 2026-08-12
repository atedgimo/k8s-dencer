package sqlstore

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"sync"
	"testing"
)

// A Postgres database is created by this code at the current version, so
// today there is nothing older to upgrade and the SQLite fixture has no
// counterpart here. That is true exactly once. The next schema version will
// be applied to Postgres databases in the field, through the same loop, and
// the first time anyone finds out whether that works should not be during
// somebody's upgrade.
//
// So the property under test is not "v13 to v14 works" — it is that Migrate
// is *resumable* on Postgres: a database left partway through completes on
// the next start, applies only the versions it is missing, and is idempotent
// once it arrives. Everything a future migration depends on.
func TestPostgresMigrationResumesFromPartial(t *testing.T) {
	s, schema := openPostgresSchema(t, "resume")
	ctx := context.Background()

	// Stop deliberately short of the current version, which is what an
	// interrupted upgrade or an older release leaves behind.
	const partial = 12
	if got := applyThrough(ctx, t, s, partial); got != partial {
		t.Fatalf("setup left the database at v%d, want v%d", got, partial)
	}

	// The real entry point, exactly as a starting ui-backend calls it.
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("resuming a partially migrated Postgres database failed — this is what every future upgrade does: %v", err)
	}
	if got := versionOf(ctx, t, s); got != schemaVersion {
		t.Fatalf("after resuming, version = %d, want %d", got, schemaVersion)
	}

	// The columns the last versions add must actually be there. Asking the
	// catalogue rather than trusting the version number, because a migration
	// that bumped the version without applying its DDL would otherwise look
	// like a success and fail later on a query.
	for _, c := range []struct{ table, column string }{
		{"plans", "seq"},
		{"runs", "seq"},
		{"runs", "stop_requested"},
	} {
		if !columnExists(ctx, t, s, schema, c.table, c.column) {
			t.Errorf("%s.%s is missing after the resumed migration", c.table, c.column)
		}
	}

	// Idempotent on arrival: the planner and the ui-backend both call Migrate
	// on every start, so this runs far more often than it does anything.
	for i := range 3 {
		if err := s.Migrate(ctx); err != nil {
			t.Fatalf("Migrate is not idempotent at the current version (call %d): %v", i+2, err)
		}
	}

	// And the store works afterwards, which is the only thing the operator
	// actually cares about.
	if _, err := s.Latest(ctx); err == nil {
		t.Error("an empty store returned a plan")
	}
}

// A database already at the current version must not have migrations replayed
// against it — every schema step here is an unguarded ALTER TABLE, and a
// second application would error rather than no-op.
func TestPostgresMigrationSkipsWhenCurrent(t *testing.T) {
	s, _ := openPostgresSchema(t, "current")
	ctx := context.Background()

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("first migrate: %v", err)
	}
	before := versionOf(ctx, t, s)

	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second migrate on an up-to-date database: %v", err)
	}
	if after := versionOf(ctx, t, s); after != before {
		t.Errorf("version moved from %d to %d without a new schema version", before, after)
	}

	// One row, not one per start: the version is replaced rather than
	// appended, or a long-lived install accumulates a row every restart.
	var rows int
	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM schema_version`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("schema_version holds %d rows, want 1", rows)
	}
}

// applyThrough migrates only as far as version v, leaving the database in the
// state an interrupted or older install would have.
func applyThrough(ctx context.Context, t *testing.T, s *Store, v int) int {
	t.Helper()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()

	// Inside this transaction, because schemaVersionTx creates the version
	// table as a side effect and it has to be committed with the rest. The
	// standalone schemaVersionOf rolls back, which makes it a safe reader and
	// useless for setup.
	if _, err := s.schemaVersionTx(ctx, tx); err != nil {
		t.Fatalf("prepare version table: %v", err)
	}

	for i, ddl := range s.migrations() {
		if i+1 > v {
			break
		}
		if strings.TrimSpace(ddl) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, translateDDL(s.d, ddl)); err != nil {
			t.Fatalf("apply schema v%d: %v", i+1, err)
		}
	}
	if err := s.setSchemaVersion(ctx, tx, v); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return versionOf(ctx, t, s)
}

func versionOf(ctx context.Context, t *testing.T, s *Store) int {
	t.Helper()
	v, err := s.schemaVersionOf(ctx)
	if err != nil {
		t.Fatalf("read version: %v", err)
	}
	return v
}

func columnExists(ctx context.Context, t *testing.T, s *Store, schema, table, column string) bool {
	t.Helper()
	var n int
	err := s.queryRow(ctx, `
		SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = ? AND table_name = ? AND column_name = ?`,
		schema, table, column).Scan(&n)
	if err != nil {
		t.Fatalf("inspect %s.%s: %v", table, column, err)
	}
	return n > 0
}

// openPostgresSchema gives the test its own schema on the shared server and
// returns the store together with the schema name, unmigrated.
func openPostgresSchema(t *testing.T, name string) (*Store, string) {
	t.Helper()
	dsn := os.Getenv("DENCER_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set DENCER_TEST_POSTGRES to run against a real Postgres")
	}
	ctx := context.Background()
	schema := "m_" + name

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer func() { _ = admin.Close() }()
	for _, q := range []string{`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`, `CREATE SCHEMA ` + schema} {
		if _, err := admin.ExecContext(ctx, q); err != nil {
			t.Fatalf("prepare schema: %v", err)
		}
	}

	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	s, err := OpenPostgres(ctx, dsn+sep+"search_path="+schema)
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		if db, err := sql.Open("pgx", dsn); err == nil {
			_, _ = db.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
			_ = db.Close()
		}
	})
	return s, schema
}

// The planner and the ui-backend both migrate at startup, and on a fresh
// install both pods start together. Before the advisory lock this raced: one
// pod created schema_version while the other was doing the same and the loser
// died on `duplicate key value violates unique constraint
// "pg_type_typname_nsp_index"`, then CrashLoopBackOffed until the winner
// committed. It self-healed, which is precisely why it would have been
// diagnosed as something else.
//
// Postgres exists here to let uiBackend.replicaCount rise, so the number of
// simultaneous migrators is a value an operator sets. This runs eight.
func TestPostgresMigrationIsSafeWhenPodsStartTogether(t *testing.T) {
	dsn := os.Getenv("DENCER_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set DENCER_TEST_POSTGRES to run against a real Postgres")
	}
	ctx := context.Background()
	schema := "m_concurrent"

	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close() }()
	for _, q := range []string{`DROP SCHEMA IF EXISTS ` + schema + ` CASCADE`, `CREATE SCHEMA ` + schema} {
		if _, err := admin.ExecContext(ctx, q); err != nil {
			t.Fatalf("prepare schema: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`)
	})

	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}

	const migrators = 8
	errs := make(chan error, migrators)
	var wg sync.WaitGroup
	for range migrators {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A separate connection each, because separate pods are what this
			// is simulating — sharing one pool would hide the race.
			s, err := OpenPostgres(ctx, dsn+sep+"search_path="+schema)
			if err != nil {
				errs <- err
				return
			}
			defer func() { _ = s.Close() }()
			errs <- s.Migrate(ctx)
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("a pod failed to start because another was migrating: %v", err)
		}
	}

	// One row, and the schema actually built — a lock that serialised the
	// migrations into doing nothing would also produce no errors.
	s, err := OpenPostgres(ctx, dsn+sep+"search_path="+schema)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if got := versionOf(ctx, t, s); got != schemaVersion {
		t.Errorf("version = %d, want %d", got, schemaVersion)
	}
	var rows int
	if err := s.queryRow(ctx, `SELECT COUNT(*) FROM schema_version`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("schema_version holds %d rows, want 1", rows)
	}
	if !columnExists(ctx, t, s, schema, "plans", "seq") {
		t.Error("plans.seq missing; the migrations were serialised into doing nothing")
	}
}
