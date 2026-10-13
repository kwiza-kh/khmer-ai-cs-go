// Package db creates the PostgreSQL connection pool. Schema is managed by the
// migrate command — the server never migrates.
package db

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/config"
)

// Connect builds the pool from DATABASE_URL with the same sizing knobs as the
// Rust backend (DATABASE_POOL_MAX/MIN), then pings to fail fast.
//
// It also bounds three failures a pool cannot survive on its own:
//
//   - ConnectTimeout: a black-holed database host fails the request instead of
//     holding a caller until the client gives up;
//   - statement_timeout: one runaway query cannot occupy one of the 30
//     connections forever. On a 30-connection pool a handful of stuck queries
//     is the whole server.
//   - idle_in_transaction_session_timeout: a transaction left open (a request
//     that died between Begin and Commit) is closed instead of pinning a
//     connection and its locks.
//
// The defaults are far above any request this server issues; each is
// overridable by env. ConnectForMigrations is the one caller that opts out of
// the statement timeout.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return connect(ctx, databaseURL, true)
}

// ConnectForMigrations is Connect without the statement timeout (unless
// DATABASE_URL itself carries statement_timeout, which pgx has already put into
// RuntimeParams by the time we get here — the opt-out is ours, not the
// operator's). A migration is the one workload allowed to run long: an index
// build or a backfill has no smaller unit to fail at, and a statement timeout
// would roll the whole file back and start it over on the next run.
func ConnectForMigrations(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	return connect(ctx, databaseURL, false)
}

func connect(ctx context.Context, databaseURL string, boundStatements bool) (*pgxpool.Pool, error) {
	max := min(max(config.EnvInt("DATABASE_POOL_MAX", 30), 5), 200)
	min := config.EnvInt("DATABASE_POOL_MIN", 0)
	if min > max {
		min = max
	}

	poolCfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid DATABASE_URL: %w", err)
	}
	poolCfg.MaxConns = int32(max)
	poolCfg.MinConns = int32(min)
	poolCfg.MaxConnLifetime = time.Hour
	poolCfg.MaxConnIdleTime = 5 * time.Minute
	// Only fill the connect deadline when the DSN did not set one: DATABASE_URL
	// may carry `?connect_timeout=5`, and pgx's own default (2 minutes) is longer
	// than anything this deployment wants. Unconditionally overwriting made the
	// DSN knob look like it worked while the pool still waited 10s — the same rule
	// the RuntimeParams below follow.
	if dsnConnectTimeout(databaseURL) == 0 {
		poolCfg.ConnConfig.ConnectTimeout = time.Duration(config.EnvInt("DATABASE_CONNECT_TIMEOUT_MS", 10000)) * time.Millisecond
	}
	if boundStatements {
		if poolCfg.ConnConfig.RuntimeParams == nil {
			poolCfg.ConnConfig.RuntimeParams = map[string]string{}
		} // Sent in the startup packet, so every connection in the pool carries
		// them — including the ones a future caller creates without knowing.
		//
		// Only filled when the DSN did not already set the key: DATABASE_URL can
		// carry `?statement_timeout=…`, and an operator who puts it there means
		// it. Overwriting unconditionally made that knob look like it worked.
		if _, ok := poolCfg.ConnConfig.RuntimeParams["statement_timeout"]; !ok {
			poolCfg.ConnConfig.RuntimeParams["statement_timeout"] =
				strconv.Itoa(config.EnvInt("DATABASE_STATEMENT_TIMEOUT_MS", 120000))
		}
		if _, ok := poolCfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"]; !ok {
			poolCfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] =
				strconv.Itoa(config.EnvInt("DATABASE_IDLE_TX_TIMEOUT_MS", 120000))
		}
	} else {
		// The migration connection gets a lock_timeout instead of a statement
		// timeout: an index build or backfill may legitimately run for minutes,
		// but waiting on another transaction's lock may not. cmd/migrate gives the
		// whole run one five-minute budget, so blocking on a lock for minutes left
		// the later files truncated — and a DROP/CREATE INDEX CONCURRENTLY pair
		// that times out mid-pair is the one case a re-run has to repair.
		if poolCfg.ConnConfig.RuntimeParams == nil {
			poolCfg.ConnConfig.RuntimeParams = map[string]string{}
		}
		if _, ok := poolCfg.ConnConfig.RuntimeParams["lock_timeout"]; !ok {
			poolCfg.ConnConfig.RuntimeParams["lock_timeout"] = "5s"
		}
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to postgres: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres ping failed: %w", err)
	}
	return pool, nil
}

// dsnConnectTimeout returns the DSN's connect_timeout in seconds, or 0 when the
// DSN does not set it (or sets something unparseable, in which case the env knob
// applies).
func dsnConnectTimeout(databaseURL string) int {
	u, err := url.Parse(databaseURL)
	if err != nil || u.RawQuery == "" {
		return 0
	}
	n, err := strconv.Atoi(u.Query().Get("connect_timeout"))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}
