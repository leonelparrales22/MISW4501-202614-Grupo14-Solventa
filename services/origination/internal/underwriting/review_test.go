package underwriting_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/underwriting"
)

type env struct {
	clock       *testClock
	reviews     *reviews
	acceptances *acceptances
	offers      *offers
	resumed     *counter
	scheduled   *counter
	svc         *underwriting.ReviewService
}

// newEnv builds the service over the in-memory adapters; withNotices turns the
// customer notice on, as main does when EMAIL_URL is set.
func newEnv(t *testing.T, withNotices bool) *env {
	t.Helper()
	clock := &testClock{now: t0}
	e := &env{
		clock:       clock,
		reviews:     &reviews{ReviewQueueStub: memory.NewReviewQueueStub()},
		acceptances: &acceptances{AcceptanceStore: memory.NewAcceptanceStore()},
		offers:      &offers{OfferStub: memory.NewOfferStub(clock)},
		resumed:     &counter{},
		scheduled:   &counter{},
	}
	deps := underwriting.ReviewDeps{
		Reviews: e.reviews, Acceptances: e.acceptances, Offers: e.offers, OfferWriter: e.offers, Clock: clock,
		AcceptanceResumed: e.resumed.inc,
	}
	if withNotices {
		deps.NotificationScheduled = e.scheduled.inc
	}
	e.svc = underwriting.NewReviewService(deps)
	return e
}

// route registers an acceptance of the insured cust-1 that underwriting
// routed to the inbox, as the acceptance processor of HU-W1.3-01 leaves it.
func (e *env) route(t *testing.T, acceptanceID, offerID, reason string) domain.Offer {
	t.Helper()
	ctx := context.Background()
	offer, err := e.offers.OfferStub.Get(as(security.RoleInsured, "cust-1"), offerID)
	require.NoError(t, err)
	now := e.clock.Now()
	_, _, err = e.acceptances.Create(ctx, domain.Acceptance{
		ID: acceptanceID, OfferID: offerID, CustomerID: "cust-1", Channel: domain.ChannelWeb, AcceptedAt: now,
		Status:   domain.AcceptanceInReview,
		Decision: &domain.UnderwritingDecision{Outcome: domain.UnderwritingHumanReview, Reason: reason, DecidedAt: now},
	})
	require.NoError(t, err)
	require.NoError(t, e.reviews.Enqueue(ctx, domain.ReviewRequest{
		AcceptanceID: acceptanceID, OfferID: offerID, CustomerID: "cust-1", Applicant: offer.Applicant,
		Premium: offer.Premium, Currency: offer.Currency, Reason: reason, ReceivedAt: now,
	}))
	return offer
}

func (e *env) acceptance(t *testing.T, id string) domain.Acceptance {
	t.Helper()
	a, err := e.acceptances.AcceptanceStore.Get(context.Background(), id)
	require.NoError(t, err)
	return a
}

func adjust(p float64, obs string) domain.DecisionInput {
	return domain.DecisionInput{Type: domain.DecisionAdjust, Observations: obs, PremiumAdjustmentPct: &p}
}

var (
	accept = domain.DecisionInput{Type: domain.DecisionAccept}
	reject = domain.DecisionInput{Type: domain.DecisionReject, Observations: "Riesgo fuera de política"}
)

func TestEveryOperationRequiresTheAdvisorRole(t *testing.T) {
	e := newEnv(t, true)
	e.route(t, "acc-1", "demo-review-1", domain.ReviewReasonProvisionalOffer)

	for _, ctx := range []context.Context{context.Background(), as(security.RoleInsured, "cust-1"),
		as(security.RoleOperations, "ops-1"), as(security.RolePartner, "par-1")} {
		_, _, err := e.svc.List(ctx, domain.ReviewFilter{Status: domain.ReviewPending})
		assert.True(t, errors.Is(err, security.ErrUnauthenticated) || errors.Is(err, security.ErrForbidden), "list: %v", err)
		_, err = e.svc.Get(ctx, "acc-1")
		assert.True(t, errors.Is(err, security.ErrUnauthenticated) || errors.Is(err, security.ErrForbidden), "get: %v", err)
		_, err = e.svc.Decide(ctx, "acc-1", accept)
		assert.True(t, errors.Is(err, security.ErrUnauthenticated) || errors.Is(err, security.ErrForbidden), "decide: %v", err)
	}
	assert.Equal(t, domain.AcceptanceInReview, e.acceptance(t, "acc-1").Status, "nothing was decided")
}

