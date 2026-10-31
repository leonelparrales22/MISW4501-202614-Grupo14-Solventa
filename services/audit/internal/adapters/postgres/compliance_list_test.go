package postgres_test

import (
	"context"
	"io/fs"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/migrations"
)

// HU-W4.2-01: the migration of the indexes of the list and how the database
// runs the queries of the list.

func migration0006(t *testing.T) string {
	t.Helper()
	body, err := fs.ReadFile(migrations.FS, "0006_compliance_list_indexes.sql")
	require.NoError(t, err)
	return string(body)
}

func TestTheIndexMigrationOnlyCreatesIndexesAndRunsTwice(t *testing.T) {
	db := newDB(t)
	body := migration0006(t)

	var statements []string
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			statements = append(statements, line)
		}
	}
	code := strings.ToUpper(strings.Join(statements, "\n"))
	for _, word := range []string{"CREATE TABLE", "ALTER ", "DROP ", "UPDATE ", "DELETE ", "INSERT ", "TRIGGER", "FUNCTION"} {
		assert.NotContains(t, code, word, "the migration only creates indexes")
	}
	assert.Equal(t, 3, strings.Count(code, "CREATE INDEX IF NOT EXISTS"))
	_, err := db.ExecContext(context.Background(), body)
	require.NoError(t, err, "applied again over the migrated database")
	_, err = db.ExecContext(context.Background(), body)
	require.NoError(t, err, "and once more")

	assert.Equal(t, 1, count(t, db, `SELECT count(*) FROM pg_extension WHERE extname = 'pg_trgm'`))
	for _, index := range []string{"consent_record_compliance_order_idx", "consent_event_consent_revoked_idx", "consent_record_holder_name_trgm_idx"} {
		assert.Equal(t, 1, count(t, db, `SELECT count(*) FROM pg_indexes WHERE indexname = $1`, index), index)
	}
}

func TestTheTrigramIndexIsOnTheExpressionTheListSearches(t *testing.T) {
	assert.Contains(t, migration0006(t), postgres.ComplianceFoldSQL("holder_full_name"),
		"the planner only uses the index when the expression is the same")
}

// The service connects in AWS as the owner of its database, which is not a
// superuser (db-init of modules/platform). pg_trgm is a trusted extension, so
// that owner can create it: the migrations run as such a role here too.
func TestTheMigrationsRunAsTheOwnerOfTheDatabaseWithoutSuperuser(t *testing.T) {
	newDB(t) // starts the container
	ctx := context.Background()
	admin, err := postgres.Open(ctx, adminURL)
	require.NoError(t, err)
	defer func() { _ = admin.Close() }()
	role, name := "owner_"+randomHex(t, 4), "t_"+randomHex(t, 8)
	for _, stmt := range []string{
		"CREATE ROLE " + role + " LOGIN PASSWORD 'owner-password' NOSUPERUSER NOCREATEDB NOCREATEROLE",
		"CREATE DATABASE " + name + " OWNER " + role,
		"REVOKE ALL ON DATABASE " + name + " FROM PUBLIC",
	} {
		_, err := admin.ExecContext(ctx, stmt)
		require.NoError(t, err, stmt)
	}
	u, err := url.Parse(adminURL)
	require.NoError(t, err)
	u.User = url.UserPassword(role, "owner-password")
	u.Path = "/" + name

	db, err := postgres.Open(ctx, u.String())
	require.NoError(t, err)
	defer func() { _ = db.Close() }()
	var super bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&super))
	require.False(t, super)

	require.NoError(t, postgres.Migrate(ctx, db), "the owner creates pg_trgm and the indexes")
	assert.Equal(t, 1, count(t, db, `SELECT count(*) FROM pg_extension WHERE extname = 'pg_trgm'`))
	_, err = postgres.NewComplianceListStore(db).ListForCompliance(ctx, domain.ComplianceQuery{Page: 1, PageSize: 6}, time.Now())
	require.NoError(t, err)
}

func TestTheListOnlyReads(t *testing.T) {
	f := newEqFixture(t)
	f.seedBorders()
	before := count(t, f.db, `SELECT (SELECT count(*) FROM consent_record) + (SELECT count(*) FROM consent_event)`)

	_, err := f.store.ListForCompliance(context.Background(), domain.ComplianceQuery{Status: domain.StatusRevoked, Page: 1, PageSize: 6}, eqNow)

	require.NoError(t, err)
	assert.Equal(t, before, count(t, f.db, `SELECT (SELECT count(*) FROM consent_record) + (SELECT count(*) FROM consent_event)`))
}

