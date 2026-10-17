package postgres_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
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

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
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

// newDB returns a migrated, empty database for the test. It skips when
// Docker is not available (decision P5 of HA-D02).
func newDB(t *testing.T) *sql.DB {
	t.Helper()
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx := context.Background()
	containerOnce.Do(func() {
		container, containerErr = tcpostgres.Run(ctx, "postgres:16-alpine",
			tcpostgres.WithDatabase("origination"), tcpostgres.WithUsername("origination"), tcpostgres.WithPassword("origination"),
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

	db, err := postgres.Open(ctx, strings.Replace(adminURL, "/origination?", "/"+name+"?", 1))
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

var t0 = time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)

func charge(id string) domain.Charge {
	acc := domain.Acceptance{ID: id, OfferID: "demo-active-" + id, CustomerID: "cust-1", Channel: domain.ChannelWeb,
		AcceptedAt: t0.Add(-time.Minute)}
	offer := domain.Offer{ID: "demo-active-" + id, CustomerID: "cust-1", Premium: 89_500, Currency: "COP",
		Applicant: domain.Applicant{FullName: "Ana Pérez", DocumentID: "demo-document", Email: "ana@example.com"}}
	terms := domain.CoverageTerms{InsuredAmount: 250_000_000, TermMonths: 120, Coverages: []domain.Coverage{
		{Code: domain.CoverageDeath, InsuredAmount: 250_000_000},
		{Code: domain.CoverageDisability, InsuredAmount: 250_000_000},
	}}
	return domain.NewCharge(acc, offer, terms, t0, t0.Add(24*time.Hour))
}

func create(t *testing.T, s *postgres.ChargeStore, id string) domain.Charge {
	t.Helper()
	c, created, err := s.CreatePending(context.Background(), charge(id))
	require.NoError(t, err)
	require.True(t, created)
	return c
}

func claim(t *testing.T, s *postgres.ChargeStore, id string, now time.Time) domain.Charge {
	t.Helper()
	c, ok, err := s.Claim(context.Background(), id, now, 30*time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	return c
}

func TestCreatePendingStoresTheWholeSnapshotOnce(t *testing.T) {
	s := postgres.NewChargeStore(newDB(t))
	want := charge("acc-1")

	got, created, err := s.CreatePending(context.Background(), want)
	require.NoError(t, err)
	assert.True(t, created)
	again, created, err := s.CreatePending(context.Background(), want)
	require.NoError(t, err)
	assert.False(t, created, "idempotent by acceptance")

	read, err := s.Get(context.Background(), "acc-1")
	require.NoError(t, err)
	for _, c := range []domain.Charge{got, again, read} {
		assert.Equal(t, want, c, "the snapshot round-trips, coverages included")
	}
}

func TestConcurrentCreatesStoreOneCharge(t *testing.T) {
	s := postgres.NewChargeStore(newDB(t))
	var wg sync.WaitGroup
	var created atomic.Int32
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := s.CreatePending(context.Background(), charge("acc-1"))
			assert.NoError(t, err)
			if ok {
				created.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, created.Load())
}

func TestTheKeyTheAcceptanceAndThePolicyAreUnique(t *testing.T) {
	db := newDB(t)
	s := postgres.NewChargeStore(db)
	create(t, s, "acc-1")

	sameKey := charge("acc-2")
	sameKey.IdempotencyKey = "cobro-acc-1"
	_, created, err := s.CreatePending(context.Background(), sameKey)
	assert.False(t, created, "the key belongs to another acceptance")
	assert.ErrorIs(t, err, ports.ErrChargeNotFound, "nothing was stored for acc-2")

	_, err = db.ExecContext(context.Background(),
		`INSERT INTO charge_attempt (acceptance_id, idempotency_key, offer_id, customer_id, channel, accepted_at, holder_full_name,
		 holder_document_id, holder_email, amount_minor, currency, insured_amount_minor, term_months, coverages, status,
		 window_started_at, policy_id, created_at, updated_at)
		 SELECT 'acc-3', 'cobro-acc-3', offer_id, customer_id, channel, accepted_at, holder_full_name, holder_document_id,
		 holder_email, amount_minor, currency, insured_amount_minor, term_months, coverages, 'confirmed', window_started_at,
		 'pol-x', created_at, updated_at FROM charge_attempt WHERE acceptance_id = 'acc-1'`)
	require.NoError(t, err)
	_, err = db.ExecContext(context.Background(), `UPDATE charge_attempt SET policy_id = 'pol-x' WHERE acceptance_id = 'acc-1'`)
	assert.Error(t, err, "one policy per charge")
}

func TestOnlyOneOfTwentyConcurrentClaimsWinsInEveryRepetition(t *testing.T) {
	s := postgres.NewChargeStore(newDB(t))
	for rep := 0; rep < 10; rep++ {
		id := fmt.Sprintf("acc-%d", rep)
		create(t, s, id)
		var winners atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				_, ok, err := s.Claim(context.Background(), id, t0, 30*time.Second)
				assert.NoError(t, err)
				if ok {
					winners.Add(1)
				}
			}()
		}
		close(start)
		wg.Wait()
		assert.EqualValues(t, 1, winners.Load(), "repetition %d: exactly one winner (EC-D02 §3.5)", rep)
	}
}

func TestAClaimNeedsADueChargeOrAnExpiredLease(t *testing.T) {
	s := postgres.NewChargeStore(newDB(t))
	create(t, s, "acc-1")

	_, ok, err := s.Claim(context.Background(), "acc-1", t0.Add(-time.Second), 30*time.Second)
	require.NoError(t, err)
	assert.False(t, ok, "not due yet")

	c := claim(t, s, "acc-1", t0)
	assert.Equal(t, domain.ChargeInProgress, c.Status)
	assert.Equal(t, t0.Add(30*time.Second), c.ClaimedUntil)
	assert.Equal(t, t0, c.LastAttemptAt)

	_, ok, err = s.Claim(context.Background(), "acc-1", t0.Add(10*time.Second), 30*time.Second)
	require.NoError(t, err)
	assert.False(t, ok, "the lease is valid")

	again := claim(t, s, "acc-1", t0.Add(31*time.Second))
	assert.Equal(t, t0.Add(61*time.Second), again.ClaimedUntil, "an expired lease is claimed again (D-8)")

	_, err = s.Confirm(context.Background(), c, t0.Add(32*time.Second), idempotency.Outcome{Reference: "ch-stale"})
	assert.ErrorIs(t, err, idempotency.ErrClaimLost, "the stale claim cannot record a result")

	_, ok, err = s.Claim(context.Background(), "acc-404", t0, time.Second)
	require.NoError(t, err)
	assert.False(t, ok, "unknown")
}

func TestRescheduleKeepsTheKeyAndCountsTheFailure(t *testing.T) {
	s := postgres.NewChargeStore(newDB(t))
	create(t, s, "acc-1")
	c := claim(t, s, "acc-1", t0)

	got, err := s.Reschedule(context.Background(), c, t0.Add(time.Second), t0.Add(1500*time.Millisecond), "timeout")

	require.NoError(t, err)
	assert.Equal(t, domain.ChargePending, got.Status)
	assert.Equal(t, 1, got.Attempts)
	assert.Equal(t, t0.Add(1500*time.Millisecond), got.NextAttemptAt)
	assert.True(t, got.ClaimedUntil.IsZero())
	assert.Equal(t, t0.Add(time.Second), got.LastFailureAt)
	assert.Equal(t, "timeout", got.LastError)
	assert.Equal(t, "cobro-acc-1", got.IdempotencyKey)

	_, err = s.Reschedule(context.Background(), c, t0, t0, "again")
	assert.ErrorIs(t, err, idempotency.ErrClaimLost)
}

func TestFailForTheWindowAndForARejection(t *testing.T) {
	s := postgres.NewChargeStore(newDB(t))
	for id, reason := range map[string]idempotency.FailureReason{
		"acc-w": idempotency.ReasonWindowExpired, "acc-r": idempotency.ReasonRejected,
	} {
		create(t, s, id)
		c := claim(t, s, id, t0)
		got, err := s.Fail(context.Background(), c, t0.Add(time.Second), reason, "cause")
		require.NoError(t, err)
		assert.Equal(t, domain.ChargeFailed, got.Status)
		assert.Equal(t, domain.ChargeFailureReasonFor(reason), got.FailureReason)
		assert.True(t, got.NextAttemptAt.IsZero())
		assert.Equal(t, 1, got.Attempts)
	}
	_, ok, err := s.Claim(context.Background(), "acc-w", t0.Add(time.Hour), time.Second)
	require.NoError(t, err)
	assert.False(t, ok, "a failed charge is never claimed")
}

func TestConfirmSetsThePolicyAndKeepsTheClaimToPublish(t *testing.T) {
	s := postgres.NewChargeStore(newDB(t))
	create(t, s, "acc-1")
	c := claim(t, s, "acc-1", t0)
	at := t0.Add(2 * time.Second)

	got, err := s.Confirm(context.Background(), c, at, idempotency.Outcome{Reference: "ch-1"})

	require.NoError(t, err)
	assert.Equal(t, c.Confirmed(at, "ch-1"), got, "the stored activation is the one the domain computes")
	assert.Equal(t, "pol-acc-1", got.PolicyID)
	assert.Equal(t, at.AddDate(0, 120, 0), got.CoverageEnd)
	assert.Equal(t, c.ClaimedUntil, got.ClaimedUntil, "the claim is still held")
	assert.NoError(t, got.ActivationEvent().Validate())

	ids, err := s.ListActivationsDue(context.Background(), at.Add(time.Second), 10)
	require.NoError(t, err)
	assert.Empty(t, ids, "held by the confirming worker")

	pub, err := s.MarkActivationPublished(context.Background(), got, at.Add(time.Second))
	require.NoError(t, err)
	assert.Equal(t, at.Add(time.Second), pub.ActivationPublishedAt)
	assert.True(t, pub.ClaimedUntil.IsZero())
	assert.True(t, pub.ActivationNextAttemptAt.IsZero())

	_, err = s.MarkActivationPublished(context.Background(), got, at.Add(2*time.Second))
	assert.ErrorIs(t, err, idempotency.ErrClaimLost, "published once")
}

func TestOneOfManyConcurrentPublicationClaimsWins(t *testing.T) {
	s := postgres.NewChargeStore(newDB(t))
	create(t, s, "acc-1")
	c := claim(t, s, "acc-1", t0)
	c, err := s.Confirm(context.Background(), c, t0.Add(time.Second), idempotency.Outcome{})
	require.NoError(t, err)
	c, err = s.RescheduleActivation(context.Background(), c, t0.Add(time.Second), t0.Add(2*time.Second), "consumer down")
	require.NoError(t, err)
	assert.Equal(t, 1, c.ActivationAttempts)
	assert.Equal(t, "consumer down", c.ActivationLastError)
	assert.True(t, c.ClaimedUntil.IsZero(), "released")

	ids, err := s.ListActivationsDue(context.Background(), t0.Add(time.Second), 10)
	require.NoError(t, err)
	assert.Empty(t, ids, "not due yet")
	ids, err = s.ListActivationsDue(context.Background(), t0.Add(2*time.Second), 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"acc-1"}, ids)

	var winners atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok, err := s.ClaimActivation(context.Background(), "acc-1", t0.Add(3*time.Second), 30*time.Second)
			assert.NoError(t, err)
			if ok {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	assert.EqualValues(t, 1, winners.Load())

	_, err = s.RescheduleActivation(context.Background(), c, t0, t0, "stale")
	assert.ErrorIs(t, err, idempotency.ErrClaimLost)
}

func TestResumeIsConditionalOnAFailedCharge(t *testing.T) {
	s := postgres.NewChargeStore(newDB(t))
	create(t, s, "acc-1")
	c := claim(t, s, "acc-1", t0)
	_, err := s.Fail(context.Background(), c, t0.Add(time.Second), idempotency.ReasonWindowExpired, "expired")
	require.NoError(t, err)
	at := t0.Add(time.Hour)

	var wins atomic.Int32
	var notResumable atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Resume(context.Background(), "acc-1", domain.ChargeResumption{
				By: fmt.Sprintf("ops-%d", i), Note: "gateway back", At: at, WindowEndsAt: at.Add(24 * time.Hour),
			})
			switch {
			case err == nil:
				wins.Add(1)
			case errors.Is(err, ports.ErrChargeNotResumable):
				notResumable.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	assert.EqualValues(t, 1, wins.Load(), "two resumptions at once: one wins")
	assert.EqualValues(t, 9, notResumable.Load())

	got, err := s.Get(context.Background(), "acc-1")
	require.NoError(t, err)
	assert.Equal(t, domain.ChargePending, got.Status)
	assert.Equal(t, "cobro-acc-1", got.IdempotencyKey)
	assert.Equal(t, 0, got.Attempts)
	assert.Equal(t, at, got.NextAttemptAt)
	assert.Equal(t, at, got.WindowStartedAt)
	assert.Equal(t, at.Add(24*time.Hour), got.WindowEndsAt)
	assert.Empty(t, got.FailureReason)
	assert.Equal(t, 1, got.ResumeCount)
	assert.Equal(t, "gateway back", got.ResumeNote)
	assert.Equal(t, at, got.LastResumedAt)
	assert.True(t, strings.HasPrefix(got.LastResumedBy, "ops-"))

	_, err = s.Resume(context.Background(), "acc-404", domain.ChargeResumption{At: at})
	assert.ErrorIs(t, err, ports.ErrChargeNotFound)
}

func TestListsOfDueChargesAndExceptions(t *testing.T) {
	s := postgres.NewChargeStore(newDB(t))
	for _, id := range []string{"acc-a", "acc-b", "acc-c", "acc-d"} {
		create(t, s, id)
	}
	held := claim(t, s, "acc-b", t0) // in progress, lease until t0+30 s
	for i, id := range []string{"acc-c", "acc-d"} {
		c := claim(t, s, id, t0)
		_, err := s.Fail(context.Background(), c, t0.Add(time.Duration(i+1)*time.Minute), idempotency.ReasonWindowExpired, "x")
		require.NoError(t, err)
	}

	due, err := s.ListDue(context.Background(), t0, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"acc-a"}, due)
	due, err = s.ListDue(context.Background(), held.ClaimedUntil.Add(time.Second), 10)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"acc-a", "acc-b"}, due, "an expired lease is due again")
	due, err = s.ListDue(context.Background(), t0.Add(time.Hour), 1)
	require.NoError(t, err)
	assert.Len(t, due, 1, "limit")

	ex, err := s.ListExceptions(context.Background(), 10)
	require.NoError(t, err)
	require.Len(t, ex, 2)
	assert.Equal(t, "acc-d", ex[0].AcceptanceID, "the latest failure first")
	assert.Equal(t, "acc-c", ex[1].AcceptanceID)
	ex, err = s.ListExceptions(context.Background(), 1)
	require.NoError(t, err)
	assert.Len(t, ex, 1)
}

