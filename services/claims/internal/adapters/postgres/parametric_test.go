package postgres_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// HU-M2.4-01: the claim the parametric engine resolves, its payout and the
// ParametricPayoutCompleted event of the outbox.

var flightObservedAt = time.Date(2026, 9, 11, 21, 39, 0, 0, time.UTC)

const parametricClaim = "6f1c2a7e-3b4d-4e5f-8a9b-0c1d2e3f4a5b"

func flightObservation() domain.Observation {
	return domain.Observation{ID: "obs-av8421", Subject: "sub-a", PolicyID: "VJ-2026-000931",
		Parameter: domain.IncidentFlightDelay, EventReference: "AV-8421", Observed: 215, Unit: "minutes",
		ObservedAt: flightObservedAt}
}

// resolution builds what the engine stores for the observation.
func resolution(t *testing.T, id string, o domain.Observation) (domain.ClaimRecord, domain.ParametricResolution, domain.Decision) {
	t.Helper()
	terms := domain.PolicyTerms{
		Policy: domain.Policy{ID: o.PolicyID, Line: domain.LineTravel, Product: "Viaje", InsuredObject: "Viajes nacionales",
			Status: domain.PolicyActive, ValidFrom: flightObservedAt.AddDate(0, -1, 0), ValidTo: flightObservedAt.AddDate(0, 11, 0),
			CoveredIncidents: []domain.IncidentType{domain.IncidentFlightDelay}},
		Coverages: []domain.Coverage{{Incident: domain.IncidentFlightDelay, InsuredValueMinor: 500_000_000, Currency: "COP"}},
	}
	holder := domain.Holder{PayoutAccount: domain.PayoutAccount{BankCode: "1007", BankName: "Banco Aliado",
		AccountType: "savings", AccountNumber: "0123-4567-12"}}
	c, r, d, err := domain.ResolveParametric(id, o, domain.DefaultParametricRules(), terms, holder,
		flightObservedAt.Add(30*time.Second))
	require.NoError(t, err)
	return c, r, d
}

func TestTheEngineFilesAndApprovesTheClaimInOneGo(t *testing.T) {
	s := newWebStores(t)
	ctx := context.Background()
	c, r, d := resolution(t, parametricClaim, flightObservation())

	stored, created, err := s.claims.Resolve(ctx, c, r, d)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, parametricClaim, stored.ID)
	assert.Regexp(t, `^SIN-2026-\d{5}$`, stored.FilingNumber)
	assert.Equal(t, domain.ChannelParametric, stored.Channel)
	assert.Equal(t, domain.StatusApproved, stored.Status)
	require.NotNil(t, stored.Parametric)
	assert.Equal(t, r, *stored.Parametric)

	// The approval is the one of the engine, with its payout due.
	decision, err := postgres.DecisionStoreOf(s.claims).Decision(ctx, parametricClaim)
	require.NoError(t, err)
	assert.Equal(t, domain.ParametricEngine, decision.DecidedBy)
	assert.Equal(t, int64(18_000_000), decision.Payout.AmountMinor)
	assert.Equal(t, idempotency.StatusPending, decision.Payout.Status)

	// The timeline: born in evaluation, approved at once.
	history, err := postgres.ReviewStoreOf(s.claims).History(ctx, parametricClaim)
	require.NoError(t, err)
	require.Len(t, history, 2)
	assert.Equal(t, domain.StatusInEvaluation, history[0].Status)
	assert.Equal(t, domain.StatusApproved, history[1].Status)

	// The list of the insured tells it apart too.
	list, err := s.claims.ListByOwner(ctx, "sub-a", ports.ClaimFilter{}, nil, 10)
	require.NoError(t, err)
	require.Len(t, list, 1)
	require.NotNil(t, list[0].Parametric)
	assert.Equal(t, float64(215), list[0].Parametric.Trigger.Observed)
}

func TestTheSameObservationAgainFilesNothingNew(t *testing.T) {
	s := newWebStores(t)
	ctx := context.Background()
	c, r, d := resolution(t, parametricClaim, flightObservation())
	first, _, err := s.claims.Resolve(ctx, c, r, d)
	require.NoError(t, err)

	// A redelivery draws a new id: the observation finds the first claim.
	c2, r2, d2 := resolution(t, "a0000000-0000-4000-8000-000000000001", flightObservation())
	again, created, err := s.claims.Resolve(ctx, c2, r2, d2)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, first.FilingNumber, again.FilingNumber)

	var claims int
	require.NoError(t, s.db.QueryRow(`SELECT count(*) FROM claim`).Scan(&claims))
	assert.Equal(t, 1, claims)
}

