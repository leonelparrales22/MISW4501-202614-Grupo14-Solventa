package httpadapter_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/triage"
)

// The reads of the priority of the claims (HU-W2.2-01).

// fakeTriage is the triage of the tests.
type fakeTriage struct {
	view      triage.View
	summaries []triage.Summary
	err       error
	calls     int
	gotID     string
	gotIDs    []string
	gotCaller security.Claims
}

func (f *fakeTriage) View(ctx context.Context, id string) (triage.View, error) {
	f.calls++
	f.gotID = id
	f.gotCaller, _ = security.ClaimsFromContext(ctx)
	return f.view, f.err
}

func (f *fakeTriage) Summaries(_ context.Context, ids []string) ([]triage.Summary, error) {
	f.calls++
	f.gotIDs = ids
	return f.summaries, f.err
}

func (f *fakeTriage) Policy() domain.TriagePolicy { return domain.TriagePolicyV1() }

func triageRouter(f *fakeTriage) http.Handler {
	return httpadapter.NewRouter(httpadapter.WithTriage(f, callers))
}

var assessedAt = time.Date(2026, 9, 4, 13, 31, 7, 0, time.UTC)

func triageRecord(seq int, level domain.PriorityLevel, previous *domain.PriorityLevel, amount *int64,
	causes ...domain.TriageCause) domain.TriageRecord {
	p := domain.TriagePolicyV1()
	return domain.TriageRecord{
		ClaimID: claimID, Seq: seq, Causes: causes, PreviousLevel: previous, Actor: domain.TriageActorSystem,
		AssessedAt: assessedAt.Add(time.Duration(seq) * time.Hour),
		Assessment: domain.TriageAssessment{
			Level: level, Severity: domain.SeverityMedium, AmountBand: p.BandOf(amount), Target: p.TargetOf(level),
			Input: domain.TriageInput{IncidentType: domain.IncidentWaterDamage, Line: domain.LineHome, Coverage: "water_damage",
				EstimatedAmountMinor: amount, PolicyVersion: p.Version},
		},
	}
}

func assessedView() triage.View {
	low, high := int64(480000000), int64(4000000000)
	two := domain.Triage2
	second := triageRecord(2, domain.Triage1, &two, &high, domain.CauseEstimatedAmountChanged)
	first := triageRecord(1, domain.Triage2, nil, &low, domain.CauseEnteredEvaluation)
	return triage.View{ClaimID: claimID, State: domain.TriageAssessed, Current: &second,
		History: []domain.TriageRecord{second, first}}
}

func getTriage(h http.Handler, token, id string) *httptest.ResponseRecorder {
	return do(h, http.MethodGet, "/v1/claims/"+id+"/triage", token, nil, nil)
}

func TestOperationsReadsTheWholeTriageOfAClaim(t *testing.T) {
	f := &fakeTriage{view: assessedView()}

	rec := getTriage(triageRouter(f), "operations", claimID)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, claimID, f.gotID)
	assert.JSONEq(t, `{
		"view": "operations", "claim_id": "`+claimID+`", "state": "assessed",
		"current": {"level": 1, "severity": "medium", "amount_band": "high", "estimated_amount_minor": 4000000000,
			"currency": "COP", "incident_type": "water_damage", "coverage": "water_damage", "policy_version": "triage-policy-v1",
			"target": {"kind": "immediate", "business_days": null, "resolution_business_days": 2},
			"assessed_at": "2026-09-04T15:31:07Z"},
		"history": [
			{"level": 1, "previous_level": 2, "severity": "medium", "amount_band": "high", "estimated_amount_minor": 4000000000,
			 "incident_type": "water_damage", "causes": ["estimated_amount_changed"], "actor": "system",
			 "policy_version": "triage-policy-v1", "assessed_at": "2026-09-04T15:31:07Z"},
			{"level": 2, "previous_level": null, "severity": "medium", "amount_band": "low", "estimated_amount_minor": 480000000,
			 "incident_type": "water_damage", "causes": ["entered_evaluation"], "actor": "system",
			 "policy_version": "triage-policy-v1", "assessed_at": "2026-09-04T14:31:07Z"}
		]}`, rec.Body.String())
}

