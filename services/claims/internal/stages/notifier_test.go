package stages_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/stages"
)

// HU-M3.3-01: the ClaimStageChanged of each change of stage.

var changedAt = time.Date(2026, 9, 5, 15, 2, 0, 0, time.UTC)

func change(status, before domain.Status) domain.StageChange {
	return domain.StageChange{ChangeID: 42, ClaimID: "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90", FilingNumber: "SIN-2026-01655",
		PolicyID: "HG-2025-880231", Subject: "sub-carolina", Channel: domain.ChannelWeb, Status: status,
		PreviousStatus: before, ChangedAt: changedAt}
}

func decode(t *testing.T, ev *ports.OutboxEvent) (events.Envelope, map[string]any) {
	t.Helper()
	require.NotNil(t, ev)
	e, err := events.Decode(ev.Body)
	require.NoError(t, err)
	var p map[string]any
	require.NoError(t, json.Unmarshal(e.Payload, &p))
	return e, p
}

func TestEnteringEvaluationIsTheStageWithItsTimeAndNoAmount(t *testing.T) {
	ev, err := stages.Event(change(domain.StatusInEvaluation, domain.StatusActive))
	require.NoError(t, err)

	e, p := decode(t, ev)
	assert.Equal(t, "claim-stage-changed-42", ev.ID)
	assert.Equal(t, stages.EventTypeClaimStageChanged, ev.Type)
	assert.Equal(t, changedAt, ev.OccurredAt)
	assert.Equal(t, "claim-stage-changed-42", e.EventID)
	assert.Equal(t, "claims", e.Source)
	assert.Equal(t, 1, e.EventVersion)
	assert.Equal(t, map[string]any{
		"change_id": float64(42), "claim_id": "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90", "filing_number": "SIN-2026-01655",
		"policy_id": "HG-2025-880231", "subject": "sub-carolina", "stage": "in_evaluation", "previous_stage": "active",
		"changed_at": "2026-09-05T15:02:00Z",
	}, p)
}

func TestTheApprovalCarriesTheAmountTheInsuredReceives(t *testing.T) {
	c := change(domain.StatusApproved, domain.StatusInEvaluation)
	payout := int64(413_250_000)
	c.PayoutMinor, c.Currency = &payout, "COP"

	ev, err := stages.Event(c)
	require.NoError(t, err)

	_, p := decode(t, ev)
	assert.Equal(t, "approved", p["stage"])
	assert.Equal(t, float64(413_250_000), p["amount_minor"])
	assert.Equal(t, "COP", p["currency"])
	assert.NotContains(t, p, "paid_at")
}

func TestThePaymentCarriesTheAmountAndTheDateOfTheDisbursement(t *testing.T) {
	c := change(domain.StatusPaid, domain.StatusApproved)
	payout := int64(413_250_000)
	paidAt := changedAt.Add(-time.Second)
	c.PayoutMinor, c.Currency, c.PaidAt = &payout, "COP", &paidAt

	ev, err := stages.Event(c)
	require.NoError(t, err)

	_, p := decode(t, ev)
	assert.Equal(t, "paid", p["stage"])
	assert.Equal(t, float64(413_250_000), p["amount_minor"])
	assert.Equal(t, "2026-09-05T15:01:59Z", p["paid_at"])
}

func TestWithoutTheDateOfThePaymentTheChangeGivesIt(t *testing.T) {
	ev, err := stages.Event(change(domain.StatusPaid, domain.StatusApproved))
	require.NoError(t, err)

	_, p := decode(t, ev)
	assert.Equal(t, "2026-09-05T15:02:00Z", p["paid_at"])
	assert.NotContains(t, p, "amount_minor")
}

func TestAChangeThatNotifiesNothingHasNoEvent(t *testing.T) {
	ev, err := stages.Event(change(domain.StatusRejected, domain.StatusInEvaluation))
	require.NoError(t, err)
	assert.Nil(t, ev)
}

type fakeStore struct {
	passes []int
	err    error
	calls  int
}

func (f *fakeStore) NoticePending(_ context.Context, limit int,
	toEvent func(domain.StageChange) (*ports.OutboxEvent, error)) (int, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	if _, err := toEvent(change(domain.StatusInEvaluation, domain.StatusActive)); err != nil {
		return 0, err
	}
	if len(f.passes) == 0 {
		return 0, nil
	}
	n := f.passes[0]
	f.passes = f.passes[1:]
	if n > limit {
		n = limit
	}
	return n, nil
}

func TestAFullBatchIsFollowedByAnotherPassAtOnce(t *testing.T) {
	store := &fakeStore{passes: []int{100, 3}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		stages.NewNotifier(store, time.Hour).Run(ctx)
		close(done)
	}()
	require.Eventually(t, func() bool { return store.calls >= 2 }, time.Second, 5*time.Millisecond)
	cancel()
	<-done
	assert.Equal(t, 2, store.calls, "the second pass found less than a batch and waits for the next tick")
}

func TestAFailingPassIsRetriedOnTheNextTick(t *testing.T) {
	store := &fakeStore{err: errors.New("database down")}
	n := stages.NewNotifier(store, 0)

	_, err := n.ProcessDue(context.Background())
	assert.Error(t, err)
}
