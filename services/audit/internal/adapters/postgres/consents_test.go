package postgres_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// One PostgreSQL container for the package, started by the first test that
// needs it; every test gets its own database in it.
var (
	containerOnce sync.Once
	container     *tcpostgres.PostgresContainer
	adminURL      string
	containerErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if container != nil {
		_ = testcontainers.TerminateContainer(container)
	}
	os.Exit(code)
}

// requireDocker fails the test when REQUIRE_DOCKER=1 and Docker is not
// healthy, and skips it otherwise, so the certification run never passes
// without the tests that need Docker.
func requireDocker(t *testing.T) {
	t.Helper()
	provider, err := testcontainers.NewDockerProvider()
	if err == nil {
		defer func() { _ = provider.Close() }()
		err = provider.Health(context.Background())
	}
	if err == nil {
		return
	}
	if os.Getenv("REQUIRE_DOCKER") == "1" {
		t.Fatalf("Docker is required (REQUIRE_DOCKER=1) but is not healthy: %v", err)
	}
	t.Skipf("Docker is not available: %v", err)
}

// newDB returns a migrated, empty database for the test.
func newDB(t *testing.T) *sql.DB {
	t.Helper()
	requireDocker(t)
	ctx := context.Background()
	containerOnce.Do(func() {
		container, containerErr = tcpostgres.Run(ctx, "postgres:16-alpine",
			tcpostgres.WithDatabase("audit"), tcpostgres.WithUsername("audit"), tcpostgres.WithPassword("audit"),
			testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(2*time.Minute)))
		if containerErr == nil {
			adminURL, containerErr = container.ConnectionString(ctx, "sslmode=disable")
		}
	})
	require.NoError(t, containerErr, "start PostgreSQL")

	admin, err := postgres.Open(ctx, adminURL)
	require.NoError(t, err)
	defer func() { _ = admin.Close() }()
	name := "t_" + randomHex(t, 8)
	_, err = admin.ExecContext(ctx, "CREATE DATABASE "+name)
	require.NoError(t, err)

	db, err := postgres.Open(ctx, strings.Replace(adminURL, "/audit?", "/"+name+"?", 1))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, postgres.Migrate(ctx, db))
	return db
}

func randomHex(t *testing.T, n int) string {
	t.Helper()
	b := make([]byte, n)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return hex.EncodeToString(b)
}

func consent(id, customer, application string, grantedAt time.Time) domain.Consent {
	terms := domain.TermsV1()
	return domain.Consent{
		ID: id, CustomerID: customer, Holder: domain.Holder{DocumentID: "1020304050", FullName: "Ana Pérez"},
		TermsVersion: terms.Version, Scope: terms.Sources, Purpose: terms.Purpose, Channel: domain.ChannelEmbedded,
		PartnerID: "partner-1", ApplicationID: application, UserAgent: "UA", GrantedAt: grantedAt,
	}
}

var base = time.Date(2026, 10, 17, 12, 0, 0, 123456000, time.UTC)

func TestCreateStoresTheConsentAndReadsItBack(t *testing.T) {
	store := postgres.NewConsentStore(newDB(t))
	c := consent("cns-1", "cust-1", "app-1", base)

	stored, created, err := store.Create(context.Background(), c)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, c, stored)

	got, err := store.Get(context.Background(), "cns-1")
	require.NoError(t, err)
	assert.Equal(t, c, got, "the scope, the holder and the microsecond timestamp survive the round trip")
}

func TestCreateIsIdempotentByCustomerApplicationAndTerms(t *testing.T) {
	db := newDB(t)
	store := postgres.NewConsentStore(db)
	first := consent("cns-1", "cust-1", "app-1", base)
	_, created, err := store.Create(context.Background(), first)
	require.NoError(t, err)
	require.True(t, created)

	again, created, err := store.Create(context.Background(), consent("cns-2", "cust-1", "app-1", base.Add(time.Hour)))

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first, again, "the stored consent comes back, not the new candidate")
	var rows int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM consent_record`).Scan(&rows))
	assert.Equal(t, 1, rows)
}

func TestCreateConcurrentlyRecordsOneConsent(t *testing.T) {
	db := newDB(t)
	store := postgres.NewConsentStore(db)
	var createdCount atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := "cns-" + randomHex(t, 4)
			_, created, err := store.Create(context.Background(), consent(id, "cust-1", "app-1", base))
			assert.NoError(t, err)
			if created {
				createdCount.Add(1)
			}
		}()
	}
	wg.Wait()

	assert.EqualValues(t, 1, createdCount.Load())
	var rows int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM consent_record`).Scan(&rows))
	assert.Equal(t, 1, rows)
}