func TestTheInsuredReadsTheLevelAndTheResolutionTimeOnly(t *testing.T) {
	current := triageRecord(1, domain.Triage2, nil, nil, domain.CauseEnteredEvaluation)
	f := &fakeTriage{view: triage.View{ClaimID: claimID, State: domain.TriageAssessed, Current: &current,
		History: []domain.TriageRecord{current}}}

	rec := getTriage(triageRouter(f), "insured", claimID)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"view": "insured", "claim_id": "`+claimID+`", "state": "assessed", "level": 2,
		"estimated_resolution_business_days": 5, "assessed_at": "2026-09-04T14:31:07Z"}`, rec.Body.String())
	assert.Equal(t, "sub-carolina", f.gotCaller.Subject, "the service checks whose claim it is")
	for _, hidden := range []string{"severity", "amount", "history", "causes"} {
		assert.NotContains(t, rec.Body.String(), hidden)
	}
}

func TestAClaimWaitingForItsLevelIsPending(t *testing.T) {
	f := &fakeTriage{view: triage.View{ClaimID: claimID, State: domain.TriagePending}}

	rec := getTriage(triageRouter(f), "operations", claimID)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"view": "operations", "claim_id": "`+claimID+`", "state": "pending", "current": null,
		"history": []}`, rec.Body.String())

	rec = getTriage(triageRouter(f), "insured", claimID)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"view": "insured", "claim_id": "`+claimID+`", "state": "pending", "level": null,
		"estimated_resolution_business_days": null, "assessed_at": null}`, rec.Body.String())
}

func TestTheTriageOfAClaimAnswersEveryRefusal(t *testing.T) {
	cases := []struct {
		name   string
		id     string
		err    error
		status int
		code   string
	}{
		{"another insured's", claimID, security.ErrForbidden, http.StatusForbidden, ""},
		{"unknown", claimID, ports.ErrClaimNotFound, http.StatusNotFound, "claim_not_found"},
		{"down", claimID, errors.New("the database is down"), http.StatusServiceUnavailable, "claims_unavailable"},
		{"not an id", "not-a-claim-id", nil, http.StatusNotFound, "claim_not_found"},
		// The router itself answers a path that climbs: 404, with no body of ours.
		{"climbs", "../../etc", nil, http.StatusNotFound, ""},
		{"too long", claimID + "0", nil, http.StatusNotFound, "claim_not_found"},
	}
	for _, c := range cases {
		f := &fakeTriage{err: c.err}
		rec := getTriage(triageRouter(f), "insured", c.id)
		assert.Equal(t, c.status, rec.Code, c.name)
		if c.code != "" {
			assert.Equal(t, c.code, decode(t, rec)["error"], c.name)
		}
		if c.err == nil {
			assert.Zero(t, f.calls, "%s: never reaches the service", c.name)
		}
	}
}

func batchPath(ids ...string) string {
	if len(ids) == 0 {
		return "/v1/claims/triage"
	}
	return "/v1/claims/triage?claim_id=" + strings.Join(ids, "&claim_id=")
}

func claimIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("8f14e45f-ceea-467a-9f3a-%012x", i)
	}
	return ids
}

func TestTheTriageOfAPageComesInTheOrderAsked(t *testing.T) {
	current := triageRecord(1, domain.Triage3, nil, nil, domain.CauseEnteredEvaluation)
	current.Assessment.Severity, current.Assessment.AmountBand = domain.SeverityLow, domain.AmountLow
	ids := claimIDs(3)
	f := &fakeTriage{summaries: []triage.Summary{
		{ClaimID: ids[0], State: domain.TriageAssessed, Current: &current},
		{ClaimID: ids[1], State: domain.TriagePending},
		{ClaimID: ids[2], State: domain.TriageNotFound},
	}}

	rec := do(triageRouter(f), http.MethodGet, batchPath(ids...), "operations", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, ids, f.gotIDs)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.JSONEq(t, `{"items": [
		{"claim_id": "`+ids[0]+`", "state": "assessed", "level": 3, "severity": "low", "amount_band": "low",
		 "target": {"kind": "business_days", "business_days": 10, "resolution_business_days": 10},
		 "assessed_at": "2026-09-04T14:31:07Z"},
		{"claim_id": "`+ids[1]+`", "state": "pending", "level": null, "severity": null, "amount_band": null,
		 "target": null, "assessed_at": null},
		{"claim_id": "`+ids[2]+`", "state": "not_found", "level": null, "severity": null, "amount_band": null,
		 "target": null, "assessed_at": null}
	]}`, rec.Body.String())
}

func TestTheBatchTakesBetweenOneAndFiftyClaimIds(t *testing.T) {
	f := &fakeTriage{}
	h := triageRouter(f)

	rec := do(h, http.MethodGet, batchPath(claimIDs(1)...), "operations", nil, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	rec = do(h, http.MethodGet, batchPath(claimIDs(50)...), "operations", nil, nil)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Len(t, f.gotIDs, 50)
	calls := f.calls

	refused := map[string]string{
		"none":        batchPath(),
		"51":          batchPath(claimIDs(51)...),
		"repeated":    batchPath(claimIDs(1)[0], claimIDs(1)[0]),
		"not an id":   batchPath(claimIDs(1)[0], "../etc"),
		"10 KB long":  batchPath(strings.Repeat("a", 10*1024)),
		"other param": "/v1/claims/triage?id=" + claimIDs(1)[0],
	}
	for name, path := range refused {
		rec := do(h, http.MethodGet, path, "operations", nil, nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code, name)
		assert.Equal(t, "invalid_request", decode(t, rec)["error"], name)
	}
	assert.Equal(t, calls, f.calls, "a refused batch never reaches the service")

	f.err = errors.New("the database is down")
	rec = do(h, http.MethodGet, batchPath(claimIDs(1)...), "operations", nil, nil)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestThePolicyInForceIsServed(t *testing.T) {
	rec := do(triageRouter(&fakeTriage{}), http.MethodGet, "/v1/claims/triage-policy", "operations", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	body := decode(t, rec)
	assert.Equal(t, "triage-policy-v1", body["version"])
	assert.Equal(t, "COP", body["currency"])
	assert.Equal(t, map[string]any{"low_below_minor": 500000000.0, "high_above_minor": 3000000000.0}, body["amount_thresholds"])
	assert.Len(t, body["severity"], 6)
	assert.Contains(t, body["severity"], map[string]any{"line": "auto", "incident_type": "glass", "severity": "low"})
	assert.Equal(t, []any{
		map[string]any{"level": 1.0, "target": map[string]any{"kind": "immediate", "business_days": nil,
			"resolution_business_days": 2.0}},
		map[string]any{"level": 2.0, "target": map[string]any{"kind": "business_days", "business_days": 5.0,
			"resolution_business_days": 5.0}},
		map[string]any{"level": 3.0, "target": map[string]any{"kind": "business_days", "business_days": 10.0,
			"resolution_business_days": 10.0}},
	}, body["targets"])
}
