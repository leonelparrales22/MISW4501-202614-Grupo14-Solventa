package main

import (
	"bytes"
	"context"
	"log"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/migrations"
)

// captureLog sends the standard log (and slog, which writes through it) to a
// buffer for the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	t.Cleanup(func() { log.SetOutput(prev) })
	return &buf
}

// CF-7 / D-2 a: without DATABASE_URL every store is in memory, and one notice
// says so with these exact words.
func TestNewStoresWithoutADatabaseKeepsEverythingInMemoryAndSaysSo(t *testing.T) {
	buf := captureLog(t)

	st, err := newStores(context.Background(), "", systemClock{})

	require.NoError(t, err)
	assert.Nil(t, st.db)
	assert.IsType(t, &memory.PersonalizedOfferStore{}, st.offers)
	assert.IsType(t, &memory.AcceptanceStore{}, st.acceptances)
	assert.IsType(t, &memory.ReviewQueueStub{}, st.reviews)
	assert.IsType(t, &memory.CustomerStore{}, st.customers)
	assert.Same(t, st.demo, st.adjusted, "the adjusted offers live in the stub, in memory")
	assert.Contains(t, buf.String(), "DATABASE_URL is not set: personalized and adjusted offers, acceptances, "+
		"the advisor inbox and its decisions, customers, the advisor quotes and their formalizations, and the outbox of domain events are kept in memory and lost on restart")
	assert.IsType(t, &memory.QuoteStore{}, st.quotes)
	assert.IsType(t, &memory.FormalizationStore{}, st.formalized)
	assert.IsType(t, &memory.ConsentChallengeStore{}, st.consentCodes)
	assert.IsType(t, &memory.Outbox{}, st.outbox)
	assert.Same(t, st.customers.(*memory.CustomerStore).Outbox(), st.outbox, "the customers write to the outbox the relay reads")
	assert.Equal(t, 1, strings.Count(buf.String(), "DATABASE_URL is not set"), "a single notice")
	assert.NotContains(t, buf.String(), "PostgreSQL")
	assert.NotNil(t, st.router())
}

func TestNewStoresFailsWithAnUnreachableDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	_, err := newStores(ctx, "postgres://o:x@127.0.0.1:1/origination?sslmode=disable&connect_timeout=1", systemClock{})

	assert.Error(t, err)
}

// requireDocker fails with REQUIRE_DOCKER=1 when Docker is not healthy, and
// skips otherwise.
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

// CF-7 / D-4 a: with DATABASE_URL a single pool is opened and migrated once and
// every store is in PostgreSQL over it; a customer saved without a payment
// gateway survives another newStores over the same database (a restart).
func TestNewStoresPutsEverythingInPostgresOverASinglePool(t *testing.T) {
	requireDocker(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pg, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("origination"), tcpostgres.WithUsername("origination"), tcpostgres.WithPassword("origination"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(2*time.Minute)))
	testcontainers.CleanupContainer(t, pg)
	require.NoError(t, err)
	dbURL, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	files, err := migrations.FS.ReadDir(".")
	require.NoError(t, err)
	buf := captureLog(t)

	first, err := newStores(ctx, dbURL, systemClock{})
	require.NoError(t, err)

	require.NotNil(t, first.db)
	assert.IsType(t, &postgres.OfferStore{}, first.offers)
	assert.IsType(t, &postgres.AdjustedOfferStore{}, first.adjusted)
	assert.IsType(t, &postgres.AcceptanceStore{}, first.acceptances)
	assert.IsType(t, &postgres.ReviewStore{}, first.reviews)
	assert.IsType(t, &postgres.CustomerStore{}, first.customers)
	assert.IsType(t, &postgres.OutboxStore{}, first.outbox)
	assert.Contains(t, buf.String(), "origination data in PostgreSQL: personalized and adjusted offers, acceptances, "+
		"the advisor inbox and its decisions, customers, the advisor quotes and their formalizations, and the outbox of domain events (HU-W1.2-03, HU-W1.3-01, HU-W1.1-02, HU-M1.1-01, HU-M1.3-01, HU-W1.1-01, HU-W1.1-03)")
	assert.IsType(t, &postgres.QuoteStore{}, first.quotes)
	assert.IsType(t, &postgres.FormalizationStore{}, first.formalized)
	assert.IsType(t, &postgres.ConsentChallengeStore{}, first.consentCodes)
	assert.NotContains(t, buf.String(), "is not set")
	assert.Equal(t, len(files), strings.Count(buf.String(), "database migration applied"), "every migration applied once")

	now := time.Now().UTC().Truncate(time.Microsecond)
	saved, err := first.customers.Save(ctx, domain.Customer{
		ID: "cus-1", DocumentID: "1020304050", FullName: "Ana María Pérez", Email: "ana@example.com",
		BirthDate: time.Date(1985, 3, 14, 0, 0, 0, 0, time.UTC), CreatedAt: now, UpdatedAt: now,
	})
	require.NoError(t, err)
	_, created, err := first.acceptances.Create(ctx, domain.Acceptance{ID: "acc-1", OfferID: "demo-review-1", CustomerID: "cust-1",
		Channel: domain.ChannelWeb, AcceptedAt: now, Status: domain.AcceptanceRegistered, NextAttemptAt: now, UpdatedAt: now})
	require.NoError(t, err)
	require.True(t, created)

	// A restart: another process opens the same database.
	buf.Reset()
	second, err := newStores(ctx, dbURL, systemClock{})
	require.NoError(t, err)
	defer func() { _ = second.db.Close() }()
	assert.Zero(t, strings.Count(buf.String(), "database migration applied"), "nothing left to migrate")

	got, err := second.customers.GetByDocument(ctx, "1020304050")
	require.NoError(t, err)
	assert.Equal(t, saved, got, "the customer survives without a payment gateway")
	a, err := second.acceptances.Get(ctx, "acc-1")
	require.NoError(t, err)
	assert.Equal(t, domain.AcceptanceRegistered, a.Status)

	// One pool: closing it stops every store of that process.
	require.NoError(t, first.db.Close())
	_, err = first.customers.GetByDocument(ctx, "1020304050")
	assert.Error(t, err)
	_, err = first.acceptances.Get(ctx, "acc-1")
	assert.Error(t, err)
	_, _, err = first.reviews.List(ctx, domain.ReviewFilter{})
	assert.Error(t, err)
	_, err = first.offers.Get(ctx, "off-1")
	assert.Error(t, err)
	_, err = first.adjusted.Get(ctx, "adj-acc-1")
	assert.Error(t, err)
}
