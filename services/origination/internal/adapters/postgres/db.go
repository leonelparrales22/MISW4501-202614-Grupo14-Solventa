// Package postgres holds the adapters over origination's own PostgreSQL
// database (one database per service, AGENTS.md §2). It uses database/sql
// with the pgx v5 driver and its default pool, as validated by EXP-D03 (no
// RDS Proxy). HA-D02 introduced it for the premium charges.
package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	// The pgx driver registers itself as "pgx" for database/sql.
	_ "github.com/jackc/pgx/v5/stdlib"
)

// pingTimeout bounds the first connection check when the service starts.
const pingTimeout = 10 * time.Second

// Open opens the database at url (a PostgreSQL URL or DSN) and checks that it
// answers.
func Open(ctx context.Context, url string) (*sql.DB, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, fmt.Errorf("open the database: %w", err)
	}
	pctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	if err := db.PingContext(pctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("reach the database: %w", err)
	}
	return db, nil
}
