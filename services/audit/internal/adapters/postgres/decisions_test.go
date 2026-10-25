package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

func decisionRecord(offerID string) domain.DecisionRecord {
	return domain.DecisionRecord{
		OfferID: offerID, CustomerID: "cus-1", PartnerID: "p-1", ApplicationID: "app-1", Channel: "embedded",
		RequestedAt: time.Date(2026, 10, 20, 15, 4, 5, 0, time.UTC), RaterVersion: "v1",
		Sources:      []domain.SourceConsulted{{Source: "open_finance", Status: "ok", LatencyMS: 80}, {Source: "kyc", Status: "timeout", LatencyMS: 700}},
		ProfileScore: 0.4, ProfileCategory: "medium", ProfilePartial: true,
		Rules:   []domain.RuleApplied{{Order: 1, Code: "base_monthly_rate", Value: 0.001}, {Order: 2, Code: "age_factor", Value: 1.2}},
		Premium: 120000.5, Currency: "COP", Decision: domain.DecisionDeferred, DecisionReason: "kyc_unavailable",
		RecordedAt: time.Date(2026, 10, 20, 15, 4, 6, 0, time.UTC),
	}
}

func TestDecisionStoreRoundTripAndIdempotency(t *testing.T) {
	store := postgres.NewDecisionStore(newDB(t))
	ctx := context.Background()

	stored, created, err := store.Create(ctx, decisionRecord("off-1"))
	require.NoError(t, err)
	assert.True(t, created)

	got, err := store.Get(ctx, "off-1")
	require.NoError(t, err)
	assert.Equal(t, stored, got, "what is read is what was stored, rules and sources in order")

	other := decisionRecord("off-1")
	other.Premium = 1
	again, created, err := store.Create(ctx, other)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, stored, again)

	_, err = store.Get(ctx, "missing")
	assert.ErrorIs(t, err, domain.ErrDecisionNotFound)
}

func TestDecisionTableIsAppendOnly(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	_, _, err := postgres.NewDecisionStore(db).Create(ctx, decisionRecord("off-1"))
	require.NoError(t, err)

	for _, stmt := range []string{
		`UPDATE underwriting_decision SET premium = 1`,
		`DELETE FROM underwriting_decision`,
		`TRUNCATE underwriting_decision`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.Error(t, err, stmt)
		assert.Contains(t, err.Error(), "append-only", stmt)
	}
}
