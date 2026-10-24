package main

import (
	"bytes"
	"context"
	"log"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

type recordedNotices struct {
	mu   sync.Mutex
	sent []domain.ReviewOutcomeNotice
}

func (r *recordedNotices) NotifyReviewOutcome(_ context.Context, n domain.ReviewOutcomeNotice) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, n)
	return nil
}

func (r *recordedNotices) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

// HU-W1.1-02: the inbox shares the acceptance adapters, the adjusted offer is
// approved by the decorated underwriting and the notice is sent by the worker.
func TestNewAcceptanceServiceWiresTheInbox(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	notices := &recordedNotices{}
	issued := memory.NewIssuanceStub()
	acceptances, reviews := newAcceptanceService(ctx, acceptanceConfig{reviewScoreThreshold: memory.DefaultReviewScoreThreshold}, issued, notices)
	insured := security.WithClaims(ctx, security.Claims{Subject: "cust-1", Email: "cust-1@example.com", Roles: []security.Role{security.RoleInsured}})
	advisor := security.WithClaims(ctx, security.Claims{Subject: "adv-1", Email: "adv-1@example.com", Roles: []security.Role{security.RoleAdvisor}})

	a, _, err := acceptances.Accept(insured, "demo-review-wiring", domain.ChannelWeb)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got, err := acceptances.Get(insured, a.ID)
		return err == nil && got.Status == domain.AcceptanceInReview
	}, 3*time.Second, 10*time.Millisecond)

	cases, counts, err := reviews.List(advisor, domain.ReviewFilter{Status: domain.ReviewPending})
	require.NoError(t, err)
	require.Len(t, cases, 1)
	assert.Equal(t, a.ID, cases[0].Request.AcceptanceID)
	assert.Equal(t, 1, counts.Pending)

	p := 10.0
	c, err := reviews.Decide(advisor, a.ID, domain.DecisionInput{Type: domain.DecisionAdjust, Observations: "Recargo", PremiumAdjustmentPct: &p})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return notices.count() == 1 }, 3*time.Second, 10*time.Millisecond)

	adjusted, _, err := acceptances.Accept(insured, c.Decision.AdjustedOffer.ID, domain.ChannelWeb)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got, err := acceptances.Get(insured, adjusted.ID)
		return err == nil && got.Status == domain.AcceptanceIssuancePending
	}, 3*time.Second, 10*time.Millisecond, "the adjusted offer does not go back to the inbox")
	assert.True(t, issued.Started(adjusted.ID))
}

// Fix C5: the inbox is the one newStores gives (PostgreSQL with DATABASE_URL),
// shared by the processor that enqueues and the advisor who decides.
func TestNewAcceptanceServiceUsesTheInboxItIsGiven(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	inbox := memory.NewReviewQueueStub()
	cfg := acceptanceConfig{reviewScoreThreshold: memory.DefaultReviewScoreThreshold, reviewStore: inbox}
	acceptances, _ := newAcceptanceService(ctx, cfg, memory.NewIssuanceStub(), nil)
	insured := security.WithClaims(ctx, security.Claims{Subject: "cust-1", Roles: []security.Role{security.RoleInsured}})

	a, _, err := acceptances.Accept(insured, "demo-review-given", domain.ChannelWeb)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		c, err := inbox.Get(ctx, a.ID)
		return err == nil && c.Status == domain.ReviewPending
	}, 3*time.Second, 10*time.Millisecond, "the processor enqueues in the given inbox")
}

func TestNewAcceptanceServiceWithoutEmailSendsNoNotice(t *testing.T) {
	var buf bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(prev)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	acceptances, reviews := newAcceptanceService(ctx, acceptanceConfig{reviewScoreThreshold: memory.DefaultReviewScoreThreshold},
		memory.NewIssuanceStub(), nil)
	assert.Contains(t, buf.String(), "EMAIL_URL is not set: the review outcome e-mails (HU-W1.1-02) are not sent")

	insured := security.WithClaims(ctx, security.Claims{Subject: "cust-1", Roles: []security.Role{security.RoleInsured}})
	advisor := security.WithClaims(ctx, security.Claims{Subject: "adv-1", Roles: []security.Role{security.RoleAdvisor}})
	a, _, err := acceptances.Accept(insured, "demo-highrisk-wiring", domain.ChannelWeb)
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		got, err := acceptances.Get(insured, a.ID)
		return err == nil && got.Status == domain.AcceptanceInReview
	}, 3*time.Second, 10*time.Millisecond)

	c, err := reviews.Decide(advisor, a.ID, domain.DecisionInput{Type: domain.DecisionReject, Observations: "Fuera de política"})
	require.NoError(t, err)
	assert.Nil(t, c.Notification)
}
