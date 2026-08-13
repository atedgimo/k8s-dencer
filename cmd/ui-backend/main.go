// Command ui-backend serves the k8s-dencer read API, the graph payload for the
// frontend, and (from M8) the MCP tool surface consumed by the Kagent agent.
//
// It owns the plan-store schema and runs migrations at startup. The planner
// writes plans into the same database over a shared volume; the two never talk
// to each other directly, so a ui-backend outage cannot stop planning.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"
	clientrest "k8s.io/client-go/rest"
	ctrlconfig "sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/atedgimo/k8s-dencer/internal/api/agenttools"
	"github.com/atedgimo/k8s-dencer/internal/api/rest"
	"github.com/atedgimo/k8s-dencer/internal/auth"
	"github.com/atedgimo/k8s-dencer/internal/httpserver"
	"github.com/atedgimo/k8s-dencer/internal/pricing"
	"github.com/atedgimo/k8s-dencer/internal/store"
	sqlitestore "github.com/atedgimo/k8s-dencer/internal/store/sqlstore"
	"github.com/atedgimo/k8s-dencer/internal/telemetry"
	"github.com/atedgimo/k8s-dencer/internal/window"
)

// version is stamped at build time via -ldflags.
var version = "dev"

func main() {
	log := telemetry.NewLogger("ui-backend", env("LOG_LEVEL", "info"))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
	log.Info("ui-backend stopped")
}

