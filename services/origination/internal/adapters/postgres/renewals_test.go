package postgres_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

var policyEnds = time.Date(2026, 10, 2, 23, 59, 0, 123_456_000, time.UTC)

func samplePolicy(id, customer string, end time.Time) domain.Policy {
	return domain.Policy{
		ID: id, AcceptanceID: "acc-" + id, CustomerID: customer,
		Holder:  domain.Applicant{FullName: "Carolina Pérez", DocumentID: "1032456789", Email: "carolina@example.com"},
		Premium: 142_300.5, Currency: "COP", InsuredAmount: 68_000_000,
		Coverages:     []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 68_000_000}},
		CoverageStart: end.AddDate(-1, 0, 0), CoverageEnd: end,
	}
}

func sampleRenewal(p domain.Policy, status domain.RenewalStatus) domain.Renewal {
	return domain.Renewal{
		ID: domain.RenewalID(p.ID, p.CoverageEnd), PolicyID: p.ID, CustomerID: p.CustomerID, PeriodEnd: p.CoverageEnd, Status: status,
		Premium: 149_800, Currency: "COP", Terms: domain.CoverageTerms{InsuredAmount: 72_500_000, TermMonths: 12, Coverages: p.Coverages},
		DiscountRate: 0.08, Requoted: true, ChargeID: domain.RenewalChargeID(domain.RenewalID(p.ID, p.CoverageEnd)),
		AcceptedAt: policyEnds.Add(-10 * 24 * time.Hour), RetryUntil: policyEnds.Add(-7 * 24 * time.Hour), CreatedAt: policyEnds.Add(-10 * 24 * time.Hour),
	}
}

func TestPolicyStoreKeepsThePolicyAsItWasActivatedAndIgnoresARedelivery(t *testing.T) {
	requireOfferDocker(t)
	s := postgres.NewPolicyStore(newDB(t))
	ctx := context.Background()
	p := samplePolicy("pol-1", "sub-1", policyEnds)
	p.RenewalOf = "pol-0"

	require.NoError(t, s.Create(ctx, p))
	changed := p
	changed.Premium = 1
	require.NoError(t, s.Create(ctx, changed), "a redelivered activation changes nothing")

	got, err := s.Get(ctx, "pol-1")
	require.NoError(t, err)
	assert.Equal(t, p, got)
	_, err = s.Get(ctx, "pol-404")
	assert.ErrorIs(t, err, ports.ErrPolicyNotFound)
}

func TestPolicyStoreListsTheOnesOfACustomerLastEndingFirst(t *testing.T) {
	requireOfferDocker(t)
	s := postgres.NewPolicyStore(newDB(t))
	ctx := context.Background()
	require.NoError(t, s.Create(ctx, samplePolicy("pol-a", "sub-1", policyEnds)))
	require.NoError(t, s.Create(ctx, samplePolicy("pol-b", "sub-1", policyEnds.AddDate(1, 0, 0))))
	require.NoError(t, s.Create(ctx, samplePolicy("pol-c", "sub-2", policyEnds)))

	got, err := s.ListByCustomer(ctx, "sub-1")

	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, []string{"pol-b", "pol-a"}, []string{got[0].ID, got[1].ID})
}

func TestPolicyStoreIsAppendOnly(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	s := postgres.NewPolicyStore(db)
	require.NoError(t, s.Create(context.Background(), samplePolicy("pol-1", "sub-1", policyEnds)))

	_, err := db.ExecContext(context.Background(), `UPDATE policy SET premium_minor = 1`)
	assert.Error(t, err)
	_, err = db.ExecContext(context.Background(), `DELETE FROM policy`)
	assert.Error(t, err)
}

func TestListLapsedReturnsThePoliciesThatEndedWithNoRenewalInsideTheRange(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	policies, renewals := postgres.NewPolicyStore(db), postgres.NewRenewalStore(db)
	ctx := context.Background()
	now := policyEnds.Add(time.Hour)
	lapsed := samplePolicy("pol-lapsed", "sub-1", policyEnds)
	answered := samplePolicy("pol-answered", "sub-1", policyEnds)
	old := samplePolicy("pol-old", "sub-1", policyEnds.AddDate(-1, 0, 0))
	running := samplePolicy("pol-running", "sub-1", now.AddDate(0, 6, 0))
	for _, p := range []domain.Policy{lapsed, answered, old, running} {
		require.NoError(t, policies.Create(ctx, p))
	}
	_, _, err := renewals.Create(ctx, sampleRenewal(answered, domain.RenewalAccepted))
	require.NoError(t, err)

	got, err := policies.ListLapsed(ctx, now.Add(-30*24*time.Hour), now, 10)

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "pol-lapsed", got[0].ID)
}

