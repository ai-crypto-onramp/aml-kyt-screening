package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration represents a single versioned SQL migration pair.
type Migration struct {
	Version int
	Up      string
	Down    string
}

// LoadMigrations reads embedded migration files and returns them sorted by version.
func LoadMigrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}

	byVersion := make(map[int]*Migration)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}

		var version int
		var kind string
		_, err = fmt.Sscanf(name, "%d_%s", &version, &kind)
		if err != nil {
			return nil, fmt.Errorf("parse migration name %s: %w", name, err)
		}

		m, ok := byVersion[version]
		if !ok {
			m = &Migration{Version: version}
			byVersion[version] = m
		}
		if strings.HasSuffix(name, ".up.sql") {
			m.Up = string(body)
		} else if strings.HasSuffix(name, ".down.sql") {
			m.Down = string(body)
		}
	}

	out := make([]Migration, 0, len(byVersion))
	for _, m := range byVersion {
		if m.Up == "" {
			return nil, fmt.Errorf("migration %d missing up script", m.Version)
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Migrate applies all pending up-migrations to db. It is idempotent: an internal
// schema_migrations table tracks applied versions.
func Migrate(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}

	migrations, err := LoadMigrations()
	if err != nil {
		return err
	}

	for _, m := range migrations {
		applied := false
		row := db.QueryRowContext(ctx, `SELECT 1 FROM schema_migrations WHERE version = $1`, m.Version)
		switch err := row.Scan(new(int)); err {
		case nil:
			applied = true
		case sql.ErrNoRows:
		default:
			return fmt.Errorf("check migration %d: %w", m.Version, err)
		}
		if applied {
			continue
		}

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx for migration %d: %w", m.Version, err)
		}
		if _, err := tx.ExecContext(ctx, m.Up); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %d up: %w", m.Version, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, m.Version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %d: %w", m.Version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", m.Version, err)
		}
	}
	return nil
}

// MigrateDown reverts the latest applied migration (the highest version
// recorded in schema_migrations). It is idempotent: if no migrations are
// recorded as applied it is a no-op. Repeated calls step back one
// migration at a time.
func MigrateDown(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}

	var version int
	row := db.QueryRowContext(ctx, `SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1`)
	switch err := row.Scan(&version); err {
	case sql.ErrNoRows:
		return nil
	case nil:
	default:
		return fmt.Errorf("find latest migration: %w", err)
	}

	migrations, err := LoadMigrations()
	if err != nil {
		return err
	}

	var down string
	for _, m := range migrations {
		if m.Version == version {
			down = m.Down
			break
		}
	}
	if down == "" {
		return fmt.Errorf("no down script for migration %d", version)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx for migration %d down: %w", version, err)
	}
	if _, err := tx.ExecContext(ctx, down); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("revert migration %d down: %w", version, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM schema_migrations WHERE version = $1`, version); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete migration %d record: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d down: %w", version, err)
	}
	return nil
}