func run(ctx context.Context, log *slog.Logger) error {
	db, dbDesc, err := sqlitestore.OpenFromEnv(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	if err := db.Migrate(ctx); err != nil {
		return err
	}
	log.Info("plan store ready", "store", dbDesc)

	authCfg := authConfig()
	guard, err := buildGuard(authCfg, log)
	if err != nil {
		return err
	}

	api := rest.New(db, log, version, guard, authCfg.Describe())

	// The execute route exists only where an executor is deployed to claim the
	// work. Without one there is no endpoint at all rather than a disabled
	// one — a "not implemented" execute route is an invitation.
	//
	// ui-backend still holds no eviction permission: it writes a row, and the
	// executor, which is unreachable from the network, performs the drain.
	if boolEnv("EXECUTOR_ENABLED", false) {
		if !authCfg.Enabled {
			// Belt and braces with the chart's schema, which rejects the same
			// combination. A mutating endpoint must never be reachable without
			// authorization, however the binary was launched.
			return errExecutorWithoutAuth
		}
		api = api.WithExecution(db)
		log.Info("execution enabled", "route", "POST /api/v1/plans/{id}/execute")
	}

	health := &httpserver.Health{}
	mux := http.NewServeMux()
	health.Register(mux)
	telemetry.NewMetrics(telemetry.ComponentUIBackend).Register(mux)
	// A machine that struggles at 4,000 pod elements should be able to say so
	// without redeploying a different build.
	if lim := intEnv("GRAPH_POD_DETAIL_LIMIT", 0); lim != 0 {
		api = api.WithGraphPodDetailLimit(lim)
		log.Info("graph detail limit overridden", "podDetailLimit", lim)
	}
	if label := env("CLUSTER_LABEL", ""); label != "" {
		api = api.WithClusterLabel(label)
		log.Info("cluster label set", "label", label)
	}
	// Maintenance windows decide whether Red steps can run, and the UI has
	// been naming that precondition without any way to check it. The read is
	// already granted: the chart's -read ClusterRole covers maintenancewindows
	// and is bound to ui-backend as well as the planner.
	if cfg, err := clusterConfig(); err != nil {
		log.Warn("no cluster config; maintenance window state will be unavailable", "error", err)
	} else if wr, err := window.NewReader(cfg, log); err != nil {
		log.Warn("could not build the maintenance window reader", "error", err)
	} else {
		api = api.WithWindows(wr)
	}

	// Prices come from the operator or not at all. A malformed table is a
	// configuration error worth saying out loud, but not worth refusing to
	// serve over: the ledger falls back to capacity, which is still true.
	if raw := env("PRICING", ""); raw != "" {
		var t pricing.Table
		if err := json.Unmarshal([]byte(raw), &t); err != nil {
			log.Error("PRICING is not valid JSON; the ledger will show capacity only", "error", err)
		} else {
			api = api.WithPricing(t)
			log.Info("pricing configured", "currency", t.Currency, "machineTypes", len(t.PerHour))
		}
	}
	api.Routes(mux)

	// The Kagent agent reaches the same plan store over MCP. It is mounted on
	// the ui-backend rather than shipped as its own image because the agent
	// itself runs inside Kagent — architecture doc §5 — so all we owe it is a
	// tool endpoint.
	//
	// Whether a token is demanded here is separate from the rest of the API.
	// Kagent's RemoteMCPServer does not necessarily forward one, so the
	// primary control on this surface is the chart's NetworkPolicy, which
	// admits only the Kagent namespace. The surface stays read-only either way
	// — a test asserts it exposes exactly four read tools and no fifth.
	var mcpHandler http.Handler = agenttools.New(db, log, version).Handler()
	if boolEnv("AUTH_MCP_REQUIRE_TOKEN", false) {
		mcpHandler = guard.Require(auth.ReadPlans, mcpHandler)
	}
	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/mcp/", mcpHandler)

	// Change detection is a poll of the latest plan ID rather than an event
	// feed: the planner is a separate process reached only through the shared
	// volume. The ID is a content hash, so the check is a string comparison.
	go api.PollStore(ctx, duration(log, "POLL_INTERVAL", 5*time.Second))

	// The schema is migrated and the store is open, so the API can serve —
	// and it keeps being asked, rather than only being asked once. A store
	// that migrates cleanly at startup can still stop having the tables in it,
	// and a ui-backend that reports Ready while answering every request with
	// "relation does not exist" is worse than one that reports NotReady.
	//
	// ActiveRun is the right question: it is a real query against a table the
	// product needs, and ErrNotFound is its healthy answer on an idle cluster,
	// so a working store cannot fail this by being quiet.
	health.AddCheck("plan store", func(ctx context.Context) error {
		if _, err := db.ActiveRun(ctx); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		return nil
	})
	health.SetReady(true)
	log.Info("starting", "version", version, "database", dbDesc)

	return httpserver.Run(ctx, log, env("HTTP_ADDR", ":8080"), mux)
}

// clusterConfig resolves the in-cluster REST config once.
//
// Two callers need it — the TokenReview client that answers "who is this?"
// and the maintenance window reader — and resolving it twice means reading
// the same service-account files twice to get the same answer. Memoised
// rather than threaded through, because the alternative is reordering
// startup around a value neither caller owns.
var (
	clusterCfgOnce sync.Once
	clusterCfgVal  *clientrest.Config
	clusterCfgErr  error
)

func clusterConfig() (*clientrest.Config, error) {
	clusterCfgOnce.Do(func() { clusterCfgVal, clusterCfgErr = ctrlconfig.GetConfig() })
	return clusterCfgVal, clusterCfgErr
}

// authConfig reads the authentication configuration the chart supplies.
//
// Enabled defaults to true: a component that answers questions about where a
// cluster's capacity is should not be open by default, and from M10 the same
// service accepts execution requests.
func authConfig() auth.Config {
	return auth.Config{
		Enabled:            boolEnv("AUTH_ENABLED", true),
		AllowAnonymousRead: boolEnv("AUTH_ANONYMOUS_READ", false),
		Audiences:          listEnv("AUTH_AUDIENCES"),
		Namespace:          env("POD_NAMESPACE", ""),
		OperatorRoleName:   env("AUTH_OPERATOR_ROLE", ""),
		TokenCacheTTL:      auth.DefaultTokenCacheTTL,
		OIDC: auth.OIDCInfo{
			Enabled:   boolEnv("AUTH_OIDC_ENABLED", false),
			IssuerURL: env("AUTH_OIDC_ISSUER_URL", ""),
			ClientID:  env("AUTH_OIDC_CLIENT_ID", ""),
			Scopes:    listEnv("AUTH_OIDC_SCOPES"),
		},
		TrustedProxy: auth.TrustedProxy{
			Enabled:      boolEnv("AUTH_TRUSTED_PROXY_ENABLED", false),
			UserHeader:   env("AUTH_TRUSTED_PROXY_USER_HEADER", "X-Forwarded-User"),
			GroupsHeader: env("AUTH_TRUSTED_PROXY_GROUPS_HEADER", "X-Forwarded-Groups"),
		},
	}
}

// buildGuard wires the middleware. When auth is disabled no Kubernetes client
// is built at all, so the binary still runs outside a cluster for development.
func buildGuard(cfg auth.Config, log *slog.Logger) (*auth.Middleware, error) {
	if !cfg.Enabled {
		log.Warn("authentication is DISABLED; every caller is anonymous")
		return auth.NewMiddleware(nil, nil, cfg, log), nil
	}

	restCfg, err := clusterConfig()
	if err != nil {
		return nil, err
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}

	if cfg.TrustedProxy.Enabled {
		// Worth a loud line in the log: from here on, anything that can reach
		// this port can assert an identity, and only the NetworkPolicy stops it.
		log.Warn("trusting proxy-asserted identity headers",
			"userHeader", cfg.TrustedProxy.UserHeader,
			"note", "ui-backend must be reachable only through the auth proxy")
	}
	log.Info("authentication enabled",
		"namespace", cfg.Namespace,
		"anonymousRead", cfg.AllowAnonymousRead,
		"oidc", cfg.OIDC.Enabled)

	return auth.NewMiddleware(
		auth.NewAuthenticator(client, cfg),
		auth.NewAuthorizer(client, cfg.Namespace),
		cfg,
		log,
	), nil
}

// errExecutorWithoutAuth refuses to start rather than serve an unauthenticated
// endpoint that can drain nodes. Failing loudly beats running in a state no
// operator would have chosen on purpose.
var errExecutorWithoutAuth = errors.New(
	"EXECUTOR_ENABLED is set but AUTH_ENABLED is false; " +
		"an execute endpoint must never be reachable without authorization")

func env(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

// boolEnv reads a boolean setting. An unparseable value takes the fallback,
// which for security settings is the safe end: a typo in AUTH_ENABLED leaves
// authentication on rather than silently opening the API.
// intEnv reads an integer setting. A malformed value is refused rather than
// silently defaulted: a typo'd limit that quietly reverts is worse than one
// that fails loudly at startup.
func intEnv(key string, fallback int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		panic(fmt.Sprintf("%s must be an integer, got %q", key, raw))
	}
	return n
}

func boolEnv(key string, fallback bool) bool {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback
	}
	return v
}

func listEnv(key string) []string {
	raw := os.Getenv(key)
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func duration(log *slog.Logger, key string, fallback time.Duration) time.Duration {
	raw, ok := os.LookupEnv(key)
	if !ok || raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		log.Warn("invalid duration, using default", "key", key, "value", raw, "default", fallback)
		return fallback
	}
	return d
}
