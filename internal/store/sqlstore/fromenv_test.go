package sqlstore

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
)

// A password is not a string that can be pasted into a URL. One containing
// '@' splits the userinfo from the host, and the DSN then names a different
// server — which either fails to connect or, worse, connects somewhere the
// operator did not intend. Postgres passwords are frequently generated, and
// generated passwords contain punctuation.
func TestPostgresDSNEscapesTheCredentials(t *testing.T) {
	t.Setenv("POSTGRES_HOST", "pg.internal")
	t.Setenv("POSTGRES_PORT", "6432")
	t.Setenv("POSTGRES_DATABASE", "dencer")
	t.Setenv("POSTGRES_USER", "dencer")
	t.Setenv("POSTGRES_PASSWORD", "p@ss/w:rd?#x")

	dsn, desc, err := postgresDSN()
	if err != nil {
		t.Fatalf("build dsn: %v", err)
	}

	// The host must survive the password verbatim.
	if !strings.Contains(dsn, "@pg.internal:6432/dencer") {
		t.Errorf("the password changed where the DSN points: %s", redact(dsn))
	}
	if strings.Contains(dsn, "p@ss/w:rd?#x") {
		t.Error("the password was interpolated raw rather than escaped")
	}

	// Whatever is logged must not be the credentials.
	for _, secret := range []string{"p@ss", "w:rd", "dencer:"} {
		if strings.Contains(desc, secret) {
			t.Errorf("the loggable description leaks part of the credentials: %q", desc)
		}
	}
	if !strings.Contains(desc, "pg.internal:6432") {
		t.Errorf("the description does not say where it connected: %q", desc)
	}
}

// sslmode is the difference between an encrypted connection and a plaintext
// one, and the default has to be the safe one: an operator who says nothing
// should not silently get plaintext.
func TestPostgresDSNDefaultsToRequiringTLS(t *testing.T) {
	t.Setenv("POSTGRES_HOST", "pg.internal")

	dsn, _, err := postgresDSN()
	if err != nil {
		t.Fatalf("build dsn: %v", err)
	}
	if !strings.Contains(dsn, "sslmode=require") {
		t.Errorf("default DSN does not require TLS: %s", redact(dsn))
	}
}

// A missing host is a misconfiguration that should be named, not turned into
// a connection attempt against nothing.
func TestPostgresWithoutAHostIsRefused(t *testing.T) {
	t.Setenv("POSTGRES_HOST", "")

	if _, _, err := postgresDSN(); err == nil {
		t.Fatal("a postgres store with no host was accepted")
	}
}

// No password set at all is legitimate — trust, peer and certificate
// authentication all exist — and must not become the literal string "".
func TestPostgresWithoutAPasswordSendsNone(t *testing.T) {
	t.Setenv("POSTGRES_HOST", "pg.internal")
	t.Setenv("POSTGRES_USER", "dencer")

	dsn, _, err := postgresDSN()
	if err != nil {
		t.Fatalf("build dsn: %v", err)
	}
	if strings.Contains(dsn, "dencer:@") {
		t.Errorf("an empty password was sent as a password: %s", redact(dsn))
	}
	if !strings.Contains(dsn, "dencer@pg.internal") {
		t.Errorf("the user is not in the DSN: %s", redact(dsn))
	}
}

func redact(dsn string) string {
	at := strings.LastIndex(dsn, "@")
	if at < 0 {
		return dsn
	}
	return "postgres://…" + dsn[at:]
}

