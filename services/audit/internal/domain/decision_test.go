package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

func validDecision() domain.DecisionRecord {
	return domain.DecisionRecord{
		OfferID: "off-1", CustomerID: "cus-1", PartnerID: "partner-1", ApplicationID: "app-1", Channel: "embedded",
		RequestedAt: time.Date(2026, 10, 20, 15, 4, 5, 0, time.UTC), RaterVersion: "v1",
		Sources:      []domain.SourceConsulted{{Source: "open_finance", Status: "ok", LatencyMS: 80}},
		ProfileScore: 0.4, ProfileCategory: "medium",
		Rules:   []domain.RuleApplied{{Order: 1, Code: "base_monthly_rate", Value: 0.001}},
		Premium: 120000, Currency: "COP", Decision: domain.DecisionAccepted,
	}
}

func TestDecisionRecordValidate(t *testing.T) {
	require.NoError(t, validDecision().Validate())

	deferred := validDecision()
	deferred.Decision, deferred.DecisionReason = domain.DecisionDeferred, "open_finance_unavailable"
	require.NoError(t, deferred.Validate())
}

func TestDecisionRecordValidateReportsEveryInvalidField(t *testing.T) {
	r := validDecision()
	r.OfferID, r.Rules, r.Currency, r.Decision = "", nil, "CO", "maybe"
	r.RequestedAt = time.Time{}

	err := r.Validate()
	var verr *domain.ValidationError
	require.ErrorAs(t, err, &verr)
	got := map[string]string{}
	for _, f := range verr.Fields {
		got[f.Field] = f.Code
	}
	assert.Equal(t, map[string]string{
		"offer_id": "required", "rules": "required", "currency": "invalid", "decision": "invalid", "requested_at": "required",
	}, got)
}

func TestDecisionRecordRejectedOrDeferredNeedsAReason(t *testing.T) {
	r := validDecision()
	r.Decision = domain.DecisionRejected
	var verr *domain.ValidationError
	require.ErrorAs(t, r.Validate(), &verr)
	assert.Equal(t, []domain.FieldError{{Field: "decision_reason", Code: "required"}}, verr.Fields)
}

func TestDecisionRecordValidateRejectsOutOfRangeNumbers(t *testing.T) {
	cases := map[string]func(*domain.DecisionRecord){
		"sources": func(r *domain.DecisionRecord) { r.Sources[0].LatencyMS = 3_600_001 },
		"rules":   func(r *domain.DecisionRecord) { r.Rules[0].Order = 0 },
		"premium": func(r *domain.DecisionRecord) { r.Premium = 1e13 },
		"profile": func(r *domain.DecisionRecord) { r.ProfileScore = 1.5 },
	}
	for field, mutate := range cases {
		r := validDecision()
		mutate(&r)
		var verr *domain.ValidationError
		require.ErrorAs(t, r.Validate(), &verr, field)
		assert.Equal(t, field, verr.Fields[0].Field)
	}
}
