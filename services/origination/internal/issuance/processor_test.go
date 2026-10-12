package issuance_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/issuance"
)

// flakyIssuance fails Start the first n calls.
type flakyIssuance struct {
	*memory.IssuanceStub
	failures atomic.Int32
}

func (f *flakyIssuance) Start(ctx context.Context, a domain.Acceptance, o domain.Offer) error {
	if f.failures.Add(-1) >= 0 {
		return errors.New("issuance down")
	}
	return f.IssuanceStub.Start(ctx, a, o)
}

// countingUnderwriter counts the decisions it takes.
type countingUnderwriter struct {
	*memory.UnderwriterStub
	calls atomic.Int32
}

func (c *countingUnderwriter) Decide(ctx context.Context, o domain.Offer, a domain.Acceptance) (domain.UnderwritingDecision, error) {
	c.calls.Add(1)
	return c.UnderwriterStub.Decide(ctx, o, a)
}

type processorFixture struct {
	*serviceFixture
	processor   *issuance.Processor
	underwriter *countingUnderwriter
	reviews     *memory.ReviewQueueStub
	issuance    *flakyIssuance
}

func newProcessorFixture(offers offerRepo, issuanceFailures int32) *processorFixture {
	f := &processorFixture{serviceFixture: newServiceFixture(offers), reviews: memory.NewReviewQueueStub()}
	f.underwriter = &countingUnderwriter{UnderwriterStub: memory.NewUnderwriterStub(f.clock, memory.DefaultFlakyFailures)}
	f.issuance = &flakyIssuance{IssuanceStub: memory.NewIssuanceStub()}
	f.issuance.failures.Store(issuanceFailures)
	f.processor = issuance.NewProcessor(offers, f.acceptances, f.underwriter, f.reviews, f.issuance, f.clock,
		issuance.ProcessorConfig{RetryBase: 2 * time.Second, RetryMax: 5 * time.Second})
	return f
}

func (f *processorFixture) accept(t *testing.T, offerID, owner string) domain.Acceptance {
	t.Helper()
	a, _, err := f.svc.Accept(insured(owner), offerID, domain.ChannelWeb)
	require.NoError(t, err)
	return a
}

func (f *processorFixture) reload(t *testing.T, id string) domain.Acceptance {
	t.Helper()
	a, err := f.acceptances.Get(context.Background(), id)
	require.NoError(t, err)
	return a
}

func TestApprovedRiskStartsIssuanceRightAway(t *testing.T) {
	f := newProcessorFixture(offerRepo{"o-1": validOffer("o-1", "cust-1")}, 0)
	a := f.accept(t, "o-1", "cust-1")

	assert.Equal(t, 1, f.processor.ProcessDue(context.Background()))

	got := f.reload(t, a.ID)
	assert.Equal(t, domain.AcceptanceIssuancePending, got.Status)
	require.NotNil(t, got.Decision)
	assert.Equal(t, domain.UnderwritingApproved, got.Decision.Outcome)
	assert.Empty(t, got.Decision.Reason)
	assert.True(t, got.NextAttemptAt.IsZero())
	assert.True(t, f.issuance.Started(a.ID))
	assert.Empty(t, f.reviews.Pending())
	assert.Zero(t, f.processor.ProcessDue(context.Background()), "nothing left to process")
}

func TestHumanReviewGoesToTheAdvisorInboxWithTheRequestData(t *testing.T) {
	provisional := validOffer("o-1", "cust-1")
	provisional.Mode = domain.ModeProvisional
	provisional.Applicant = domain.Applicant{FullName: "Ana", DocumentID: "CC 1", Email: "ana@example.com"}
	lowScore := validOffer("o-2", "cust-1")
	lowScore.ProfileScore = 0.2
	f := newProcessorFixture(offerRepo{"o-1": provisional, "o-2": lowScore}, 0)
	a1 := f.accept(t, "o-1", "cust-1")
	a2 := f.accept(t, "o-2", "cust-1")

	f.processor.ProcessDue(context.Background())

	for _, id := range []string{a1.ID, a2.ID} {
		got := f.reload(t, id)
		assert.Equal(t, domain.AcceptanceInReview, got.Status)
		assert.False(t, f.issuance.Started(id))
	}
	pending := f.reviews.Pending()
	require.Len(t, pending, 2)
	byOffer := map[string]domain.ReviewRequest{pending[0].OfferID: pending[0], pending[1].OfferID: pending[1]}
	assert.Equal(t, domain.ReviewRequest{
		AcceptanceID: a1.ID, OfferID: "o-1", CustomerID: "cust-1", Applicant: provisional.Applicant,
		Premium: 1000, Currency: "COP", Reason: domain.ReviewReasonProvisionalOffer, ReceivedAt: t0,
	}, byOffer["o-1"])
	assert.Equal(t, domain.ReviewReasonLowRiskScore, byOffer["o-2"].Reason)
}