// The env names in OpenFromEnv and the env names the chart emits are two
// lists that have to match, and nothing in Go checks a Helm template. This at
// least proves the Go half works end to end against a real server: the same
// variables, through the same function the three binaries call, to a database
// that migrates.
//
// The chart half is checked by hack/lint-chart.sh, which asserts all three
// components are told the type and that the password arrives from a Secret.
func TestOpenFromEnvConnectsToPostgres(t *testing.T) {
	dsn := os.Getenv("DENCER_TEST_POSTGRES")
	if dsn == "" {
		t.Skip("set DENCER_TEST_POSTGRES to run against a real Postgres")
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("DENCER_TEST_POSTGRES is not a URL: %v", err)
	}
	host, port := u.Hostname(), u.Port()
	if port == "" {
		port = "5432"
	}
	pw, _ := u.User.Password()

	t.Setenv("DATABASE_TYPE", "postgres")
	t.Setenv("POSTGRES_HOST", host)
	t.Setenv("POSTGRES_PORT", port)
	t.Setenv("POSTGRES_DATABASE", strings.TrimPrefix(u.Path, "/"))
	t.Setenv("POSTGRES_USER", u.User.Username())
	t.Setenv("POSTGRES_PASSWORD", pw)
	// The container has no TLS; the default is require, and overriding it here
	// is the same knob an operator turns for an in-cluster server.
	t.Setenv("POSTGRES_SSLMODE", "disable")

	ctx := context.Background()
	s, desc, err := OpenFromEnv(ctx)
	if err != nil {
		t.Fatalf("OpenFromEnv: %v", err)
	}
	defer func() { _ = s.Close() }()

	if strings.Contains(desc, pw) && pw != "" {
		t.Errorf("the description logged at startup contains the password: %q", desc)
	}
	if !strings.HasPrefix(desc, "postgres at ") {
		t.Errorf("description = %q, want it to say which backend is in use", desc)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate through OpenFromEnv: %v", err)
	}
}

// sslmode=require encrypts but does not authenticate: pgx sets
// InsecureSkipVerify when no root certificate is configured, so anything on
// the network path can present a certificate and be believed. The store
// carries the credentials, the audit trail and the run queue the executor
// drains from, so the difference is worth surfacing rather than implying.
func TestPostgresDSNCarriesTheRootCertificate(t *testing.T) {
	t.Setenv("POSTGRES_HOST", "pg.internal")
	t.Setenv("POSTGRES_SSLMODE", "verify-full")
	t.Setenv("POSTGRES_SSLROOTCERT", "/etc/dencer/postgres-ca/ca.crt")

	dsn, desc, err := postgresDSN()
	if err != nil {
		t.Fatalf("build dsn: %v", err)
	}
	if !strings.Contains(dsn, "sslrootcert=%2Fetc%2Fdencer%2Fpostgres-ca%2Fca.crt") &&
		!strings.Contains(dsn, "sslrootcert=/etc/dencer/postgres-ca/ca.crt") {
		t.Errorf("root certificate not passed to the driver: %s", redact(dsn))
	}
	if !strings.Contains(desc, "verified") || strings.Contains(desc, "NOT verified") {
		t.Errorf("description should record that the server is verified: %q", desc)
	}
}

// And the default must say plainly that it is not verifying anything, because
// the operator who never set sslMode is exactly the one who will assume it is.
func TestPostgresDefaultSaysItDoesNotVerifyTheServer(t *testing.T) {
	t.Setenv("POSTGRES_HOST", "pg.internal")

	_, desc, err := postgresDSN()
	if err != nil {
		t.Fatalf("build dsn: %v", err)
	}
	if !strings.Contains(desc, "NOT verified") {
		t.Errorf("the default hides that the server is unauthenticated: %q", desc)
	}
}

// A Secret key that exists but is empty is a routine partially-populated
// state. It must mean "no password", not "the password is the empty string".
func TestPostgresEmptyPasswordIsNoPassword(t *testing.T) {
	t.Setenv("POSTGRES_HOST", "pg.internal")
	t.Setenv("POSTGRES_USER", "dencer")
	t.Setenv("POSTGRES_PASSWORD", "")

	dsn, _, err := postgresDSN()
	if err != nil {
		t.Fatalf("build dsn: %v", err)
	}
	if strings.Contains(dsn, "dencer:@") {
		t.Errorf("an empty password was sent as a password: %s", redact(dsn))
	}
}