func TestTheConfirmedPayoutWritesTheEventOfNotifications(t *testing.T) {
	s := newWebStores(t)
	ctx := context.Background()
	c, r, d := resolution(t, parametricClaim, flightObservation())
	stored, _, err := s.claims.Resolve(ctx, c, r, d)
	require.NoError(t, err)

	decisions := postgres.DecisionStoreOf(s.claims)
	now := flightObservedAt.Add(time.Minute)
	claimed, ok, err := decisions.Claim(ctx, parametricClaim, now, 30*time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	paidAt := now.Add(250 * time.Millisecond)
	_, err = decisions.Confirm(ctx, claimed, paidAt, idempotency.Outcome{Reference: "po-1"})
	require.NoError(t, err)

	got, err := s.claims.Get(ctx, parametricClaim)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusPaid, got.Status)

	messages, err := postgres.OutboxStoreOf(s.claims).Claim(ctx, paidAt, 30*time.Second, 10)
	require.NoError(t, err)
	require.Len(t, messages, 1)
	e, err := events.Decode(messages[0].Body)
	require.NoError(t, err)
	assert.Equal(t, "parametric-payout-completed-ppo-"+parametricClaim, e.EventID)
	assert.Equal(t, "ParametricPayoutCompleted", e.EventType)
	assert.Equal(t, 1, e.EventVersion)
	assert.Equal(t, "claims", e.Source)
	assert.Equal(t, paidAt, e.OccurredAt)

	// The payload of docs/events/README.md, as notifications reads it.
	var payload map[string]any
	require.NoError(t, json.Unmarshal(e.Payload, &payload))
	assert.Equal(t, map[string]any{
		"payout_id": "ppo-" + parametricClaim, "claim_id": parametricClaim, "filing_number": stored.FilingNumber,
		"policy_id": "VJ-2026-000931", "subject": "sub-a", "amount_minor": float64(18_000_000), "currency": "COP",
		"trigger": map[string]any{"parameter": "flight_delay", "event_reference": "AV-8421", "threshold": float64(180),
			"observed": float64(215), "unit": "minutes", "observed_at": "2026-09-11T21:39:00.000000Z"},
		"payment_method":  map[string]any{"brand": "Banco Aliado", "last4": "6712"},
		"idempotency_key": domain.PayoutKey(parametricClaim),
		"paid_at":         paidAt.Format("2006-01-02T15:04:05.000000Z"),
	}, payload)
}

func TestTheConfirmedPayoutOfAnAnalystWritesNoEvent(t *testing.T) {
	s := newDecisionStores(t)
	ctx := context.Background()
	_, err := s.decisions.Decide(ctx, approval(t))
	require.NoError(t, err)
	claimed, ok, err := s.decisions.Claim(ctx, decidedClaim, decidedAt, 30*time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	_, err = s.decisions.Confirm(ctx, claimed, decidedAt.Add(time.Second), idempotency.Outcome{Reference: "po-1"})
	require.NoError(t, err)

	messages, err := postgres.OutboxStoreOf(s.claims).Claim(ctx, decidedAt.Add(time.Minute), 30*time.Second, 10)
	require.NoError(t, err)
	assert.Empty(t, messages)
}

func TestTheOutboxIsPublishedOnceByTheRelayThatHoldsIt(t *testing.T) {
	s := newWebStores(t)
	ctx := context.Background()
	c, r, d := resolution(t, parametricClaim, flightObservation())
	_, _, err := s.claims.Resolve(ctx, c, r, d)
	require.NoError(t, err)
	decisions := postgres.DecisionStoreOf(s.claims)
	now := flightObservedAt.Add(time.Minute)
	claimed, _, err := decisions.Claim(ctx, parametricClaim, now, 30*time.Second)
	require.NoError(t, err)
	_, err = decisions.Confirm(ctx, claimed, now, idempotency.Outcome{Reference: "po-1"})
	require.NoError(t, err)

	outbox := postgres.OutboxStoreOf(s.claims)
	first, err := outbox.Claim(ctx, now, 30*time.Second, 10)
	require.NoError(t, err)
	require.Len(t, first, 1)
	held, err := outbox.Claim(ctx, now.Add(time.Second), 30*time.Second, 10)
	require.NoError(t, err)
	assert.Empty(t, held, "a message with a live lease is not handed to another relay")

	require.NoError(t, outbox.MarkPublished(ctx, first[0], now.Add(time.Second)))
	after, err := outbox.Claim(ctx, now.Add(time.Hour), 30*time.Second, 10)
	require.NoError(t, err)
	assert.Empty(t, after, "a published message is not published again")
}
