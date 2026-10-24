package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"strings"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/migrations"
)

// migrationLock is the key of the advisory lock that serializes the
// migrations of several tasks starting at once.
const migrationLock = 4501_2026_14

// Migrate applies the embedded migrations that are not applied yet, in order,
// each in its own transaction, and records them in schema_migrations. Several
// tasks may call it at once: an advisory lock lets one apply them and the
// others find them done. Running it again changes nothing.
func Migrate(ctx context.Context, db *sql.DB) error {
	return migrate(ctx, db, migrations.FS)
}

func migrate(ctx context.Context, db *sql.DB, files fs.FS) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migrations: get a connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrationLock); err != nil {
		return fmt.Errorf("migrations: take the lock: %w", err)
	}
	defer func() {
		// A fresh context: the lock must be released even if ctx ended.
		_, _ = conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLock)
	}()

	if _, err := conn.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    TEXT        PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("migrations: create schema_migrations: %w", err)
	}

	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return fmt.Errorf("migrations: list the files: %w", err)
	}
	sort.Strings(names)
	for _, name := range names {
		version := strings.TrimSuffix(name, ".sql")
		var applied bool
		if err := conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`,
			version).Scan(&applied); err != nil {
			return fmt.Errorf("migrations: check %s: %w", version, err)
		}
		if applied {
			continue
		}
		body, err := fs.ReadFile(files, name)
		if err != nil {
			return fmt.Errorf("migrations: read %s: %w", name, err)
		}
		if err := apply(ctx, conn, version, string(body)); err != nil {
			return err
		}
		slog.InfoContext(ctx, "database migration applied", "version", version)
	}
	return nil
}

func apply(ctx context.Context, conn *sql.Conn, version, body string) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrations: begin %s: %w", version, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, body); err != nil {
		return fmt.Errorf("migrations: apply %s: %w", version, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
		return fmt.Errorf("migrations: record %s: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("migrations: commit %s: %w", version, err)
	}
	return nil
}
