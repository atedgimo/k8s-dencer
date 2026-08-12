package sqlstore

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// OpenFromEnv opens the plan store the deployment is configured for, and
// returns a description of it safe to log.
//
// One function rather than the same twenty lines in planner, executor and
// ui-backend. Three components must agree about which database they are
// talking to; if they disagree the planner writes plans the UI cannot see, and
// nothing reports an error. That is the same argument as one Store over two,
// applied one level up.
func OpenFromEnv(ctx context.Context) (*Store, string, error) {
	switch t := env("DATABASE_TYPE", "sqlite"); t {
	case "sqlite":
		path := env("DATABASE_PATH", "/data/dencer.db")
		s, err := Open(path)
		if err != nil {
			return nil, "", err
		}
		return s, "sqlite at " + path, nil

	case "postgres":
		dsn, desc, err := postgresDSN()
		if err != nil {
			return nil, "", err
		}
		s, err := OpenPostgres(ctx, dsn)
		if err != nil {
			return nil, "", err
		}
		return s, desc, nil

	default:
		// A binary launched outside the chart should fail loudly rather than
		// quietly falling back to a different database than it was asked for.
		return nil, "", fmt.Errorf("unsupported DATABASE_TYPE %q: want sqlite or postgres", t)
	}
}

// postgresDSN assembles the connection string from its parts, and returns a
// description with no password in it.
//
// Assembled here rather than taken whole as one env var so the password can
// arrive separately from a Secret, and never has to appear in a Helm value or
// in the rendered Deployment. url.URL does the escaping, because a password
// containing '@' or '/' silently produces a DSN pointing somewhere else.
func postgresDSN() (dsn, desc string, err error) {
	host := env("POSTGRES_HOST", "")
	if host == "" {
		return "", "", fmt.Errorf("DATABASE_TYPE is postgres but POSTGRES_HOST is empty")
	}
	port := env("POSTGRES_PORT", "5432")
	name := strings.TrimPrefix(env("POSTGRES_DATABASE", "dencer"), "/")
	user := env("POSTGRES_USER", "dencer")

	u := url.URL{
		Scheme: "postgres",
		Host:   net.JoinHostPort(host, port),
		Path:   "/" + name,
	}
	// An empty value means "no password", the same rule env() applies three
	// lines up. LookupEnv would call "" set, and a Secret key that exists but
	// is empty — a routine partially-populated state — would then send an
	// explicit empty password to a server that would otherwise have accepted
	// trust, peer or certificate authentication.
	if pw := env("POSTGRES_PASSWORD", ""); pw != "" {
		u.User = url.UserPassword(user, pw)
	} else {
		u.User = url.User(user)
	}

	q := url.Values{}
	// Defaulting to require rather than to the driver's default, so an
	// operator who says nothing gets an encrypted connection rather than a
	// plaintext one.
	//
	// Worth being precise about what that does not buy: with no root
	// certificate, pgx sets InsecureSkipVerify for sslmode=require, so the
	// channel is encrypted but the server is never authenticated — anything
	// on the network path can present a certificate and be believed.
	// POSTGRES_SSLROOTCERT is what closes that, and with a root cert present
	// pgx upgrades require to verify-ca behaviour on its own.
	mode := env("POSTGRES_SSLMODE", "require")
	q.Set("sslmode", mode)
	if ca := env("POSTGRES_SSLROOTCERT", ""); ca != "" {
		q.Set("sslrootcert", ca)
	}
	u.RawQuery = q.Encode()

	verified := "server certificate NOT verified"
	if q.Get("sslrootcert") != "" || mode == "verify-full" || mode == "verify-ca" {
		verified = "server certificate verified"
	}
	return u.String(), fmt.Sprintf("postgres at %s/%s (sslmode=%s, %s)", u.Host, name, mode, verified), nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
