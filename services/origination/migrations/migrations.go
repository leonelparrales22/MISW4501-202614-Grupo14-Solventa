// Package migrations holds the schema of origination's own PostgreSQL
// database as numbered SQL files (decision D-3 of HA-D02). They are embedded
// in the binary and applied in order when the service starts, under an
// advisory lock, by internal/adapters/postgres.Migrate.
package migrations

import "embed"

// FS holds the migration files, applied in lexical order of their names.
//
//go:embed *.sql
var FS embed.FS