func TestSameCustomerCanConsentForAnotherApplicationOrTerms(t *testing.T) {
	db := newDB(t)
	store := postgres.NewConsentStore(db)
	_, created, err := store.Create(context.Background(), consent("cns-1", "cust-1", "app-1", base))
	require.NoError(t, err)
	require.True(t, created)
	_, created, err = store.Create(context.Background(), consent("cns-2", "cust-1", "app-2", base))
	require.NoError(t, err)
	assert.True(t, created, "another application")
	v2 := consent("cns-3", "cust-1", "app-1", base)
	v2.TermsVersion = "v2"
	_, created, err = store.Create(context.Background(), v2)
	require.NoError(t, err)
	assert.True(t, created, "another terms version")
}

func TestGetUnknownConsentIsNotFound(t *testing.T) {
	store := postgres.NewConsentStore(newDB(t))

	_, err := store.Get(context.Background(), "cns-missing")

	assert.ErrorIs(t, err, ports.ErrConsentNotFound)
}

func TestLatestByCustomerReturnsTheMostRecentlyGranted(t *testing.T) {
	store := postgres.NewConsentStore(newDB(t))
	ctx := context.Background()
	for _, c := range []domain.Consent{
		consent("cns-old", "cust-1", "app-1", base),
		consent("cns-new", "cust-1", "app-2", base.Add(48*time.Hour)),
		consent("cns-mid", "cust-1", "app-3", base.Add(24*time.Hour)),
		consent("cns-other", "cust-2", "app-1", base.Add(72*time.Hour)),
	} {
		_, _, err := store.Create(ctx, c)
		require.NoError(t, err)
	}

	got, err := store.LatestByCustomer(ctx, "cust-1")
	require.NoError(t, err)
	assert.Equal(t, "cns-new", got.ID)

	_, err = store.LatestByCustomer(ctx, "nobody")
	assert.ErrorIs(t, err, ports.ErrConsentNotFound)
}

func TestTheRecordIsImmutable(t *testing.T) {
	db := newDB(t)
	store := postgres.NewConsentStore(db)
	_, _, err := store.Create(context.Background(), consent("cns-1", "cust-1", "app-1", base))
	require.NoError(t, err)

	statements := map[string]string{
		"UPDATE":           `UPDATE consent_record SET holder_full_name = 'Someone Else' WHERE consent_id = 'cns-1'`,
		"UPDATE all":       `UPDATE consent_record SET granted_at = now()`,
		"DELETE":           `DELETE FROM consent_record WHERE consent_id = 'cns-1'`,
		"DELETE all":       `DELETE FROM consent_record`,
		"TRUNCATE":         `TRUNCATE consent_record`,
		"TRUNCATE CASCADE": `TRUNCATE consent_record CASCADE`,
	}
	for name, stmt := range statements {
		t.Run(name, func(t *testing.T) {
			_, err := db.Exec(stmt)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "append-only")
			assert.Contains(t, err.Error(), "23001", "restrict_violation")
		})
	}

	got, err := store.Get(context.Background(), "cns-1")
	require.NoError(t, err)
	assert.Equal(t, "Ana Pérez", got.Holder.FullName, "the record is intact")
}

func TestMigrateTwiceChangesNothing(t *testing.T) {
	db := newDB(t)

	require.NoError(t, postgres.Migrate(context.Background(), db))

	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestMigrateRunsOnceWhenSeveralTasksStartAtOnce(t *testing.T) {
	db := newDB(t)
	files := fstest.MapFS{"0002_probe.sql": {Data: []byte(`CREATE TABLE probe (n INT); INSERT INTO probe VALUES (1);`)}}
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, postgres.MigrateFS(context.Background(), db, files))
		}()
	}
	wg.Wait()

	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM probe`).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestMigrateReportsABrokenMigration(t *testing.T) {
	db := newDB(t)
	files := fstest.MapFS{"0003_broken.sql": {Data: []byte(`THIS IS NOT SQL`)}}

	err := postgres.MigrateFS(context.Background(), db, files)

	assert.ErrorContains(t, err, "0003_broken")
}

func TestOpenReportsAnUnreachableDatabase(t *testing.T) {
	_, err := postgres.Open(context.Background(), "postgres://u:p@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")

	assert.Error(t, err)
}
