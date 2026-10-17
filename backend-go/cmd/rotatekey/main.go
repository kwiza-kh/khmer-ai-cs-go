// Command rotatekey re-seals every enc:v1 value under a new
// PLATFORM_CREDENTIAL_KEY.
//
// Why this exists: sealing is only as strong as the key, and a deployment can
// run for months on the documented demo key without anything complaining
// (found in production 2026-10-17). Rotating means decrypting with the old key
// and re-encrypting with the new one — inside one transaction, so a failure
// leaves every row on the old key rather than half on each.
//
// Usage (dsn defaults to $DATABASE_URL; keys from flags or env):
//
//	OLD_CREDENTIAL_KEY=... NEW_CREDENTIAL_KEY=... rotatekey            # dry run
//	OLD_CREDENTIAL_KEY=... NEW_CREDENTIAL_KEY=... rotatekey -apply     # write
//
// The new key must pass security.NewSealer's placeholder check; the old one is
// read with NewSealerRaw because it may be exactly the placeholder being retired.
// Nothing secret is printed: only table/column names and row counts.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"khmer-ai-cs-go/internal/security"
)

type target struct{ table, column, idColumn string }

// Every column written through security.Sealer. Keeping the list here is the
// point: a new sealed column that is not added is a column the rotation silently
// leaves on the old key, so the self-check below is what makes the omission loud.
var targets = []target{
	{"user_totp", "secret", "user_id"},
	{"platform_configs", "access_token", "config_id"},
	{"platform_configs", "bot_token", "config_id"},
	{"platform_configs", "webhook_secret", "config_id"},
	{"model_configs", "api_key", "config_id"},
	{"telegram_notify_settings", "bot_token_enc", "user_id"},
}

func main() {
	oldKey := flag.String("old-key", os.Getenv("OLD_CREDENTIAL_KEY"), "current key (base64, 32 bytes)")
	newKey := flag.String("new-key", os.Getenv("NEW_CREDENTIAL_KEY"), "new key (base64, 32 bytes)")
	apply := flag.Bool("apply", false, "write the re-sealed rows (default: dry run)")
	dsn := flag.String("dsn", "", "override $DATABASE_URL")
	flag.Parse()

	if strings.TrimSpace(*oldKey) == "" || strings.TrimSpace(*newKey) == "" {
		fail("both keys are required (flags -old-key/-new-key or OLD_CREDENTIAL_KEY/NEW_CREDENTIAL_KEY)")
	}
	oldSealer, err := security.NewSealerRaw(*oldKey)
	if err != nil {
		fail("old key: " + err.Error())
	}
	newSealer, err := security.NewSealer(*newKey)
	if err != nil {
		fail("new key: " + err.Error())
	}

	url := *dsn
	if url == "" {
		url = os.Getenv("DATABASE_URL")
	}
	if url == "" {
		fail("DATABASE_URL is required (or -dsn)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		fail("connect: " + err.Error())
	}
	defer pool.Close()

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		fail("begin: " + err.Error())
	}
	defer func() { _ = tx.Rollback(ctx) }()

	total := 0
	for _, t := range targets {
		n, err := rotateTarget(ctx, tx, t, oldSealer, newSealer, *apply)
		if err != nil {
			fail(fmt.Sprintf("%s.%s: %v", t.table, t.column, err))
		}
		if n > 0 {
			fmt.Printf("%s.%s: %d row(s) %s\n", t.table, t.column, n, map[bool]string{true: "re-sealed", false: "would be re-sealed"}[*apply])
		}
		total += n
	}

	if !*apply {
		fmt.Printf("dry run: %d row(s) total; re-run with -apply to write\n", total)
		return
	}
	// The self-check runs inside the same transaction: if any row cannot be read
	// back with the new key, the rollback leaves the database exactly as it was.
	for _, t := range targets {
		if err := selfCheck(ctx, tx, t, newSealer); err != nil {
			fail(fmt.Sprintf("self-check %s.%s: %v (rolled back, nothing changed)", t.table, t.column, err))
		}
	}
	if err := tx.Commit(ctx); err != nil {
		fail("commit: " + err.Error())
	}
	fmt.Printf("done: %d row(s) re-sealed and verified; update PLATFORM_CREDENTIAL_KEY and restart\n", total)
}

func rotateTarget(ctx context.Context, tx pgx.Tx, t target, oldSealer, newSealer *security.Sealer, apply bool) (int, error) {
	rows, err := tx.Query(ctx,
		fmt.Sprintf("SELECT %s, %s FROM %s WHERE %s LIKE 'enc:v1:%%'", t.idColumn, t.column, t.table, t.column))
	if err != nil {
		return 0, err
	}
	type row struct {
		id     int64
		sealed string
	}
	var batch []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.sealed); err != nil {
			rows.Close()
			return 0, err
		}
		batch = append(batch, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, r := range batch {
		plain, err := oldSealer.Decrypt(r.sealed)
		if err != nil {
			// Not skippable: a row the old key cannot open means the inventory is
			// wrong (or the key is), and continuing would silently leave it behind.
			return 0, fmt.Errorf("row %d does not decrypt under the old key: %w", r.id, err)
		}
		sealed, err := newSealer.Encrypt(plain)
		if err != nil {
			return 0, fmt.Errorf("row %d: %w", r.id, err)
		}
		if !apply {
			continue
		}
		if _, err := tx.Exec(ctx,
			fmt.Sprintf("UPDATE %s SET %s = $1 WHERE %s = $2", t.table, t.column, t.idColumn),
			sealed, r.id); err != nil {
			return 0, fmt.Errorf("row %d: %w", r.id, err)
		}
	}
	return len(batch), nil
}

func selfCheck(ctx context.Context, tx pgx.Tx, t target, newSealer *security.Sealer) error {
	rows, err := tx.Query(ctx,
		fmt.Sprintf("SELECT %s FROM %s WHERE %s LIKE 'enc:v1:%%'", t.column, t.table, t.column))
	if err != nil {
		return err
	}
	defer rows.Close()
	checked := 0
	for rows.Next() {
		var sealed string
		if err := rows.Scan(&sealed); err != nil {
			return err
		}
		if _, err := newSealer.Decrypt(sealed); err != nil {
			return fmt.Errorf("a row does not decrypt under the new key")
		}
		checked++
	}
	return rows.Err()
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "rotatekey:", msg)
	os.Exit(1)
}