// CA-1 and CA-5 through the use case.
func TestListAndGetShowTheRoutedRequest(t *testing.T) {
	e := newEnv(t, true)
	offer := e.route(t, "acc-1", "demo-review-1", domain.ReviewReasonProvisionalOffer)
	e.clock.advance(time.Minute)
	e.route(t, "acc-2", "demo-highrisk-1", domain.ReviewReasonHighRiskScore)

	cases, counts, err := e.svc.List(advisor, domain.ReviewFilter{Status: domain.ReviewPending, Descending: true})
	require.NoError(t, err)
	require.Len(t, cases, 2)
	assert.Equal(t, "acc-2", cases[0].Request.AcceptanceID)
	assert.Equal(t, domain.ReviewCounts{Pending: 2}, counts)

	got, err := e.svc.Get(advisor, "acc-1")
	require.NoError(t, err)
	assert.Equal(t, domain.ReviewPending, got.Case.Status)
	assert.Equal(t, offer.Applicant, got.Case.Request.Applicant)
	assert.Equal(t, domain.ModeProvisional, got.OfferMode)
	assert.Equal(t, offer.ExpiresAt, got.OfferExpiresAt)

	_, err = e.svc.Get(advisor, "missing")
	assert.ErrorIs(t, err, ports.ErrReviewRequestNotFound)
}

func TestGetLeavesTheOfferFieldsEmptyWhenTheOfferCannotBeRead(t *testing.T) {
	e := newEnv(t, true)
	e.route(t, "acc-1", "demo-review-1", domain.ReviewReasonProvisionalOffer)
	e.offers.getErr = errors.New("offers down")

	got, err := e.svc.Get(advisor, "acc-1")
	require.NoError(t, err)
	assert.Equal(t, "acc-1", got.Case.Request.AcceptanceID)
	assert.Empty(t, got.OfferMode)
	assert.True(t, got.OfferExpiresAt.IsZero())
}

// CA-3 accept + CA-4: the acceptance goes back to the processor, approved.
func TestAcceptReturnsTheAcceptanceToTheProcessorApproved(t *testing.T) {
	e := newEnv(t, true)
	e.route(t, "acc-1", "demo-review-1", domain.ReviewReasonProvisionalOffer)
	e.clock.advance(2 * time.Hour)

	c, err := e.svc.Decide(advisor, "acc-1", domain.DecisionInput{Type: domain.DecisionAccept, Observations: "  Verificado  "})
	require.NoError(t, err)

	assert.Equal(t, domain.ReviewDecided, c.Status)
	d := c.Decision
	require.NotNil(t, d)
	assert.NotEmpty(t, d.ID)
	assert.Equal(t, "acc-1", d.AcceptanceID)
	assert.Equal(t, domain.DecisionAccept, d.Type)
	assert.Equal(t, domain.Advisor{ID: "adv-1", Email: "adv-1@example.com"}, d.Advisor)
	assert.Equal(t, t0.Add(2*time.Hour), d.DecidedAt)
	assert.Equal(t, "Verificado", d.Observations)
	assert.Nil(t, d.PremiumAdjustmentPct)
	assert.Nil(t, d.AdjustedOffer)
	assert.Nil(t, c.Notification, "the issuance e-mail of HU-W1.3-03 already tells the customer")

	a := e.acceptance(t, "acc-1")
	assert.Equal(t, domain.AcceptanceRegistered, a.Status)
	assert.Equal(t, domain.UnderwritingApproved, a.Decision.Outcome)
	assert.Equal(t, d.DecidedAt, a.Decision.DecidedAt)
	assert.Equal(t, t0.Add(2*time.Hour), a.NextAttemptAt, "due now, so the processor starts the issuance")
	assert.Equal(t, &domain.AcceptanceReview{Outcome: domain.DecisionAccept, DecidedAt: d.DecidedAt}, a.Review)
	assert.Equal(t, 1, e.resumed.count())
	assert.Zero(t, e.scheduled.count())
	assert.Empty(t, e.reviews.Pending(), "the case left the pending list")
}

