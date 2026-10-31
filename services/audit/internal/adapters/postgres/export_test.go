package postgres

// MigrateFS applies the migrations of a custom file system (tests only).
var MigrateFS = migrate

// The statements of the list of the compliance portal (tests only: EXPLAIN
// and the expression of the trigram index of migration 0006).
var (
	CompliancePageSQL = pageSQL
	ComplianceFoldSQL = foldSQL
)
