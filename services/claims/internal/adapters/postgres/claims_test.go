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

// The evidence of the case (HU-M2.1-01) travels with it: the file stays in
// the evidence store and here lives what lets anyone find and check it.

func conEvidencia(c domain.Claim) domain.Claim {
	altitud := 2601.0
	c.Evidence = []domain.Evidence{
		{
			ObjectKey:       "evidence/" + c.Subject + "/" + strings.Repeat("a", 64) + ".jpg",
			Kind:            domain.KindPhoto,
			CapturedAt:      time.Date(2026, 10, 20, 23, 32, 14, 0, time.UTC),
			Latitude:        4.7712,
			Longitude:       -74.0452,
			AltitudeM:       &altitud,
			AccuracyM:       8,
			LocationTrusted: true,
			SHA256:          strings.Repeat("a", 64),
			ByteSize:        312044,
		},
		{
			ObjectKey:       "evidence/" + c.Subject + "/" + strings.Repeat("b", 64) + ".mp4",
			Kind:            domain.KindVideo,
			CapturedAt:      time.Date(2026, 10, 20, 23, 33, 40, 0, time.UTC),
			Latitude:        4.7712,
			Longitude:       -74.0452,
			AccuracyM:       120,
			LocationTrusted: false,
			SHA256:          strings.Repeat("b", 64),
			ByteSize:        18_000_000,
			DurationS:       22,
		},
	}
	return c
}

func TestTheEvidenceIsStoredWithTheCase(t *testing.T) {
	s := postgres.NewClaimStore(newDB(t))
	ctx := context.Background()
	_, created, err := s.Create(ctx, conEvidencia(claim("cl-1", "sub-carolina", "draft-1")))
	require.NoError(t, err)
	require.True(t, created)

	// The retry reads the case back, evidence included.
	again, created, err := s.Create(ctx, conEvidencia(claim("cl-1", "sub-carolina", "draft-1")))

	require.NoError(t, err)
	assert.False(t, created)
	require.Len(t, again.Evidence, 2)

	foto := again.Evidence[0]
	assert.Equal(t, domain.KindPhoto, foto.Kind, "the order of capture is kept")
	assert.Equal(t, 4.7712, foto.Latitude)
	require.NotNil(t, foto.AltitudeM)
	assert.Equal(t, 2601.0, *foto.AltitudeM)
	assert.True(t, foto.LocationTrusted)
	assert.Equal(t, time.Date(2026, 10, 20, 23, 32, 14, 0, time.UTC), foto.CapturedAt)

	video := again.Evidence[1]
	assert.Equal(t, domain.KindVideo, video.Kind)
	assert.Nil(t, video.AltitudeM, "the video came without altitude")
	assert.False(t, video.LocationTrusted, "it was captured with a poor precision")
	assert.Equal(t, 22, video.DurationS)
	assert.Equal(t, int64(18_000_000), video.ByteSize)
}

// The evidence is part of what was filed: the same key with other
// photographs is another filing, not a retry of this one.
func TestTheSameKeyWithOtherEvidenceIsAnotherFiling(t *testing.T) {
	s := postgres.NewClaimStore(newDB(t))
	ctx := context.Background()
	_, _, err := s.Create(ctx, conEvidencia(claim("cl-1", "sub-carolina", "draft-1")))
	require.NoError(t, err)

	otra := conEvidencia(claim("cl-1", "sub-carolina", "draft-1"))
	otra.Evidence[0].ObjectKey = "evidence/sub-carolina/" + strings.Repeat("c", 64) + ".jpg"

	_, _, err = s.Create(ctx, otra)

	assert.ErrorIs(t, err, ports.ErrFilingKeyReused)
}

// A filing that fails leaves nothing behind: neither the case nor its
// evidence.
func TestAFilingThatFailsLeavesNoEvidenceBehind(t *testing.T) {
	db := newDB(t)
	s := postgres.NewClaimStore(db)
	roto := conEvidencia(claim("cl-1", "sub-carolina", "draft-1"))
	roto.Evidence[0].Kind = domain.EvidenceKind(strings.Repeat("x", 300))

	_, _, err := s.Create(context.Background(), roto)
	require.Error(t, err)

	var casos, evidencias int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM claim`).Scan(&casos))
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM claim_evidence`).Scan(&evidencias))
	assert.Zero(t, casos)
	assert.Zero(t, evidencias)
}