// CA-3 adjust: a new offer with the advisor conditions and the notice.
func TestAdjustCreatesTheNewOfferAndSchedulesTheNotice(t *testing.T) {
	e := newEnv(t, true)
	offer := e.route(t, "acc-1", "demo-review-1", domain.ReviewReasonProvisionalOffer)
	e.clock.advance(time.Hour)

	c, err := e.svc.Decide(advisor, "acc-1", adjust(10, "Ingreso no verificado"))
	require.NoError(t, err)

	decidedAt := t0.Add(time.Hour)
	want := &domain.AdjustedOffer{ID: "adj-acc-1", Premium: 98450, Currency: "COP", ExpiresAt: decidedAt.Add(24 * time.Hour)}
	assert.Equal(t, want, c.Decision.AdjustedOffer)
	assert.InDelta(t, 10.0, *c.Decision.PremiumAdjustmentPct, 1e-9)

	stored, err := e.offers.OfferStub.Get(context.Background(), "adj-acc-1")
	require.NoError(t, err)
	assert.Equal(t, domain.Offer{
		ID: "adj-acc-1", CustomerID: offer.CustomerID, Applicant: offer.Applicant, Premium: 98450, Currency: "COP",
		Mode: offer.Mode, ProfileScore: offer.ProfileScore, ExpiresAt: decidedAt.Add(24 * time.Hour),
	}, stored)

	a := e.acceptance(t, "acc-1")
	assert.Equal(t, domain.AcceptanceAdjusted, a.Status)
	assert.Equal(t, &domain.AcceptanceReview{Outcome: domain.DecisionAdjust, DecidedAt: decidedAt, AdjustedOffer: want}, a.Review)
	assert.True(t, a.NextAttemptAt.IsZero())
	assert.Equal(t, domain.UnderwritingHumanReview, a.Decision.Outcome, "the routing trace stays")

	require.NotNil(t, c.Notification)
	assert.Equal(t, domain.ReviewNotification{Channel: domain.ChannelEmail, Status: domain.NotificationPending, NextAttemptAt: decidedAt},
		*c.Notification)
	assert.Equal(t, 1, e.scheduled.count())
	assert.Zero(t, e.resumed.count())
}

