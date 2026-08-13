package store

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"
	"strings"
)

// ErrUnavailable means the plan store could not be reached or is not usable —
// as distinct from a query that ran and found nothing (ErrNotFound), or a bug.
//
// The distinction is what an operator sees. A ui-backend whose database has
// gone away answered every request with "internal error", which says only that
// something is wrong somewhere in a product with a lot of somewheres. Naming
// the store turns an investigation into a glance.
var ErrUnavailable = errors.New("plan store unavailable")

// IsUnavailable reports whether err means the store cannot currently serve.
//
// Deliberately here rather than in the SQL implementation, and deliberately
// without importing either driver: pgconn's error type exposes SQLState()
// through an interface, so the Postgres codes can be recognised without this
// package depending on pgx. The two drivers disagree about everything else, so
// the rest is matched on what they have in common.
func IsUnavailable(err error) bool {
	if err == nil || errors.Is(err, ErrNotFound) {
		return false
	}
	if errors.Is(err, ErrUnavailable) ||
		errors.Is(err, driver.ErrBadConn) ||
		errors.Is(err, sql.ErrConnDone) {
		return true
	}

	// The connection itself never came up, or went away mid-flight.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// Postgres, via any error type carrying a SQLSTATE. Class 08 is
	// connection exception; the rest are the specific ways a database can be
	// present but unusable — including a schema that is not there, which is
	// what a restore from before the current version looks like.
	var coded interface{ SQLState() string }
	if errors.As(err, &coded) {
		code := coded.SQLState()
		switch {
		case strings.HasPrefix(code, "08"): // connection exception
			return true
		case code == "42P01", // undefined_table
			code == "3D000", // invalid_catalog_name
			code == "57P03", // cannot_connect_now
			code == "53300": // too_many_connections
			return true
		}
	}

	// SQLite has no error codes worth matching on, and its failure here is the
	// same shape: the file is gone, or was never migrated.
	msg := err.Error()
	return strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "unable to open database")
}
