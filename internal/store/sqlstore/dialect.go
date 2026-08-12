package sqlstore

import (
	"context"
	"database/sql"
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