// CF-10 (fix C5, decision #75): the adjusted offer belongs to the customer of
// the request that underwriting routed, with its applicant; never to the
// advisor who decided, who only appears as decided_by.
func TestTheAdjustedOfferBelongsToTheCustomerOfTheRequest(t *testing.T) {
	e := newEnv(t, true)
	offer, err := e.offers.OfferStub.Get(as(security.RoleInsured, "cust-1"), "demo-review-1")
	require.NoError(t, err)
	ctx := context.Background()
	_, _, err = e.acceptances.Create(ctx, domain.Acceptance{
		ID: "acc-1", OfferID: offer.ID, CustomerID: "cust-1", Channel: domain.ChannelWeb, AcceptedAt: t0,
		Status:   domain.AcceptanceInReview,
		Decision: &domain.UnderwritingDecision{Outcome: domain.UnderwritingHumanReview, Reason: domain.ReviewReasonProvisionalOffer, DecidedAt: t0},
	})
	require.NoError(t, err)
	// The request carries the customer data underwriting saw; it differs from
	// the demo applicant of the stub on purpose, to tell where it comes from.
	applicant := domain.Applicant{FullName: "Ana María Pérez", DocumentID: "1020304050", Email: "ana@example.com"}
	require.NoError(t, e.reviews.Enqueue(ctx, domain.ReviewRequest{
		AcceptanceID: "acc-1", OfferID: offer.ID, CustomerID: "cust-1", Applicant: applicant,
		Premium: offer.Premium, Currency: offer.Currency, Reason: domain.ReviewReasonProvisionalOffer, ReceivedAt: t0,
	}))

	c, err := e.svc.Decide(as(security.RoleAdvisor, "adv-9"), "acc-1", adjust(10, "Recargo"))
	require.NoError(t, err)

	stored, err := e.offers.OfferStub.Get(ctx, "adj-acc-1")
	require.NoError(t, err)
	assert.Equal(t, "cust-1", stored.CustomerID)
	assert.Equal(t, applicant, stored.Applicant)
	assert.NotEqual(t, "adv-9", stored.CustomerID)
	assert.Equal(t, domain.Advisor{ID: "adv-9", Email: "adv-9@example.com"}, c.Decision.Advisor, "the advisor is only decided_by")
	assert.Equal(t, "cust-1", e.acceptance(t, "acc-1").CustomerID)
}

// CA-3 reject: the case is closed and the customer notified.
func TestRejectClosesTheCaseAndSchedulesTheNotice(t *testing.T) {
	e := newEnv(t, true)
	e.route(t, "acc-1", "demo-highrisk-1", domain.ReviewReasonHighRiskScore)

	c, err := e.svc.Decide(advisor, "acc-1", reject)
	require.NoError(t, err)

	assert.Equal(t, domain.DecisionReject, c.Decision.Type)
	assert.Equal(t, "Riesgo fuera de política", c.Decision.Observations)
	assert.Nil(t, c.Decision.AdjustedOffer)
	a := e.acceptance(t, "acc-1")
	assert.Equal(t, domain.AcceptanceRejected, a.Status)
	assert.Equal(t, domain.DecisionReject, a.Review.Outcome)
	require.NotNil(t, c.Notification)
	assert.Equal(t, domain.NotificationPending, c.Notification.Status)
	assert.Equal(t, 1, e.scheduled.count())

	_, err = e.offers.OfferStub.Get(context.Background(), "adj-acc-1")
	assert.ErrorIs(t, err, ports.ErrOfferNotFound, "a reject creates no offer")
}

func TestNoNoticeIsScheduledWithoutTheEmailChannel(t *testing.T) {
	e := newEnv(t, false)
	e.route(t, "acc-1", "demo-highrisk-1", domain.ReviewReasonHighRiskScore)

	c, err := e.svc.Decide(advisor, "acc-1", reject)
	require.NoError(t, err)
	assert.Nil(t, c.Notification)
	assert.Equal(t, domain.AcceptanceRejected, e.acceptance(t, "acc-1").Status)
}

// CA-2 through the use case: an invalid input changes nothing.
func TestAnInvalidDecisionChangesNothing(t *testing.T) {
	e := newEnv(t, true)
	e.route(t, "acc-1", "demo-review-1", domain.ReviewReasonProvisionalOffer)

	_, err := e.svc.Decide(advisor, "acc-1", domain.DecisionInput{Type: domain.DecisionReject, Observations: "  "})
	assert.ErrorIs(t, err, domain.ErrObservationsRequired)
	_, err = e.svc.Decide(advisor, "acc-1", adjust(0, "x"))
	assert.ErrorIs(t, err, domain.ErrInvalidPremiumAdjustment)

	assert.Len(t, e.reviews.Pending(), 1)
	assert.Equal(t, domain.AcceptanceInReview, e.acceptance(t, "acc-1").Status)
}

func TestDecideAnUnknownRequest(t *testing.T) {
	e := newEnv(t, true)
	_, err := e.svc.Decide(advisor, "missing", accept)
	assert.ErrorIs(t, err, ports.ErrReviewRequestNotFound)
}

