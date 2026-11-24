package httpadapter_test

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/review"
)

// HU-M2.4-01 (criterion 3): the reads of the insured tell a claim resolved
// automatically apart, with the parameter, its threshold and the value that
// made it pay.

var triggerAt = time.Date(2026, 9, 11, 21, 39, 0, 0, time.UTC)

func parametricClaim() domain.ClaimRecord {
	return domain.ClaimRecord{
		Claim: domain.Claim{ID: claimID, FilingNumber: "SIN-2026-01702", Subject: "sub-carolina",
			PolicyID: "VJ-2026-000931", IncidentType: domain.IncidentFlightDelay, OccurredAt: triggerAt,
			Place: "AV-8421", Description: "Resolución automática.", Status: domain.StatusPaid, FiledAt: triggerAt},
		Channel: domain.ChannelParametric,
		Parametric: &domain.ParametricResolution{ObservationID: "obs-1", RuleVersion: domain.ParametricRulesVersion,
			ResolvedAt: triggerAt.Add(30 * time.Second),
			Trigger: domain.Trigger{Parameter: domain.IncidentFlightDelay, EventReference: "AV-8421", Threshold: 180,
				Observed: 215, Unit: "minutes", ObservedAt: triggerAt}},
	}
}

const automaticResolution = `{"resolved_at":"2026-09-11T21:39:30Z","rule_version":"parametric-rules-v1",
	"trigger":{"parameter":"flight_delay","event_reference":"AV-8421","threshold":180,"observed":215,
	"unit":"minutes","observed_at":"2026-09-11T21:39:00Z"}}`

func TestTheReadsOfAParametricClaimCarryItsAutomaticResolution(t *testing.T) {
	reads := &fakeReads{claim: parametricClaim(), page: filing.ClaimPage{Claims: []domain.ClaimRecord{parametricClaim()}}}

	rec := do(webRouterOf(reads), http.MethodGet, "/v1/claims/"+claimID, "insured", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	detail := decode(t, rec)
	assert.Equal(t, "parametric", detail["channel"])
	assert.JSONEq(t, automaticResolution, mustJSON(t, detail["automatic_resolution"]))

	rec = do(webRouterOf(reads), http.MethodGet, "/v1/claims", "insured", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	items := decode(t, rec)["items"].([]any)
	require.Len(t, items, 1)
	assert.JSONEq(t, automaticResolution, mustJSON(t, items[0].(map[string]any)["automatic_resolution"]))

	history := review.History{Claim: parametricClaim()}
	rec = do(reviewRouter(&fakeReviewer{history: history}), http.MethodGet, "/v1/claims/"+claimID+"/history",
		"insured", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decode(t, rec)
	assert.JSONEq(t, automaticResolution, mustJSON(t, body["automatic_resolution"]))
	assert.Equal(t, []any{}, body["requirements"], "a parametric claim asks for no supports")
}

func TestAClaimOfAnAnalystHasNoAutomaticResolution(t *testing.T) {
	reads := &fakeReads{claim: webClaim()}
	rec := do(webRouterOf(reads), http.MethodGet, "/v1/claims/"+claimID, "insured", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decode(t, rec)
	assert.Contains(t, body, "automatic_resolution")
	assert.Nil(t, body["automatic_resolution"])
}