func TestRenewalStoreKeepsOneRenewalPerPolicyAndPeriod(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	policies, s := postgres.NewPolicyStore(db), postgres.NewRenewalStore(db)
	ctx := context.Background()
	p := samplePolicy("pol-1", "sub-1", policyEnds)
	require.NoError(t, policies.Create(ctx, p))
	r := sampleRenewal(p, domain.RenewalAccepted)

	stored, created, err := s.Create(ctx, r)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, r, stored)
	other := r
	other.Premium = 1
	again, created, err := s.Create(ctx, other)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, r, again, "the second one finds the first")

	got, err := s.Get(ctx, p.ID, p.CoverageEnd)
	require.NoError(t, err)
	assert.Equal(t, r, got)
	_, err = s.Get(ctx, p.ID, p.CoverageEnd.AddDate(1, 0, 0))
	assert.ErrorIs(t, err, ports.ErrRenewalNotFound)
	list, err := s.ListByPolicies(ctx, []string{p.ID, "pol-x"})
	require.NoError(t, err)
	assert.Len(t, list, 1)
	empty, err := s.ListByPolicies(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

func TestOfTwoConcurrentRenewalsOfAPeriodExactlyOneIsCreated(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	policies, s := postgres.NewPolicyStore(db), postgres.NewRenewalStore(db)
	ctx := context.Background()
	p := samplePolicy("pol-1", "sub-1", policyEnds)
	require.NoError(t, policies.Create(ctx, p))

	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, created, err := s.Create(ctx, sampleRenewal(p, domain.RenewalAccepted))
			assert.NoError(t, err)
			if created {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, won)
}

func TestRenewalStoreClosesAnAcceptedRenewalOnlyOnce(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	policies, s := postgres.NewPolicyStore(db), postgres.NewRenewalStore(db)
	ctx := context.Background()
	p := samplePolicy("pol-1", "sub-1", policyEnds)
	require.NoError(t, policies.Create(ctx, p))
	r := sampleRenewal(p, domain.RenewalAccepted)
	_, _, err := s.Create(ctx, r)
	require.NoError(t, err)
	at := policyEnds.Add(time.Minute)

	changed, err := s.MarkRenewed(ctx, r.ID, "pol-2", at)
	require.NoError(t, err)
	assert.True(t, changed)
	changed, err = s.MarkNotRenewed(ctx, r.ID, domain.NotRenewedPaymentFailed, at)
	require.NoError(t, err)
	assert.False(t, changed, "a renewed period is not closed again")

	got, err := s.Get(ctx, p.ID, p.CoverageEnd)
	require.NoError(t, err)
	assert.Equal(t, domain.RenewalRenewed, got.Status)
	assert.Equal(t, "pol-2", got.NewPolicyID)
}

func TestRenewalStoreListsWhatNeedsAttentionAndRecordsTheNotice(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	policies, s := postgres.NewPolicyStore(db), postgres.NewRenewalStore(db)
	ctx := context.Background()
	waiting, lapsed := samplePolicy("pol-w", "sub-1", policyEnds), samplePolicy("pol-l", "sub-1", policyEnds)
	for _, p := range []domain.Policy{waiting, lapsed} {
		require.NoError(t, policies.Create(ctx, p))
	}
	_, _, err := s.Create(ctx, sampleRenewal(waiting, domain.RenewalAccepted))
	require.NoError(t, err)
	lapse := sampleRenewal(lapsed, domain.RenewalNotRenewed)
	lapse.Reason, lapse.ChargeID = domain.NotRenewedNoResponse, ""
	_, _, err = s.Create(ctx, lapse)
	require.NoError(t, err)

	accepted, err := s.ListAccepted(ctx, 10)
	require.NoError(t, err)
	require.Len(t, accepted, 1)
	assert.Equal(t, waiting.ID, accepted[0].PolicyID)

	pending, err := s.ListUnnotified(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.NoError(t, s.MarkNotified(ctx, lapse.ID, policyEnds, false))
	pending, err = s.ListUnnotified(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, pending)
	got, err := s.Get(ctx, lapsed.ID, lapsed.CoverageEnd)
	require.NoError(t, err)
	assert.False(t, got.NotifiedAt.IsZero())

	require.NoError(t, s.MarkNotified(ctx, waiting.ID, policyEnds, true))
}

func TestCustomerStoreFindsTheCustomerOfAnAccountSubject(t *testing.T) {
	requireOfferDocker(t)
	s := postgres.NewCustomerStore(newDB(t))
	ctx := context.Background()
	_, err := s.Save(ctx, domain.Customer{ID: "cus-1", DocumentID: "1032456789", FullName: "Carolina Pérez", Email: "carolina@example.com",
		BirthDate: time.Date(1985, 5, 1, 0, 0, 0, 0, time.UTC), CognitoSub: "sub-1"})
	require.NoError(t, err)

	got, err := s.GetBySubject(ctx, "sub-1")
	require.NoError(t, err)
	assert.Equal(t, "cus-1", got.ID)
	_, err = s.GetBySubject(ctx, "sub-404")
	assert.ErrorIs(t, err, ports.ErrCustomerNotFound)
	_, err = s.GetBySubject(ctx, "")
	assert.ErrorIs(t, err, ports.ErrCustomerNotFound)
}

// Assisted renewal (HU-W1.5-03): the renewal row and the advisor's record are
// written together, and the period keeps a single outcome.

func sampleFormalization(r domain.Renewal, requoteID, advisor string) domain.RenewalFormalization {
	return domain.RenewalFormalization{
		RenewalID: r.ID, RequoteID: requoteID, AdvisorID: advisor, AdvisorEmail: advisor + "@example.com", AdvisorName: "Laura Gómez",
		Acceptance:   domain.ConsentAcceptance{Method: domain.ConsentRecordedCall, AcceptedAt: requotedAt.Add(2 * time.Minute), Reference: "GRAB-2026-88931"},
		FormalizedAt: requotedAt.Add(3 * time.Minute),
	}
}

func TestCreateAssistedStoresTheRenewalWithItsFormalization(t *testing.T) {
	db, p := newRenewalDB(t)
	ctx := context.Background()
	require.NoError(t, postgres.NewRenewalRequoteStore(db).Create(ctx, sampleRequote("rq-1", p, requotedAt)))
	s := postgres.NewRenewalStore(db)
	r := sampleRenewal(p, domain.RenewalAccepted)
	f := sampleFormalization(r, "rq-1", "sub-advisor")

	stored, formalization, created, err := s.CreateAssisted(ctx, r, f)

	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, r, stored)
	require.NotNil(t, formalization)
	assert.Equal(t, f, *formalization)
	got, err := s.GetFormalization(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, f, got, "the advisor, the date, the acceptance and the requote applied (CA-4)")
	_, err = s.GetFormalization(ctx, "ren-404")
	assert.ErrorIs(t, err, ports.ErrRenewalFormalizationNotFound)
}

func TestCreateAssistedFindsTheOutcomeAlreadyRecordedForThePeriod(t *testing.T) {
	db, p := newRenewalDB(t)
	ctx := context.Background()
	requotes := postgres.NewRenewalRequoteStore(db)
	require.NoError(t, requotes.Create(ctx, sampleRequote("rq-1", p, requotedAt)))
	require.NoError(t, requotes.Create(ctx, sampleRequote("rq-2", p, requotedAt.Add(time.Minute))))
	s := postgres.NewRenewalStore(db)
	r := sampleRenewal(p, domain.RenewalAccepted)
	first := sampleFormalization(r, "rq-1", "sub-advisor")
	_, _, _, err := s.CreateAssisted(ctx, r, first)
	require.NoError(t, err)

	other := r
	other.Premium = 1
	stored, formalization, created, err := s.CreateAssisted(ctx, other, sampleFormalization(other, "rq-2", "sub-other"))

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, r, stored, "the second advisor finds the first renewal")
	require.NotNil(t, formalization)
	assert.Equal(t, first, *formalization)
	var n int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM renewal_formalization`).Scan(&n))
	assert.Equal(t, 1, n)
}

func TestCreateAssistedAfterTheInsuredRenewedOnlineHasNoFormalization(t *testing.T) {
	db, p := newRenewalDB(t)
	ctx := context.Background()
	require.NoError(t, postgres.NewRenewalRequoteStore(db).Create(ctx, sampleRequote("rq-1", p, requotedAt)))
	s := postgres.NewRenewalStore(db)
	online := sampleRenewal(p, domain.RenewalAccepted)
	_, _, err := s.Create(ctx, online)
	require.NoError(t, err)

	stored, formalization, created, err := s.CreateAssisted(ctx, online, sampleFormalization(online, "rq-1", "sub-advisor"))

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, online, stored)
	assert.Nil(t, formalization, "the insured renewed: there is no advisor's record")
}

func TestCreateAssistedIsOneTransaction(t *testing.T) {
	db, p := newRenewalDB(t)
	ctx := context.Background()
	s := postgres.NewRenewalStore(db)
	r := sampleRenewal(p, domain.RenewalAccepted)

	_, _, _, err := s.CreateAssisted(ctx, r, sampleFormalization(r, "rq-missing", "sub-advisor"))

	require.Error(t, err, "the formalization cites a requote that does not exist")
	_, err = s.Get(ctx, p.ID, p.CoverageEnd)
	assert.ErrorIs(t, err, ports.ErrRenewalNotFound, "the renewal was rolled back with it")
}

func TestOfTwentyConcurrentAssistedRenewalsOfAPeriodExactlyOneIsCreated(t *testing.T) {
	db, p := newRenewalDB(t)
	ctx := context.Background()
	requotes := postgres.NewRenewalRequoteStore(db)
	s := postgres.NewRenewalStore(db)
	const advisors = 20
	for i := 0; i < advisors; i++ {
		require.NoError(t, requotes.Create(ctx, sampleRequote(fmt.Sprintf("rq-%02d", i), p, requotedAt.Add(time.Duration(i)*time.Second))))
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	winners := map[string]bool{}
	start := make(chan struct{})
	for i := 0; i < advisors; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			r := sampleRenewal(p, domain.RenewalAccepted)
			_, f, created, err := s.CreateAssisted(ctx, r, sampleFormalization(r, fmt.Sprintf("rq-%02d", i), fmt.Sprintf("sub-%02d", i)))
			assert.NoError(t, err)
			mu.Lock()
			defer mu.Unlock()
			if created {
				won++
			}
			if f != nil {
				winners[f.AdvisorID] = true
			}
		}()
	}
	close(start)
	wg.Wait()

	assert.Equal(t, 1, won)
	assert.Len(t, winners, 1, "everyone sees the same winner")
	for table, want := range map[string]int{"renewal": 1, "renewal_formalization": 1} {
		var n int
		require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM `+table).Scan(&n))
		assert.Equal(t, want, n, table)
	}
}

