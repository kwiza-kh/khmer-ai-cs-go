// Package migrations applies the embedded SQL migrations in lexical order —
// each in its own transaction, except a file containing CONCURRENTLY, which the
// server refuses inside a transaction block — and bootstraps the admin account:
// a faithful port of the Rust runner (itself a port of the original Go
// cmd/migrate).
package migrations

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"regexp"
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

// Run applies all pending migrations and returns the list of applied names.
// The simple query protocol is used so multi-statement DDL and DO blocks work.
func Run(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	// Serialise the whole run on one session-scoped advisory lock. Nothing did
	// this before, and since 075/077 a concurrent run is destructive rather than
	// merely wasteful: two cmd/migrate invocations (a redeploy racing an operator,
	// or two hosts against one database) both read Applied and both execute, so
	// run B's `DROP INDEX CONCURRENTLY IF EXISTS` can remove the index run A just
	// finished building — and if B is killed before its CREATE, the index stays
	// gone until a later run, with the version already recorded.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire migration lock connection: %w", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationsAdvisoryLockKey); err != nil {
		return nil, fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		// Detached: the caller's context may be cancelled (a failed migration),
		// and the lock must still be handed back on this connection.
		if _, err := conn.Exec(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationsAdvisoryLockKey); err != nil {
			slog.Default().Warn("migration advisory lock not released", "error", err.Error())
		}
	}()

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

// migrationsAdvisoryLockKey is arbitrary but fixed: every process that migrates
// this database must agree on it.
const migrationsAdvisoryLockKey = 741852963

func applyOne(ctx context.Context, pool *pgxpool.Pool, name, content string) error {
	// CONCURRENTLY is the one statement kind Postgres refuses inside a
	// transaction block, and the simple query protocol wraps a multi-statement
	// message in an implicit one. Such a file therefore goes statement by
	// statement, each in its own implicit transaction. That is safe only because
	// a migration that needs CONCURRENTLY is idempotent on its own terms
	// (IF [NOT] EXISTS): a failure part-way leaves earlier statements applied,
	// and the next run re-executes them harmlessly before retrying the failed
	// one. The version is recorded only after every statement succeeded.
	stmts := splitStatements(content)
	if needsConcurrentIndex(stmts) {
		for _, stmt := range stmts {
			if _, err := pool.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		_, err := pool.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", name)
		return err
	}

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

// needsConcurrentIndex reports whether any statement of a migration asks for a
// concurrent build. Comments are already stripped by splitStatements, so a file
// that merely mentions the word in a note is not affected. The match is anchored
// at the statement head: a raw substring test moved a file onto the
// statement-by-statement path (losing its all-or-nothing transaction) as soon as
// any statement merely contained the word — a help-text row or a column comment
// would have done it.
func needsConcurrentIndex(stmts []string) bool {
	for _, stmt := range stmts {
		if concurrentIndexStmt.MatchString(stmt) {
			return true
		}
	}
	return false
}

var concurrentIndexStmt = regexp.MustCompile(`(?is)^\s*(DROP|CREATE|REINDEX)\s+(UNIQUE\s+)?INDEX\s+CONCURRENTLY\b`)

// splitStatements splits migration SQL into individual statements, dropping
// comments and ignoring semicolons inside string literals, dollar-quoted blocks
// and comments. It is only consulted for the CONCURRENTLY path above; every
// other migration still reaches the server as one untouched multi-statement
// message.
func splitStatements(content string) []string {
	var (
		stmts    []string
		b        strings.Builder
		i, n     = 0, len(content)
		inSingle bool
		inDouble bool
		escapes  bool // inside E'...', where a backslash escapes the next byte
		dollar   string
	)
	emit := func() {
		if s := strings.TrimSpace(b.String()); s != "" {
			stmts = append(stmts, s)
		}
		b.Reset()
	}
	for i < n {
		c := content[i]
		switch {
		case inSingle:
			b.WriteByte(c)
			if c == '\\' && escapes && i+1 < n {
				b.WriteByte(content[i+1])
				i += 2
				continue
			}
			if c == '\'' {
				if i+1 < n && content[i+1] == '\'' {
					b.WriteByte('\'')
					i += 2
					continue
				}
				inSingle = false
			}
			i++
		case inDouble:
			b.WriteByte(c)
			if c == '"' {
				if i+1 < n && content[i+1] == '"' {
					b.WriteByte('"')
					i += 2
					continue
				}
				inDouble = false
			}
			i++
		case dollar != "":
			if c == '$' && strings.HasPrefix(content[i:], dollar) {
				b.WriteString(dollar)
				i += len(dollar)
				dollar = ""
				continue
			}
			b.WriteByte(c)
			i++
		case c == '-' && i+1 < n && content[i+1] == '-':
			for i < n && content[i] != '\n' {
				i++
			}
			b.WriteByte(' ')
		case c == '/' && i+1 < n && content[i+1] == '*':
			// Postgres block comments nest. i must be the outer index: a shadowed
			// copy here would leave the scanner on the same byte forever.
			depth := 1
			i += 2
			for i < n && depth > 0 {
				if strings.HasPrefix(content[i:], "/*") {
					depth++
					i += 2
					continue
				}
				if strings.HasPrefix(content[i:], "*/") {
					depth--
					i += 2
					continue
				}
				i++
			}
			b.WriteByte(' ')
		case c == '\'':
			escapes = i > 0 && (content[i-1] == 'e' || content[i-1] == 'E')
			inSingle = true
			b.WriteByte(c)
			i++
		case c == '"':
			inDouble = true
			b.WriteByte(c)
			i++
		case c == '$':
			if tag := dollarTagAt(content, i); tag != "" {
				dollar = tag
				b.WriteString(tag)
				i += len(tag)
				continue
			}
			b.WriteByte(c)
			i++
		case c == ';':
			emit()
			i++
		default:
			b.WriteByte(c)
			i++
		}
	}
	emit()
	return stmts
}

// dollarTagAt returns the dollar-quote tag starting at i ("$$", "$fn$"), or ""
// when the dollar sign starts something else such as a $1 parameter.
func dollarTagAt(s string, i int) string {
	j := i + 1
	for j < len(s) {
		c := s[j]
		digit := c >= '0' && c <= '9'
		if c != '_' && !digit && !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') {
			break
		}
		if digit && j == i+1 { // a tag cannot start with a digit ($1 is a parameter)
			break
		}
		j++
	}
	if j < len(s) && s[j] == '$' {
		return s[i : j+1]
	}
	return ""
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
				"INSERT INTO users (username, email, password_hash, role) VALUES ('admin', 'admin@mekongchat.com', $1, 'platform_admin')",
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
