// Package migrations applies the embedded SQL migrations in lexical order,
// each in its own transaction, and bootstraps the admin account — a faithful
// port of the Rust runner (itself a port of the original Go cmd/migrate).
package migrations

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

//go:embed migrations/*.sql
var embedded embed.FS

// InsecureDefaultAdminHash ships in migration 001 and must be replaced before
// going live. Identical constant to the Rust/Go implementations.
const InsecureDefaultAdminHash = "$2a$10$KxIv1cHF3IW9EpS0eDQ6NuZY5AlIRmGOxqbVuYR8YTrI5rMaOZj4q"

// File pairs (name, contents) in lexical order.
func Files() ([][2]string, error) {
	entries, err := embedded.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	var files [][2]string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		content, err := embedded.ReadFile("migrations/" + e.Name())
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", e.Name(), err)
		}
		files = append(files, [2]string{e.Name(), string(content)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i][0] < files[j][0] })
	return files, nil
}

// ensureSchemaTable creates the tracking table when missing.
func ensureSchemaTable(ctx context.Context, pool *pgxpool.Pool) error {
	_, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version VARCHAR(255) PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`)
	return err
}

// Applied returns the set of applied migration versions.
func Applied(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error) {
	if err := ensureSchemaTable(ctx, pool); err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	applied := make(map[string]bool)
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// Run applies all pending migrations (each in its own transaction) and returns
// the list of applied names. The simple query protocol is used so
// multi-statement DDL and DO blocks work.
func Run(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	applied, err := Applied(ctx, pool)
	if err != nil {
		return nil, err
	}
	files, err := Files()
	if err != nil {
		return nil, err
	}
	var newly []string
	for _, f := range files {
		if applied[f[0]] {
			continue
		}
		if err := applyOne(ctx, pool, f[0], f[1]); err != nil {
			return newly, fmt.Errorf("apply %s: %w", f[0], err)
		}
		newly = append(newly, f[0])
	}
	return newly, nil
}

func applyOne(ctx context.Context, pool *pgxpool.Pool, name, content string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, content); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", name); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MarkApplied records a version as applied without running it (bootstrap).
func MarkApplied(ctx context.Context, pool *pgxpool.Pool, version string) error {
	if err := ensureSchemaTable(ctx, pool); err != nil {
		return err
	}
	_, err := pool.Exec(ctx,
		"INSERT INTO schema_migrations (version) VALUES ($1) ON CONFLICT (version) DO NOTHING", version)
	return err
}

// EnsureBootstrapAdmin mirrors the Rust behaviour exactly:
//   - admin exists with a non-default hash → no-op (except role promotion)
//   - otherwise initialPassword (≥12 chars) is required; INSERT when the row
//     is missing, UPDATE (re-hash) when it still uses the default hash.
func EnsureBootstrapAdmin(ctx context.Context, pool *pgxpool.Pool, initialPassword string) error {
	var hashPtr *string // NULL when the admin signed up through Google only
	var role string
	err := pool.QueryRow(ctx, "SELECT password_hash, role::text FROM users WHERE username = 'admin'").
		Scan(&hashPtr, &role)
	hash := ""
	if hashPtr != nil {
		hash = *hashPtr
	}
	needsCreate := false
	passwordOK := true
	if err != nil {
		if err.Error() == "no rows in result set" || strings.Contains(err.Error(), "no rows") {
			needsCreate, passwordOK = true, false
		} else {
			return fmt.Errorf("look up admin user: %w", err)
		}
	} else if hash == InsecureDefaultAdminHash {
		passwordOK = false
	}

	if !passwordOK {
		if len(initialPassword) < 12 {
			return fmt.Errorf("INITIAL_ADMIN_PASSWORD must be at least 12 characters when no admin exists or the default password is in use")
		}
		newHash, err := bcrypt.GenerateFromPassword([]byte(initialPassword), 12)
		if err != nil {
			return fmt.Errorf("hash bootstrap admin password: %w", err)
		}
		if needsCreate {
			// Fresh DB: create the bootstrap admin directly as platform_admin.
			if _, err := pool.Exec(ctx,
				"INSERT INTO users (username, email, password_hash, role) VALUES ('admin', 'admin@khmer-ai-cs.com', $1, 'platform_admin')",
				newHash); err != nil {
				return fmt.Errorf("create admin user: %w", err)
			}
		} else {
			if _, err := pool.Exec(ctx, "UPDATE users SET password_hash = $1 WHERE username = 'admin'", newHash); err != nil {
				return fmt.Errorf("rotate admin password: %w", err)
			}
		}
	}

	// Guarantee the elevated role even when an older migrator created 'admin'.
	if _, err := pool.Exec(ctx,
		"UPDATE users SET role = 'platform_admin' WHERE username = 'admin' AND role = 'admin'"); err != nil {
		return fmt.Errorf("promote admin to platform_admin: %w", err)
	}
	return nil
}
