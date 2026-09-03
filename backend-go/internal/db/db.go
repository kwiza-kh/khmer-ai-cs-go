// Package db creates the PostgreSQL connection pool. Schema is managed by the
// migrate command — the server never migrates.
package db

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Connect builds the pool from DATABASE_URL with the same sizing knobs as the
// Rust backend (DATABASE_POOL_MAX/MIN), then pings to fail fast.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	max := clamp(envInt("DATABASE_POOL_MAX", 30), 5, 200)
	min := envInt("DATABASE_POOL_MIN", 0)
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

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return fallback
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
