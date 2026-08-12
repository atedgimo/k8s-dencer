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
	if pw, ok := os.LookupEnv("POSTGRES_PASSWORD"); ok {
		u.User = url.UserPassword(user, pw)
	} else {
		u.User = url.User(user)
	}

	q := url.Values{}
	// Defaulting to require rather than to the driver's default: an operator
	// who says nothing should get an encrypted connection, not a plaintext
	// one, and the chart's default matches.
	q.Set("sslmode", env("POSTGRES_SSLMODE", "require"))
	u.RawQuery = q.Encode()

	return u.String(), fmt.Sprintf("postgres at %s/%s (sslmode=%s)", u.Host, name, q.Get("sslmode")), nil
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