func TestGetUnknownCharge(t *testing.T) {
	s := postgres.NewChargeStore(newDB(t))
	_, err := s.Get(context.Background(), "acc-404")
	assert.ErrorIs(t, err, ports.ErrChargeNotFound)
}

func TestMigrateIsRepeatableAndSerializedAcrossTasks(t *testing.T) {
	db := newDB(t) // already migrated once
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			assert.NoError(t, postgres.Migrate(context.Background(), db))
		}()
	}
	wg.Wait()
	var n int
	require.NoError(t, db.QueryRowContext(context.Background(), `SELECT count(*) FROM schema_migrations`).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestABrokenMigrationIsNotRecorded(t *testing.T) {
	db := newDB(t)
	err := postgres.MigrateFS(context.Background(), db, fstest.MapFS{"9999_broken.sql": {Data: []byte("CREATE TABLE (")}})
	assert.Error(t, err)
	var n int
	require.NoError(t, db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM schema_migrations WHERE version = '9999_broken'`).Scan(&n))
	assert.Zero(t, n)
}

func TestOpenFailsForAnUnreachableDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := postgres.Open(ctx, "postgres://nobody:nothing@127.0.0.1:1/none?sslmode=disable&connect_timeout=1")
	assert.Error(t, err)
	_, err = postgres.Open(ctx, "://not a url")
	assert.Error(t, err)
}