func TestARenewalFormalizationIsAppendOnlyAndChecked(t *testing.T) {
	db, p := newRenewalDB(t)
	ctx := context.Background()
	requotes := postgres.NewRenewalRequoteStore(db)
	require.NoError(t, requotes.Create(ctx, sampleRequote("rq-1", p, requotedAt)))
	s := postgres.NewRenewalStore(db)
	r := sampleRenewal(p, domain.RenewalAccepted)

	noReference := sampleFormalization(r, "rq-1", "sub-advisor")
	noReference.Acceptance.Reference = ""
	_, _, _, err := s.CreateAssisted(ctx, r, noReference)
	assert.Error(t, err, "a recorded call needs its reference")

	byCode := sampleFormalization(r, "rq-1", "sub-advisor")
	byCode.AdvisorName = ""
	byCode.Acceptance = domain.ConsentAcceptance{Method: domain.ConsentConfirmationCode, AcceptedAt: requotedAt.Add(time.Minute)}
	_, f, created, err := s.CreateAssisted(ctx, r, byCode)
	require.NoError(t, err, "the code needs no reference, and the token may carry no name")
	assert.True(t, created)
	assert.Empty(t, f.AdvisorName)
	got, err := s.GetFormalization(ctx, r.ID)
	require.NoError(t, err)
	assert.Equal(t, byCode, got)

	_, err = db.ExecContext(ctx, `UPDATE renewal_formalization SET advisor_id = 'x'`)
	assert.Error(t, err)
	_, err = db.ExecContext(ctx, `DELETE FROM renewal_formalization`)
	assert.Error(t, err)
	_, err = db.ExecContext(ctx, `TRUNCATE renewal_formalization`)
	assert.Error(t, err)
}