// CA-4: a decided case is never decided again.
func TestDecidingTwiceKeepsTheFirstDecision(t *testing.T) {
	e := newEnv(t, true)
	e.route(t, "acc-1", "demo-highrisk-1", domain.ReviewReasonHighRiskScore)
	first, err := e.svc.Decide(advisor, "acc-1", reject)
	require.NoError(t, err)

	c, err := e.svc.Decide(as(security.RoleAdvisor, "adv-2"), "acc-1", accept)
	assert.ErrorIs(t, err, ports.ErrReviewAlreadyDecided)
	assert.Equal(t, first.Decision, c.Decision)
	assert.Equal(t, domain.AcceptanceRejected, e.acceptance(t, "acc-1").Status)
	assert.Equal(t, 1, e.scheduled.count(), "the notice is scheduled once")
}

func TestALostClaimShowsTheWinningDecision(t *testing.T) {
	e := newEnv(t, true)
	e.route(t, "acc-1", "demo-highrisk-1", domain.ReviewReasonHighRiskScore)
	e.reviews.lostClaim = true

	c, err := e.svc.Decide(advisor, "acc-1", reject)
	assert.ErrorIs(t, err, ports.ErrReviewAlreadyDecided)
	assert.Equal(t, "adv-2", c.Decision.Advisor.ID)
	assert.Equal(t, domain.AcceptanceRejected, e.acceptance(t, "acc-1").Status, "the winner's continuation is completed")
}

func TestAnAcceptanceThatIsNotInReviewCannotBeDecided(t *testing.T) {
	e := newEnv(t, true)
	e.route(t, "acc-1", "demo-review-1", domain.ReviewReasonProvisionalOffer)
	a := e.acceptance(t, "acc-1")
	a.Status = domain.AcceptanceIssuancePending
	require.NoError(t, e.acceptances.AcceptanceStore.Update(context.Background(), a))

	_, err := e.svc.Decide(advisor, "acc-1", accept)
	assert.ErrorIs(t, err, ports.ErrReviewAlreadyDecided)
	assert.Len(t, e.reviews.Pending(), 1)
}

func TestAFailedRecordLeavesTheCasePending(t *testing.T) {
	e := newEnv(t, true)
	e.route(t, "acc-1", "demo-review-1", domain.ReviewReasonProvisionalOffer)
	e.reviews.recordErr = errors.New("store down")

	_, err := e.svc.Decide(advisor, "acc-1", adjust(10, "x"))
	assert.ErrorIs(t, err, underwriting.ErrDecisionNotRegistered)
	assert.Equal(t, domain.AcceptanceInReview, e.acceptance(t, "acc-1").Status)
	_, err = e.offers.OfferStub.Get(context.Background(), "adj-acc-1")
	assert.ErrorIs(t, err, ports.ErrOfferNotFound, "no offer exists without a recorded decision")

	e.reviews.recordErr = nil
	c, err := e.svc.Decide(advisor, "acc-1", adjust(10, "x"))
	require.NoError(t, err)
	assert.Equal(t, "adj-acc-1", c.Decision.AdjustedOffer.ID)
}

// To adjust, the routed offer must be readable before the claim: nothing is
// recorded otherwise.
func TestTheRoutedOfferMustBeReadableToAdjust(t *testing.T) {
	e := newEnv(t, true)
	e.route(t, "acc-1", "demo-review-1", domain.ReviewReasonProvisionalOffer)
	e.offers.getErr = errors.New("offers down")

	_, err := e.svc.Decide(advisor, "acc-1", adjust(10, "x"))
	assert.ErrorIs(t, err, underwriting.ErrDecisionNotRegistered)
	assert.Len(t, e.reviews.Pending(), 1, "nothing was recorded")
}

