package sqlstore

import (
	"context"
	"database/sql"
	"regexp"
	"strconv"
	"strings"
)

// dialect is the difference between the two servers this store speaks to.
//
// One implementation rather than two. The alternative — a parallel package
// with its own copy of thirty-two methods — is the mistake this project has
// now made twice and fixed twice: two things that must agree, kept apart, and
// discovered to disagree by a user. A store is the product's memory, and two
// memories that drift are worse than one that is a little more general.
type dialect int

const (
	sqliteDialect dialect = iota
	postgresDialect
)

func (d dialect) String() string {
	if d == postgresDialect {
		return "postgres"
	}
	return "sqlite"
}

// rebind turns the `?` placeholders every query is written with into the
// `$1, $2` form Postgres requires.
//
// Queries are authored once, in SQLite's spelling, because that is what the
// existing thirteen schema versions and their tests are written against.
// Rewriting them all to `$N` would have made the SQLite path the translated
// one, and the translated path is the one that rots.
//
// Literals are respected: a `?` inside a quoted string is data, not a
// placeholder, and rewriting one would corrupt a query in a way no type
// checker would notice.
func rebind(d dialect, query string) string {
	if d != postgresDialect || !strings.ContainsRune(query, '?') {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 8)
	n := 0
	inSingle, inDouble := false, false
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
		case c == '"' && !inSingle:
			inDouble = !inDouble
		case c == '?' && !inSingle && !inDouble:
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// ddlTypes maps SQLite's type names to the Postgres type that means the same
// thing. Matched on word boundaries, because a plain substring replacement
// would rewrite a future column named REAL_COST into DOUBLE PRECISION_COST —
// on Postgres only, so it would reproduce on nobody's SQLite machine.
var ddlTypes = map[string]string{
	// SQLite stores arbitrary bytes in BLOB; Postgres calls it BYTEA. Every
	// use is a gzipped or JSON payload the store already marshals itself.
	"BLOB": "BYTEA",

	// Postgres REAL is 4-byte and SQLite's is 8. The one column using it is
	// the packing ceiling, a fraction compared against computed capacity, so
	// the narrower type would quietly change plan output.
	"REAL": "DOUBLE PRECISION",

	// The one that got away, and the reason this list is now anchored and
	// tested. SQLite's INTEGER is a 64-bit signed integer; Postgres's is
	// 32-bit, topping out at 2147483647. Every mem_*_bytes column in this
	// schema exceeds that on any node with more than 2 GiB of memory — which
	// is every node. v0.6.0 shipped with these as int4 and the reclamation
	// ledger could not record a single drain:
	//
	//	unable to encode 34359738368 into binary format for int4
	//
	// BIGINT is not a widening for safety's sake; it is the faithful
	// translation. INTEGER was never 32 bits on the backend this schema was
	// written for.
	"INTEGER": "BIGINT",
}

var ddlTypePattern = regexp.MustCompile(`\b(BLOB|REAL|INTEGER)\b`)

// translateDDL rewrites schema statements for the target server.
//
// Only DDL goes through here. Queries are portable already: the placeholders
// are handled by rebind, and ON CONFLICT is spelled the same in both.
func translateDDL(d dialect, ddl string) string {
	if d != postgresDialect {
		return ddl
	}
	// A SQLite storage optimisation with no Postgres equivalent and no
	// meaning there — the tables it applies to are keyed by a text primary
	// key either way.
	ddl = strings.ReplaceAll(ddl, ") WITHOUT ROWID;", ");")
	return ddlTypePattern.ReplaceAllStringFunc(ddl, func(m string) string {
		return ddlTypes[m]
	})
}

// The three call shapes every method uses, routed through one place so a
// query cannot reach a server in the wrong dialect.
func (s *Store) exec(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return s.db.ExecContext(ctx, rebind(s.d, q), args...)
}

func (s *Store) query(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, rebind(s.d, q), args...)
}

func (s *Store) queryRow(ctx context.Context, q string, args ...any) *sql.Row {
	return s.db.QueryRowContext(ctx, rebind(s.d, q), args...)
}

// txExec is the same for statements inside a transaction, which migrations
// and Save both use.
func (s *Store) txExec(ctx context.Context, tx *sql.Tx, q string, args ...any) (sql.Result, error) {
	return tx.ExecContext(ctx, rebind(s.d, q), args...)
}
