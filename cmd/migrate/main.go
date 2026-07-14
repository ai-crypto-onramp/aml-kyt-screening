// Command migrate applies or reverts the embedded database migrations for
// aml-kyt-screening. It reuses internal/store.Migrate / MigrateDown.
//
// Usage:
//
//	migrate --up     apply all pending up-migrations (reads DB_URL)
//	migrate --down   revert the latest applied migration (reads DB_URL)
//
// Run with `go run ./cmd/migrate --up` (local dev) or `make migrate-up`.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"time"

	_ "github.com/lib/pq"

	"github.com/ai-crypto-onramp/aml-kyt-screening/internal/store"
)

func main() {
	up := flag.Bool("up", false, "apply all pending up-migrations")
	down := flag.Bool("down", false, "revert the latest applied migration")
	flag.Parse()
	if !*up && !*down {
		fmt.Fprintln(os.Stderr, "usage: migrate [--up|--down]")
		os.Exit(2)
	}

	dsn := os.Getenv("DB_URL")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "DB_URL is required")
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := sql.Open("postgres", dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open db:", err)
		os.Exit(1)
	}
	defer db.Close()
	if err := db.PingContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "ping db:", err)
		os.Exit(1)
	}

	switch {
	case *up:
		if err := store.Migrate(ctx, db); err != nil {
			fmt.Fprintln(os.Stderr, "migrate up:", err)
			os.Exit(1)
		}
		fmt.Println("migrations applied")
	case *down:
		if err := store.MigrateDown(ctx, db); err != nil {
			fmt.Fprintln(os.Stderr, "migrate down:", err)
			os.Exit(1)
		}
		fmt.Println("latest migration reverted")
	}
}