// B-1: the offer is created after the claim. If creating it fails, the
// decision stays recorded and a retry creates it from the recorded decision.
func TestARetryCreatesTheAdjustedOfferOfTheRecordedDecision(t *testing.T) {
	e := newEnv(t, true)
	e.route(t, "acc-1", "demo-review-1", domain.ReviewReasonProvisionalOffer)
	e.offers.createErr = errors.New("offers down")

	c, err := e.svc.Decide(advisor, "acc-1", adjust(10, "Recargo"))
	assert.ErrorIs(t, err, underwriting.ErrContinuationPending)
	assert.Equal(t, domain.ReviewDecided, c.Status, "the decision is recorded")
	_, err = e.offers.OfferStub.Get(context.Background(), "adj-acc-1")
	assert.ErrorIs(t, err, ports.ErrOfferNotFound)
	assert.Equal(t, domain.AcceptanceInReview, e.acceptance(t, "acc-1").Status, "never adjusted without its offer")
	assert.Zero(t, e.scheduled.count())

	// The retry carries other terms (50 %): the recorded decision (10 %) wins.
	e.offers.createErr = nil
	again, err := e.svc.Decide(advisor, "acc-1", adjust(50, "Otro recargo"))
	assert.ErrorIs(t, err, ports.ErrReviewAlreadyDecided)
	stored, err := e.offers.OfferStub.Get(context.Background(), "adj-acc-1")
	require.NoError(t, err)
	assert.InDelta(t, 98450.0, stored.Premium, 1e-9)
	assert.Equal(t, again.Decision.AdjustedOffer.ExpiresAt, stored.ExpiresAt)
	assert.InDelta(t, 98450.0, again.Decision.AdjustedOffer.Premium, 1e-9)
	assert.Equal(t, domain.AcceptanceAdjusted, e.acceptance(t, "acc-1").Status)
	assert.InDelta(t, 98450.0, e.acceptance(t, "acc-1").Review.AdjustedOffer.Premium, 1e-9)
	assert.Equal(t, 1, e.scheduled.count())
}

// adjustedState reads the stored offer, the recorded decision and the
// acceptance of a case: the three must show the same premium.
func adjustedState(t *testing.T, e *env, id string) (offer, decision, acceptance float64) {
	t.Helper()
	o, err := e.offers.OfferStub.Get(context.Background(), domain.AdjustedOfferID(id))
	require.NoError(t, err)
	c, err := e.reviews.ReviewQueueStub.Get(context.Background(), id)
	require.NoError(t, err)
	a := e.acceptance(t, id)
	require.NotNil(t, a.Review)
	require.NotNil(t, a.Review.AdjustedOffer)
	return o.Premium, c.Decision.AdjustedOffer.Premium, a.Review.AdjustedOffer.Premium
}

// B-1, deterministic interleaving (the validator's case): advisor A (10 %)
// computes its terms and stops just before the claim; advisor B (50 %) decides
// meanwhile; then A goes on and loses. The offer has B's terms.
func TestTheLosingAdvisorNeverCreatesTheAdjustedOffer(t *testing.T) {
	e := newEnv(t, true)
	e.route(t, "acc-1", "demo-review-1", domain.ReviewReasonProvisionalOffer)
	e.reviews.hold = func(d domain.HumanDecision) bool {
		return d.PremiumAdjustmentPct != nil && *d.PremiumAdjustmentPct == 10
	}
	e.reviews.entered, e.reviews.release = make(chan struct{}), make(chan struct{})

	errA := make(chan error, 1)
	go func() {
		_, err := e.svc.Decide(as(security.RoleAdvisor, "adv-a"), "acc-1", adjust(10, "A"))
		errA <- err
	}()
	<-e.reviews.entered
	_, err := e.offers.OfferStub.Get(context.Background(), "adj-acc-1")
	require.ErrorIs(t, err, ports.ErrOfferNotFound, "A reached the claim without creating the offer")

	_, errB := e.svc.Decide(as(security.RoleAdvisor, "adv-b"), "acc-1", adjust(50, "B"))
	require.NoError(t, errB)
	close(e.reviews.release)
	require.ErrorIs(t, <-errA, ports.ErrReviewAlreadyDecided)

	offer, decision, acceptance := adjustedState(t, e, "acc-1")
	assert.InDelta(t, 134250.0, offer, 1e-9, "the offer the customer accepts and pays")
	assert.InDelta(t, 134250.0, decision, 1e-9, "the recorded decision (CA-4)")
	assert.InDelta(t, 134250.0, acceptance, 1e-9, "what E6b shows")
}