func TestTheListAnswersAPageAfterTheLastWithTheTotal(t *testing.T) {
	f := newEqFixture(t)
	f.seedBorders()

	got, err := f.store.ListForCompliance(context.Background(), domain.ComplianceQuery{Page: 40, PageSize: 6}, eqNow)

	require.NoError(t, err)
	assert.Empty(t, got.Rows)
	assert.NotNil(t, got.Rows)
	assert.Equal(t, len(f.ids), got.TotalItems)
	assert.Equal(t, []string{"aliado-banco-1", "aliado-banco-2", "aliado-coop"}, got.EntityCodes)
}

func TestTheListReportsAFailureOfTheDatabase(t *testing.T) {
	db := newDB(t)
	store := postgres.NewComplianceListStore(db)
	require.NoError(t, db.Close())

	_, err := store.ListForCompliance(context.Background(), domain.ComplianceQuery{Page: 1, PageSize: 6}, eqNow)

	require.Error(t, err)
}

// volume writes n consents in SQL (one in ten revoked) and refreshes the
// statistics, so the planner sees a table of a realistic shape.
func volume(t *testing.T, f *eqFixture, n int) {
	t.Helper()
	ctx := context.Background()
	_, err := f.db.ExecContext(ctx, `INSERT INTO consent_record (consent_id, customer_id, holder_document_id, holder_full_name,
			terms_version, scope, purpose, channel, partner_id, application_id, user_agent, granted_at)
		SELECT 'cns-v-' || lpad(i::text, 6, '0'), 'cust-v-' || i, (1000000000 + i)::text,
			(ARRAY['Ana María Pérez', 'José Ñuñez', 'Luis Gómez', 'Sofía Ríos', 'Martín Ibáñez'])[1 + i % 5] || ' ' || i,
			'v1', '[]'::jsonb, 'risk_profiling_mortgage_life_offer', 'embedded',
			(ARRAY['aliado-banco-1', 'aliado-banco-2', 'aliado-coop'])[1 + i % 3], 'app-v-' || i, '',
			$1::timestamptz - i * interval '97 minutes'
		FROM generate_series(1, $2::int) AS i`, eqNow, n)
	require.NoError(t, err)
	_, err = f.db.ExecContext(ctx, `INSERT INTO consent_event (event_id, consent_id, status, occurred_at, channel, details)
		SELECT 'ev-v-' || r.consent_id, r.consent_id, 'revoked', r.granted_at + interval '1 day', 'mobile_app',
			jsonb_build_object('entities', jsonb_build_array(jsonb_build_object('code', r.partner_id,
				'deadline_at', to_char((r.granted_at + interval '1 day 5 minutes') AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'))))
		FROM consent_record r WHERE r.consent_id LIKE 'cns-v-%' AND right(r.consent_id, 1) = '0'`)
	require.NoError(t, err)
	_, err = f.db.ExecContext(ctx, `ANALYZE consent_record; ANALYZE consent_event`)
	require.NoError(t, err)
}

func explain(t *testing.T, f *eqFixture, q domain.ComplianceQuery) string {
	t.Helper()
	query, args := postgres.CompliancePageSQL(q, eqNow)
	rows, err := f.db.QueryContext(context.Background(), "EXPLAIN "+query, args...)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var lines []string
	for rows.Next() {
		var line string
		require.NoError(t, rows.Scan(&line))
		lines = append(lines, line)
	}
	require.NoError(t, rows.Err())
	return strings.Join(lines, "\n")
}

// The plans of the queries by status, by days and by name over 50 000
// consents use the indexes of migration 0006. The plans are logged (go test
// -v -run ExplainTheListQueries) for the record; the times are not a result.
func TestExplainTheListQueries(t *testing.T) {
	f := newEqFixture(t)
	volume(t, f, 50000)
	day, err := domain.ComplianceListRequest{GrantedFrom: "2026-10-20", GrantedTo: "2026-10-20", PageSize: "6"}.Validate()
	require.NoError(t, err)

	cases := []struct {
		name  string
		q     domain.ComplianceQuery
		index string
	}{
		{name: "by status", q: domain.ComplianceQuery{Status: domain.StatusRevoked, Page: 1, PageSize: 6}, index: "consent_event_consent_revoked_idx"},
		{name: "by days", q: day, index: "consent_record_compliance_order_idx"},
		{name: "by name", q: domain.ComplianceQuery{Search: domain.NewComplianceSearch("ibanez 43214"), Page: 1, PageSize: 6}, index: "consent_record_holder_name_trgm_idx"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := explain(t, f, tc.q)
			t.Logf("EXPLAIN %s:\n%s", tc.name, plan)
			assert.Contains(t, plan, tc.index)
		})
	}
}
