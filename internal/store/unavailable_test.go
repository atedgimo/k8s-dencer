package store_test

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/atedgimo/k8s-dencer/internal/store"
)

// A stand-in for pgconn.PgError, which is recognised through its SQLState
// method rather than its type so this package need not import a driver.
type pgErr struct{ code, msg string }

func (e pgErr) Error() string    { return "ERROR: " + e.msg + " (SQLSTATE " + e.code + ")" }
func (e pgErr) SQLState() string { return e.code }

func TestUnavailableRecognisesAStoreThatCannotServe(t *testing.T) {
	for name, err := range map[string]error{
		// The one that started this: a schema that is not there, which is what
		// a restore from before the current version looks like.
		"postgres missing table":  pgErr{"42P01", `relation "runs" does not exist`},
		"postgres wrong database": pgErr{"3D000", "database does not exist"},
		"postgres starting up":    pgErr{"57P03", "the database system is starting up"},
		"postgres out of conns":   pgErr{"53300", "too many connections"},
		"postgres connection":     pgErr{"08006", "connection failure"},
		"connection refused":      fmt.Errorf("dial: %w", &net.OpError{Op: "dial", Err: errors.New("connection refused")}),
		"bad conn":                fmt.Errorf("query: %w", driver.ErrBadConn),
		"closed pool":             fmt.Errorf("query: %w", sql.ErrConnDone),
		"sqlite unmigrated":       errors.New("no such table: runs"),
		"sqlite file gone":        errors.New("unable to open database file"),
		"already classified":      fmt.Errorf("wrapped: %w", store.ErrUnavailable),
	} {
		t.Run(name, func(t *testing.T) {
			if !store.IsUnavailable(err) {
				t.Errorf("not recognised as an unavailable store: %v", err)
			}
		})
	}
}

// The distinction is the whole point: these must stay "internal error" or
// "not found", because reporting them as an unreachable database sends an
// operator to look at Postgres when the problem is here.
func TestUnavailableDoesNotSwallowRealErrors(t *testing.T) {
	for name, err := range map[string]error{
		"nothing wrong":       nil,
		"empty store":         store.ErrNotFound,
		"wrapped not found":   fmt.Errorf("latest: %w", store.ErrNotFound),
		"a bug":               errors.New("nil pointer dereference"),
		"bad json in a row":   errors.New("unmarshal snapshot: unexpected end of JSON input"),
		"constraint violated": pgErr{"23505", "duplicate key value violates unique constraint"},
	} {
		t.Run(name, func(t *testing.T) {
			if store.IsUnavailable(err) {
				t.Errorf("misreported as an unavailable store: %v", err)
			}
		})
	}
}