// B-1, free race: two advisors adjust the same case at once with different
// surcharges (goroutines and a barrier). In every run one wins, the other gets
// 409 and the offer, the decision and the acceptance show the winner's premium.
func TestConcurrentAdjustsKeepTheWinnersTerms(t *testing.T) {
	for run := 0; run < 300; run++ {
		e := newEnv(t, true)
		e.route(t, "acc-1", "demo-review-1", domain.ReviewReasonProvisionalOffer)
		start := make(chan struct{})
		errs := make([]error, 2)
		var wg sync.WaitGroup
		for i, p := range []float64{10, 50} {
			wg.Add(1)
			go func(i int, p float64) {
				defer wg.Done()
				<-start
				_, errs[i] = e.svc.Decide(as(security.RoleAdvisor, fmt.Sprintf("adv-%d", i)), "acc-1", adjust(p, "x"))
			}(i, p)
		}
		close(start)
		wg.Wait()

		wins := 0
		for _, err := range errs {
			if err == nil {
				wins++
			} else {
				require.ErrorIs(t, err, ports.ErrReviewAlreadyDecided, "run %d", run)
			}
		}
		require.Equal(t, 1, wins, "run %d", run)
		offer, decision, acceptance := adjustedState(t, e, "acc-1")
		require.InDelta(t, decision, offer, 1e-9, "run %d: offer vs decision", run)
		require.InDelta(t, decision, acceptance, 1e-9, "run %d: acceptance vs decision", run)
	}
}

// B-1 variant: an adjust that loses against a reject leaves no orphan offer.
func TestAnAdjustThatLosesAgainstARejectLeavesNoOffer(t *testing.T) {
	for run := 0; run < 300; run++ {
		e := newEnv(t, true)
		e.route(t, "acc-1", "demo-review-1", domain.ReviewReasonProvisionalOffer)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, in := range []domain.DecisionInput{adjust(10, "x"), reject} {
			wg.Add(1)
			go func(in domain.DecisionInput) {
				defer wg.Done()
				<-start
				_, _ = e.svc.Decide(advisor, "acc-1", in)
			}(in)
		}
		close(start)
		wg.Wait()

		c, err := e.reviews.ReviewQueueStub.Get(context.Background(), "acc-1")
		require.NoError(t, err)
		_, offerErr := e.offers.OfferStub.Get(context.Background(), "adj-acc-1")
		if c.Decision.Type == domain.DecisionReject {
			require.ErrorIs(t, offerErr, ports.ErrOfferNotFound, "run %d: orphan adjusted offer", run)
			require.Equal(t, domain.AcceptanceRejected, e.acceptance(t, "acc-1").Status, "run %d", run)
		} else {
			require.NoError(t, offerErr, "run %d", run)
			require.Equal(t, domain.AcceptanceAdjusted, e.acceptance(t, "acc-1").Status, "run %d", run)
		}
	}
}

