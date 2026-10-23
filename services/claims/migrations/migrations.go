// Package migrations holds the schema of the claims service's own PostgreSQL
// database as numbered SQL files (one database per service, AGENTS.md §2).
// They are embedded in the binary and applied in order when the service
// starts, under an advisory lock, by internal/adapters/postgres.Migrate.
package migrations

import "embed"

// FS holds the migration files, applied in lexical order of their names.
//
//go:embed *.sql
var FS embed.FS
