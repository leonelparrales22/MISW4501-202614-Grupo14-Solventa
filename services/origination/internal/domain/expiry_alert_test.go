package domain_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// HU-M3.2-01: the alerts 30, 15 and 5 days before a policy expires.

var cot = time.FixedZone("COT", -5*3600)

// alertNow is 10:00 in Bogotá on 14/11/2026.
var alertNow = time.Date(2026, 11, 14, 10, 0, 0, 0, cot)

func corePolicy(end time.Time) domain.WalletPolicy {
	return domain.WalletPolicy{ID: "AU-2025-118204", Source: domain.SourcePolicyCore, Line: domain.LineAuto,
		Product: "Autos", InsuredObject: "KJT-482", Status: domain.PolicyActive, CoverageEnd: end}
}

func TestTheDaysLeftAreCalendarDaysInColombia(t *testing.T) {
	for name, tc := range map[string]struct {
		now, end time.Time
		want     int
	}{
		"same day, later":                 {alertNow, time.Date(2026, 11, 14, 23, 0, 0, 0, cot), 0},
		"next day, earlier hour":          {alertNow, time.Date(2026, 11, 15, 1, 0, 0, 0, cot), 1},
		"16 days at midnight UTC":         {alertNow, time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC), 15},
		"a UTC date ahead of Colombia's":  {time.Date(2026, 11, 15, 2, 0, 0, 0, time.UTC), time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC), 1},
		"yesterday":                       {alertNow, time.Date(2026, 11, 13, 23, 0, 0, 0, cot), -1},
		"thirty days at the end of a day": {alertNow, time.Date(2026, 12, 14, 23, 59, 59, 0, cot), 30},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, domain.ExpiryDaysLeft(tc.now, tc.end))
		})
	}
}

func TestTheMilestoneIsTheLastOneCrossed(t *testing.T) {
	for days, want := range map[int]int{30: 30, 29: 30, 16: 30, 15: 15, 14: 15, 6: 15, 5: 5, 1: 5, 0: 5} {
		got, ok := domain.ExpiryMilestone(days)
		require.True(t, ok, days)
		assert.Equal(t, want, got, "%d days left", days)
	}
	for _, days := range []int{31, 90, -1} {
		_, ok := domain.ExpiryMilestone(days)
		assert.False(t, ok, "%d days left has no milestone", days)
	}
}

func TestAnActivePolicyThatCrossedAMilestoneIsAlerted(t *testing.T) {
	end := time.Date(2026, 11, 28, 23, 59, 59, 0, cot) // 14 days left

	e, ok := domain.NewPolicyExpiryApproaching(corePolicy(end), "sub-a", alertNow)

	require.True(t, ok)
	assert.Equal(t, 15, e.MilestoneDays, "a policy first seen at 14 days gets only the 15-day alert")
	assert.Equal(t, 14, e.DaysLeft)
	assert.Equal(t, "sub-a", e.Subject)
	assert.Equal(t, end.UTC(), e.CoverageEnd)
	assert.Equal(t, alertNow.UTC(), e.DetectedAt)
	assert.Equal(t, "policy-expiry-approaching-AU-2025-118204-2026-11-28-15", e.EventID())
	assert.NoError(t, e.Validate())
}

func TestNoAlertForAPolicyFarFromItsEndEndedOrNotActive(t *testing.T) {
	far := corePolicy(alertNow.AddDate(0, 0, 45))
	ended := corePolicy(alertNow.Add(-time.Minute))
	cancelled := corePolicy(alertNow.AddDate(0, 0, 3))
	cancelled.Status = domain.PolicyCancelled
	for name, p := range map[string]domain.WalletPolicy{"far": far, "ended": ended, "cancelled": cancelled} {
		_, ok := domain.NewPolicyExpiryApproaching(p, "sub-a", alertNow)
		assert.False(t, ok, name)
	}
}

func TestAnAlertNeedsWhatTheNotificationShows(t *testing.T) {
	valid, ok := domain.NewPolicyExpiryApproaching(corePolicy(alertNow.AddDate(0, 0, 4)), "sub-a", alertNow)
	require.True(t, ok)
	for name, change := range map[string]func(*domain.PolicyExpiryApproaching){
		"no policy":          func(e *domain.PolicyExpiryApproaching) { e.PolicyID = "" },
		"no subject":         func(e *domain.PolicyExpiryApproaching) { e.Subject = "" },
		"an unknown source":  func(e *domain.PolicyExpiryApproaching) { e.Source = "partner" },
		"no line":            func(e *domain.PolicyExpiryApproaching) { e.Line = "" },
		"no end":             func(e *domain.PolicyExpiryApproaching) { e.CoverageEnd = time.Time{} },
		"no detection":       func(e *domain.PolicyExpiryApproaching) { e.DetectedAt = time.Time{} },
		"not a milestone":    func(e *domain.PolicyExpiryApproaching) { e.MilestoneDays = 10 },
		"beyond its window":  func(e *domain.PolicyExpiryApproaching) { e.DaysLeft = 6 },
		"negative days left": func(e *domain.PolicyExpiryApproaching) { e.DaysLeft = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			e := valid
			change(&e)
			assert.ErrorIs(t, e.Validate(), domain.ErrInvalidExpiryAlert)
			_, err := e.Envelope()
			assert.Error(t, err)
		})
	}
}

func TestTheEventCarriesTheContractOfTheAlert(t *testing.T) {
	end := time.Date(2026, 12, 14, 5, 0, 0, 0, time.UTC) // 14/12 at midnight in Bogotá: 30 days left
	e, ok := domain.NewPolicyExpiryApproaching(corePolicy(end), "sub-a", alertNow)
	require.True(t, ok)

	env, err := e.Envelope()
	require.NoError(t, err)
	body, err := env.Encode()
	require.NoError(t, err)
	decoded, err := events.Decode(body)
	require.NoError(t, err)

	assert.Equal(t, "policy-expiry-approaching-AU-2025-118204-2026-12-14-30", decoded.EventID)
	assert.Equal(t, "PolicyExpiryApproaching", decoded.EventType)
	assert.Equal(t, 1, decoded.EventVersion)
	assert.Equal(t, "origination", decoded.Source)
	assert.Equal(t, alertNow.UTC(), decoded.OccurredAt)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(decoded.Payload, &payload))
	assert.Equal(t, map[string]any{
		"policy_id": "AU-2025-118204", "subject": "sub-a", "source": "policy_core", "line": "auto",
		"product": "Autos", "insured_object": "KJT-482", "coverage_end": "2026-12-14T05:00:00Z",
		"milestone_days": float64(30), "days_left": float64(30), "detected_at": "2026-11-14T15:00:00Z",
	}, payload)
}

func TestALifePolicyOfOriginationHasNoProductNorObject(t *testing.T) {
	p := domain.Policy{ID: "pol-acc-1", CustomerID: "sub-b", Premium: 1, CoverageEnd: alertNow.AddDate(0, 0, 5)}
	e, ok := domain.NewPolicyExpiryApproaching(domain.WalletPolicyOf(p, alertNow), p.CustomerID, alertNow)
	require.True(t, ok)

	env, err := e.Envelope()
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(env.Payload, &payload))

	assert.Equal(t, "origination", payload["source"])
	assert.Equal(t, "life", payload["line"])
	assert.Contains(t, payload, "product")
	assert.Nil(t, payload["product"], "null, not missing")
	assert.Nil(t, payload["insured_object"])
	assert.Equal(t, float64(5), payload["milestone_days"])
}