// A recorded decision whose acceptance update failed is completed by a retry.
func TestARetryCompletesAPendingContinuation(t *testing.T) {
	e := newEnv(t, true)
	e.route(t, "acc-1", "demo-review-1", domain.ReviewReasonProvisionalOffer)
	e.acceptances.updateErrs = []error{errors.New("store down")}

	c, err := e.svc.Decide(advisor, "acc-1", adjust(5, "x"))
	assert.ErrorIs(t, err, underwriting.ErrContinuationPending)
	assert.Equal(t, domain.ReviewDecided, c.Status, "the decision is recorded")
	assert.Equal(t, domain.AcceptanceInReview, e.acceptance(t, "acc-1").Status)
	assert.Zero(t, e.scheduled.count())

	c, err = e.svc.Decide(advisor, "acc-1", adjust(5, "x"))
	assert.ErrorIs(t, err, ports.ErrReviewAlreadyDecided)
	assert.Equal(t, domain.AcceptanceAdjusted, e.acceptance(t, "acc-1").Status)
	require.NotNil(t, c.Notification)
	assert.Equal(t, 1, e.scheduled.count())
}

func TestARetrySchedulesANoticeThatCouldNotBeScheduled(t *testing.T) {
	e := newEnv(t, true)
	e.route(t, "acc-1", "demo-highrisk-1", domain.ReviewReasonHighRiskScore)
	e.reviews.updateErrs = []error{errors.New("store down")}

	_, err := e.svc.Decide(advisor, "acc-1", reject)
	assert.ErrorIs(t, err, underwriting.ErrContinuationPending)
	assert.Equal(t, domain.AcceptanceRejected, e.acceptance(t, "acc-1").Status)

	c, err := e.svc.Decide(advisor, "acc-1", reject)
	assert.ErrorIs(t, err, ports.ErrReviewAlreadyDecided)
	require.NotNil(t, c.Notification)
	assert.Equal(t, 1, e.scheduled.count())
}

func TestAContinuationThatStillFailsOnRetryIsReported(t *testing.T) {
	e := newEnv(t, true)
	e.route(t, "acc-1", "demo-review-1", domain.ReviewReasonProvisionalOffer)
	e.acceptances.updateErrs = []error{errors.New("down"), errors.New("still down")}

	_, err := e.svc.Decide(advisor, "acc-1", accept)
	assert.ErrorIs(t, err, underwriting.ErrContinuationPending)
	_, err = e.svc.Decide(advisor, "acc-1", accept)
	assert.ErrorIs(t, err, underwriting.ErrContinuationPending)
	assert.Zero(t, e.resumed.count())
}

func TestReadFailuresAreReturned(t *testing.T) {
	e := newEnv(t, true)
	e.route(t, "acc-1", "demo-review-1", domain.ReviewReasonProvisionalOffer)
	boom := errors.New("down")

	e.acceptances.getErr = boom
	_, err := e.svc.Decide(advisor, "acc-1", accept)
	assert.ErrorIs(t, err, boom)

	e.acceptances.getErr, e.reviews.getErr = nil, boom
	_, err = e.svc.Decide(advisor, "acc-1", accept)
	assert.ErrorIs(t, err, boom)
	_, err = e.svc.Get(advisor, "acc-1")
	assert.ErrorIs(t, err, boom)
}

func TestTheDecisionIsReturnedWhenTheRefreshFails(t *testing.T) {
	e := newEnv(t, true)
	e.route(t, "acc-1", "demo-highrisk-1", domain.ReviewReasonHighRiskScore)
	// The refresh after the continuation is the second read of the case.
	reads := 0
	failing := &refreshFailing{reviews: e.reviews, failOn: 1, reads: &reads}
	svc := underwriting.NewReviewService(underwriting.ReviewDeps{
		Reviews: failing, Acceptances: e.acceptances, Offers: e.offers, OfferWriter: e.offers, Clock: e.clock,
	})

	c, err := svc.Decide(advisor, "acc-1", reject)
	require.NoError(t, err)
	assert.Equal(t, domain.ReviewDecided, c.Status)
}

// refreshFailing fails the n-th Get (0-based).
type refreshFailing struct {
	*reviews
	failOn int
	reads  *int
}

func (r *refreshFailing) Get(ctx context.Context, id string) (domain.ReviewCase, error) {
	n := *r.reads
	*r.reads++
	if n == r.failOn {
		return domain.ReviewCase{}, errors.New("down")
	}
	return r.reviews.Get(ctx, id)
}
