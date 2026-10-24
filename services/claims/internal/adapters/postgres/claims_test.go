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
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The filed claim in PostgreSQL (HU-M2.2-01). It is what makes the filing
// survive a redeploy of the service.

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

// newDB returns a migrated, empty database for the test. It skips when
// Docker is not available.
func newDB(t *testing.T) *sql.DB {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	containerOnce.Do(func() {
		container, containerErr = tcpostgres.Run(ctx, "postgres:16-alpine",
			tcpostgres.WithDatabase("claims"), tcpostgres.WithUsername("claims"),
			tcpostgres.WithPassword("claims"),
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

	db, err := postgres.Open(ctx, strings.Replace(adminURL, "/claims?", "/"+name+"?", 1))
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

var filedAt = time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)

func claim(id, subject, key string) domain.Claim {
	return domain.Claim{
		ID:           id,
		Subject:      subject,
		FilingKey:    key,
		PolicyID:     "AU-2025-118204",
		IncidentType: domain.IncidentCollision,
		OccurredAt:   time.Date(2026, 10, 20, 23, 30, 0, 0, time.UTC),
		Place:        "Calle 80 con Carrera 15, Bogotá",
		Description:  "Choque por alcance en el semáforo.",
		Details:      map[string]string{"third_party": "yes"},
		Status:       domain.StatusActive,
		FiledAt:      filedAt,
	}
}

func TestTheFilingIsStoredAndReadBack(t *testing.T) {
	s := postgres.NewClaimStore(newDB(t))

	stored, created, err := s.Create(context.Background(), claim("cl-1", "sub-carolina", "draft-1"))

	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "SIN-2026-00001", stored.FilingNumber)

	again, created, err := s.Create(context.Background(), claim("cl-1", "sub-carolina", "draft-1"))
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, "Calle 80 con Carrera 15, Bogotá", again.Place)
	assert.Equal(t, "Choque por alcance en el semáforo.", again.Description)
	assert.Equal(t, domain.IncidentCollision, again.IncidentType)
	assert.Equal(t, "yes", again.Details["third_party"])
	assert.Equal(t, filedAt, again.FiledAt)
	assert.Equal(t, time.Date(2026, 10, 20, 23, 30, 0, 0, time.UTC), again.OccurredAt)
}

// A retry after a lost response finds the case instead of filing again.
func TestTheSameKeyDoesNotFileTwice(t *testing.T) {
	s := postgres.NewClaimStore(newDB(t))
	first, _, err := s.Create(context.Background(), claim("cl-1", "sub-carolina", "draft-1"))
	require.NoError(t, err)

	second, created, err := s.Create(context.Background(), claim("cl-2", "sub-carolina", "draft-1"))

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, second.ID, "it is the same case, not the one that was retried")
	assert.Equal(t, first.FilingNumber, second.FilingNumber)
}

func TestTheSameKeyWithAnotherFilingIsRefused(t *testing.T) {
	s := postgres.NewClaimStore(newDB(t))
	_, _, err := s.Create(context.Background(), claim("cl-1", "sub-carolina", "draft-1"))
	require.NoError(t, err)

	other := claim("cl-2", "sub-carolina", "draft-1")
	other.IncidentType = domain.IncidentTheft

	_, _, err = s.Create(context.Background(), other)

	assert.ErrorIs(t, err, ports.ErrFilingKeyReused)
}

// The key belongs to the person: two people may be filing their first draft.
func TestTheKeyIsPerInsured(t *testing.T) {
	s := postgres.NewClaimStore(newDB(t))

	first, _, err := s.Create(context.Background(), claim("cl-1", "sub-ana", "draft-1"))
	require.NoError(t, err)
	second, created, err := s.Create(context.Background(), claim("cl-2", "sub-luis", "draft-1"))

	require.NoError(t, err)
	assert.True(t, created)
	assert.NotEqual(t, first.FilingNumber, second.FilingNumber)
}

func TestEachFilingTakesItsOwnNumber(t *testing.T) {
	s := postgres.NewClaimStore(newDB(t))

	first, _, err := s.Create(context.Background(), claim("cl-1", "sub-carolina", "draft-1"))
	require.NoError(t, err)
	second, _, err := s.Create(context.Background(), claim("cl-2", "sub-carolina", "draft-2"))
	require.NoError(t, err)

	assert.Equal(t, "SIN-2026-00001", first.FilingNumber)
	assert.Equal(t, "SIN-2026-00002", second.FilingNumber)
}

// The phone may send the same filing twice at once when the network comes
// back: only one case may be created, and both calls must answer the same.
func TestTwoSimultaneousRetriesCreateOneCase(t *testing.T) {
	s := postgres.NewClaimStore(newDB(t))
	const attempts = 6

	var (
		wg      sync.WaitGroup
		creates atomic.Int32
		numbers sync.Map
	)
	wg.Add(attempts)
	for i := 0; i < attempts; i++ {
		go func(i int) {
			defer wg.Done()
			stored, created, err := s.Create(context.Background(),
				claim("cl-"+hex.EncodeToString([]byte{byte(i)}), "sub-carolina", "draft-1"))
			if err != nil {
				return
			}
			if created {
				creates.Add(1)
			}
			numbers.Store(stored.FilingNumber, true)
		}(i)
	}
	wg.Wait()

	assert.Equal(t, int32(1), creates.Load(), "only one call created the case")
	count := 0
	numbers.Range(func(_, _ any) bool { count++; return true })
	assert.Equal(t, 1, count, "every call answered with the same filing number")
}

func TestAFilingWithoutDetailsReadsBackEmpty(t *testing.T) {
	s := postgres.NewClaimStore(newDB(t))
	c := claim("cl-1", "sub-carolina", "draft-1")
	c.Details = nil

	_, _, err := s.Create(context.Background(), c)
	require.NoError(t, err)

	again, _, err := s.Create(context.Background(), c)
	require.NoError(t, err)
	assert.Empty(t, again.Details)
}