func TestAFailedStepKeepsTheAcceptanceAndResumesWithoutDecidingTwice(t *testing.T) {
	f := newProcessorFixture(offerRepo{"o-1": validOffer("o-1", "cust-1")}, 2)
	a := f.accept(t, "o-1", "cust-1")

	f.processor.ProcessDue(context.Background())
	got := f.reload(t, a.ID)
	assert.Equal(t, domain.AcceptanceRegistered, got.Status, "the acceptance is never lost")
	assert.Equal(t, 1, got.Attempts)
	assert.Equal(t, t0, got.LastFailureAt)
	assert.Equal(t, t0.Add(2*time.Second), got.NextAttemptAt)
	require.NotNil(t, got.Decision, "the decision is kept for the resumed attempt")

	assert.Zero(t, f.processor.ProcessDue(context.Background()), "not due before the backoff")

	f.clock.Advance(2 * time.Second)
	f.processor.ProcessDue(context.Background())
	got = f.reload(t, a.ID)
	assert.Equal(t, 2, got.Attempts)
	assert.Equal(t, f.clock.Now().Add(4*time.Second), got.NextAttemptAt, "the wait doubles")

	f.clock.Advance(4 * time.Second)
	f.processor.ProcessDue(context.Background())
	got = f.reload(t, a.ID)
	assert.Equal(t, domain.AcceptanceIssuancePending, got.Status)
	assert.Equal(t, 2, got.Attempts)
	assert.Equal(t, int32(1), f.underwriter.calls.Load(), "underwriting ran once")
	assert.True(t, f.issuance.Started(a.ID))
}

func TestBackoffIsCappedAtRetryMax(t *testing.T) {
	f := newProcessorFixture(offerRepo{"o-1": validOffer("o-1", "cust-1")}, 10)
	a := f.accept(t, "o-1", "cust-1")

	for range 4 {
		f.processor.ProcessDue(context.Background())
		f.clock.Advance(time.Minute)
	}
	got := f.reload(t, a.ID)
	assert.Equal(t, 4, got.Attempts)
	assert.Equal(t, got.LastFailureAt.Add(5*time.Second), got.NextAttemptAt)
}

func TestFlakyUnderwritingResumesTheAcceptance(t *testing.T) {
	offers := offerRepo{"demo-flaky-1": validOffer("demo-flaky-1", "cust-1")}
	f := newProcessorFixture(offers, 0)
	a := f.accept(t, "demo-flaky-1", "cust-1")

	for range 3 {
		f.processor.ProcessDue(context.Background())
		f.clock.Advance(time.Minute)
	}

	got := f.reload(t, a.ID)
	assert.Equal(t, domain.AcceptanceIssuancePending, got.Status)
	assert.Equal(t, memory.DefaultFlakyFailures, got.Attempts)
}

func TestAMissingOfferIsRetried(t *testing.T) {
	offers := offerRepo{"o-1": validOffer("o-1", "cust-1")}
	f := newProcessorFixture(offers, 0)
	a := f.accept(t, "o-1", "cust-1")
	delete(offers, "o-1")

	f.processor.ProcessDue(context.Background())

	got := f.reload(t, a.ID)
	assert.Equal(t, domain.AcceptanceRegistered, got.Status)
	assert.Equal(t, 1, got.Attempts)
	assert.Nil(t, got.Decision)
}

func TestRunProcessesOnNotifyAndStopsWithTheContext(t *testing.T) {
	f := newProcessorFixture(offerRepo{"o-1": validOffer("o-1", "cust-1")}, 0)
	processor := issuance.NewProcessor(offerRepo{"o-1": validOffer("o-1", "cust-1")}, f.acceptances, f.underwriter, f.reviews,
		f.issuance, f.clock, issuance.ProcessorConfig{PollInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { processor.Run(ctx); close(done) }()

	svc := issuance.NewAcceptanceService(offerRepo{"o-1": validOffer("o-1", "cust-1")}, f.acceptances, f.clock, processor.Notify)
	a, _, err := svc.Accept(insured("cust-1"), "o-1", domain.ChannelWeb)
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return f.reload(t, a.ID).Status == domain.AcceptanceIssuancePending
	}, 2*time.Second, 10*time.Millisecond, "processed without waiting for the hourly poll")

	processor.Notify()
	processor.Notify() // never blocks
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
}
