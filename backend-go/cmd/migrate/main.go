// Command migrate applies the embedded SQL migrations and bootstraps the
// admin account — same CLI surface as the Rust binary:
//
//	migrate                  apply all pending migrations
//	migrate --status         show applied/pending migrations only
//	migrate --mark-applied V record V as applied without running it
//	migrate --dsn URL        override $DATABASE_URL
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/joho/godotenv"

	"khmer-ai-cs-go/internal/db"
	"khmer-ai-cs-go/internal/migrations"
)

func main() {
	status := flag.Bool("status", false, "show applied/pending migrations only")
	markApplied := flag.String("mark-applied", "", "record a version as applied without running it")
	dsn := flag.String("dsn", "", "override $DATABASE_URL")
	flag.Parse()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	_ = godotenv.Load()
	databaseURL := *dsn
	if databaseURL == "" {
		databaseURL = os.Getenv("DATABASE_URL")
	}
	if databaseURL == "" {
		logger.Error("DATABASE_URL is required")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := db.Connect(ctx, databaseURL)
	if err != nil {
		logger.Error("postgres unavailable", "error", err.Error())
		os.Exit(1)
	}
	defer pool.Close()

	files, err := migrations.Files()
	if err != nil {
		logger.Error("read migrations", "error", err.Error())
		os.Exit(1)
	}

	if *status {
		applied, err := migrations.Applied(ctx, pool)
		if err != nil {
			logger.Error("query applied migrations", "error", err.Error())
			os.Exit(1)
		}
		pending := 0
		for _, f := range files {
			mark := "pending"
			if applied[f[0]] {
				mark = "applied"
			} else {
				pending++
			}
			fmt.Printf("%-45s %s\n", f[0], mark)
		}
		fmt.Printf("\n%d applied, %d pending\n", len(files)-pending, pending)
		return
	}

	if *markApplied != "" {
		if err := migrations.MarkApplied(ctx, pool, *markApplied); err != nil {
			logger.Error("mark applied", "error", err.Error())
			os.Exit(1)
		}
		fmt.Printf("marked %s as applied\n", *markApplied)
		return
	}

	applied, err := migrations.Run(ctx, pool)
	if err != nil {
		logger.Error("migration failed", "error", err.Error())
		os.Exit(1)
	}
	for _, name := range applied {
		fmt.Printf("→ Applying %s …\n  ✓ applied\n", name)
	}
	fmt.Printf("✓ All migrations applied (%d file(s)).\n", len(applied))

	if err := migrations.EnsureBootstrapAdmin(ctx, pool, os.Getenv("INITIAL_ADMIN_PASSWORD")); err != nil {
		logger.Error("bootstrap admin failed", "error", err.Error())
		os.Exit(1)
	}
	logger.Info("bootstrap admin ensured")
}
