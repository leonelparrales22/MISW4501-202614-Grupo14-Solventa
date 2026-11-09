package httpadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/evaluation"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/triage"
)

// HU-W2.2-03: lo que el analista de operaciones lee para evaluar un siniestro.

type fakeEvaluation struct {
	page      evaluation.QueuePage
	priority  evaluation.PriorityPage
	detail    evaluation.Evaluation
	err       error
	gotSize   int
	gotToken  string
	gotClaim  string
	gotFilter evaluation.QueueFilter
	callCount int
}

func (f *fakeEvaluation) PriorityQueue(_ context.Context, size int, token string, filter evaluation.QueueFilter) (evaluation.PriorityPage, error) {
	f.callCount++
	f.gotSize, f.gotToken, f.gotFilter = size, token, filter
	return f.priority, f.err
}

func (f *fakeEvaluation) Exceptions(_ context.Context, size int, token string) (evaluation.QueuePage, error) {
	f.callCount++
	f.gotSize, f.gotToken = size, "excepciones:"+token
	return f.page, f.err
}

func (f *fakeEvaluation) Detail(_ context.Context, id string) (evaluation.Evaluation, error) {
	f.callCount++
	f.gotClaim = id
	return f.detail, f.err
}

func evaluationRouter(f *fakeEvaluation) http.Handler {
	return httpadapter.NewRouter(httpadapter.WithEvaluation(f, callers))
}

var radicadoEn = time.Date(2026, 9, 3, 19, 20, 0, 0, time.UTC)

func claimInEvaluation() domain.ClaimRecord {
	amount := int64(480_000_000)
	return domain.ClaimRecord{
		Claim: domain.Claim{
			ID: claimID, FilingNumber: "SIN-2026-01655", Subject: "sub-carolina", PolicyID: "HG-2025-880231",
			IncidentType: domain.IncidentWaterDamage, OccurredAt: radicadoEn.Add(-21 * time.Hour),
			Place: "Carrera 15 # 93-40", Description: "Filtración desde el apartamento superior.",
			Status: domain.StatusInEvaluation, FiledAt: radicadoEn, EstimatedAmountMinor: &amount,
		},
		Channel: domain.ChannelWeb,
	}
}

func TestOnlyOperationsReadsTheEvaluation(t *testing.T) {
	for _, caller := range []string{"insured", "advisor", "partner", ""} {
		f := &fakeEvaluation{}
		h := evaluationRouter(f)

		queue := do(h, http.MethodGet, "/v1/claims/evaluations", caller, nil, nil)
		detail := do(h, http.MethodGet, "/v1/claims/"+claimID+"/evaluation", caller, nil, nil)

		want := http.StatusForbidden
		if caller == "" {
			want = http.StatusUnauthorized
		}
		assert.Equal(t, want, queue.Code, caller)
		assert.Equal(t, want, detail.Code, caller)
		assert.Zero(t, f.callCount, "%s never reaches the service", caller)
	}
}

func TestTheQueueListsTheClaimsWithTheirPriority(t *testing.T) {
	record := triageRecord(1, domain.Triage2, nil, nil, domain.CauseEnteredEvaluation)
	mean := 101 * time.Hour
	f := &fakeEvaluation{priority: evaluation.PriorityPage{
		Items: []evaluation.QueueItem{{
			Claim:  claimInEvaluation(),
			Triage: triage.Summary{ClaimID: claimID, State: domain.TriageAssessed, Current: &record},
			Holder: &domain.Holder{FullName: "Carolina Pérez Ruiz", DocumentType: "CC", DocumentID: "1020345678",
				Email: "carolina@correo.example"},
		}},
		NextPageToken: "siguiente",
		Summary: evaluation.QueueSummary{ByLevel: map[int]int{1: 3, 2: 7, 3: 12}, Unassessed: 1, Total: 23, Matching: 7,
			MeanResolution: &mean, Decided: 9},
	}}

	rec := do(evaluationRouter(f), http.MethodGet,
		"/v1/claims/evaluations?page_size=10&page_token=abc&level=2&line=home&status=in_evaluation", "operations", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, 10, f.gotSize)
	assert.Equal(t, "abc", f.gotToken)
	assert.Equal(t, evaluation.QueueFilter{Level: 2, Line: domain.LineHome, Status: domain.StatusInEvaluation}, f.gotFilter)
	summary := decode(t, do(evaluationRouter(f), http.MethodGet, "/v1/claims/evaluations", "operations", nil, nil))["summary"]
	assert.Equal(t, map[string]any{
		"total": 23.0, "matching": 7.0, "unassessed": 1.0, "mean_resolution_days": 4.2, "decided_in_window": 9.0,
		"window_days": 30.0, "levels": []any{
			map[string]any{"level": 1.0, "claims": 3.0}, map[string]any{"level": 2.0, "claims": 7.0},
			map[string]any{"level": 3.0, "claims": 12.0}},
	}, summary)
	assert.Equal(t, evaluation.QueueFilter{}, f.gotFilter, "without filters, the whole queue")
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "siguiente", body["next_page_token"])
	items := body["items"].([]any)
	require.Len(t, items, 1)
	item := items[0].(map[string]any)
	assert.Equal(t, "SIN-2026-01655", item["filing_number"])
	assert.Equal(t, "in_evaluation", item["status"])
	assert.EqualValues(t, 480_000_000, item["estimated_amount_minor"])
	assert.EqualValues(t, 2, item["triage"].(map[string]any)["level"])
	assert.Equal(t, map[string]any{"full_name": "Carolina Pérez Ruiz", "document_type": "CC", "document_id": "1020345678"},
		item["holder"], "the insured without their contact")
}

func TestTheQueueWithoutDecisionsOrInsuredSaysSo(t *testing.T) {
	f := &fakeEvaluation{priority: evaluation.PriorityPage{
		Items:   []evaluation.QueueItem{{Claim: claimInEvaluation(), Triage: triage.Summary{ClaimID: claimID, State: domain.TriagePending}}},
		Summary: evaluation.QueueSummary{ByLevel: map[int]int{}},
	}}

	body := decode(t, do(evaluationRouter(f), http.MethodGet, "/v1/claims/evaluations?level=none", "operations", nil, nil))

	assert.Equal(t, 4, f.gotFilter.Level, "none is the claims without a level")
	assert.Nil(t, body["next_page_token"])
	assert.Nil(t, body["items"].([]any)[0].(map[string]any)["holder"])
	summary := body["summary"].(map[string]any)
	assert.Nil(t, summary["mean_resolution_days"], "no claim decided: no mean, never a made-up one")
	assert.Equal(t, []any{map[string]any{"level": 1.0, "claims": 0.0}, map[string]any{"level": 2.0, "claims": 0.0},
		map[string]any{"level": 3.0, "claims": 0.0}}, summary["levels"])
}

func TestTheQueueRejectsAnInvalidFilter(t *testing.T) {
	for _, query := range []string{"level=4", "level=0", "level=alta", "line=travel", "line=AUTO", "status=active",
		"status=returned", "page_token=" + strings.Repeat("a", 513)} {
		f := &fakeEvaluation{}
		rec := do(evaluationRouter(f), http.MethodGet, "/v1/claims/evaluations?"+query, "operations", nil, nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code, query)
		assert.Zero(t, f.callCount, query)
	}
	rec := do(evaluationRouter(&fakeEvaluation{err: errors.New("database down")}), http.MethodGet,
		"/v1/claims/evaluations", "operations", nil, nil)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestThePaymentExceptionsAreListedLikeTheQueue(t *testing.T) {
	c := claimInEvaluation()
	c.Status = domain.StatusPaymentException
	f := &fakeEvaluation{page: evaluation.QueuePage{Items: []evaluation.QueueItem{{
		Claim: c, Triage: triage.Summary{ClaimID: claimID, State: domain.TriageNotApplicable},
	}}}}

	rec := do(evaluationRouter(f), http.MethodGet, "/v1/claims/payment-exceptions?page_token=abc", "operations", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "excepciones:abc", f.gotToken, "the exceptions are read, not the queue")
	items := decode(t, rec)["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, "payment_exception", items[0].(map[string]any)["status"])
	assert.Equal(t, http.StatusForbidden,
		do(evaluationRouter(f), http.MethodGet, "/v1/claims/payment-exceptions", "insured", nil, nil).Code)
}

func TestTheDetailCarriesTheDecision(t *testing.T) {
	f := &fakeEvaluation{detail: evaluation.Evaluation{Claim: claimInEvaluation(),
		Triage: triage.View{ClaimID: claimID, State: domain.TriagePending}, Decision: &aprobacionFallida}}

	rec := do(evaluationRouter(f), http.MethodGet, "/v1/claims/"+claimID+"/evaluation", "operations", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	decision := decode(t, rec)["decision"].(map[string]any)
	assert.Equal(t, "payment_exception", decision["status"])
	assert.Equal(t, "invalid_destination_account", decision["payout"].(map[string]any)["gateway_code"])
	assert.NotContains(t, rec.Body.String(), "0123-4567-89", "the full account number never leaves the service")
}

func TestTheQueueRejectsAnInvalidPage(t *testing.T) {
	for _, path := range []string{"/v1/claims/evaluations?page_size=abc", "/v1/claims/evaluations?page_size=0",
		"/v1/claims/evaluations?page_size=-3"} {
		rec := do(evaluationRouter(&fakeEvaluation{}), http.MethodGet, path, "operations", nil, nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code, path)
	}
	rec := do(evaluationRouter(&fakeEvaluation{err: evaluation.ErrInvalidQuery}), http.MethodGet,
		"/v1/claims/evaluations?page_size=99", "operations", nil, nil)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestTheDetailHasEverythingTheAnalystDecidesWith(t *testing.T) {
	coverage := domain.Coverage{Incident: domain.IncidentWaterDamage, InsuredValueMinor: 32_000_000_000, Currency: "COP",
		DeductibleBasisPoints: 500}
	view := assessedView()
	f := &fakeEvaluation{detail: evaluation.Evaluation{
		Claim:  claimInEvaluation(),
		Triage: view,
		Terms: &domain.PolicyTerms{
			Policy: domain.Policy{ID: "HG-2025-880231", Line: domain.LineHome, Product: "Hogar", Status: domain.PolicyActive,
				ValidFrom: radicadoEn.AddDate(-1, 0, 0), ValidTo: radicadoEn.AddDate(0, 6, 0)},
			Coverages: []domain.Coverage{coverage},
		},
		Coverage: &coverage,
		Holder: &domain.Holder{FullName: "Carolina Pérez Ruiz", DocumentType: "CC", DocumentID: "1020345678",
			Email: "carolina@correo.example", Phone: "+57 301 234 5678",
			PayoutAccount: domain.PayoutAccount{BankCode: "1007", BankName: "Banco Aliado", AccountType: "savings",
				AccountNumber: "0123-4567-12"}},
		Supports: []evaluation.SupportLink{{
			Support: domain.AttachedSupport{RequirementCode: "water_photos", File: domain.SupportFile{ID: "sup-1",
				FileName: "techo.jpg", Format: domain.FormatJPEG, ByteSize: 2048, SHA256: "aa"}},
			URL: "https://firmada/sup-1", ExpiresAt: radicadoEn,
		}},
		Evidence: []filing.EvidenceLink{},
	}}

	rec := do(evaluationRouter(f), http.MethodGet, "/v1/claims/"+claimID+"/evaluation", "operations", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, claimID, f.gotClaim)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	claim := body["claim"].(map[string]any)
	assert.Equal(t, "Filtración desde el apartamento superior.", claim["description"])
	assert.EqualValues(t, 480_000_000, claim["estimated_amount_minor"])
	assert.EqualValues(t, map[string]any{"incident": "water_damage", "insured_value_minor": 3.2e10, "currency": "COP",
		"deductible_basis_points": 500.0}, body["coverage"])
	holder := body["holder"].(map[string]any)
	assert.Equal(t, "Carolina Pérez Ruiz", holder["full_name"])
	account := holder["payout_account"].(map[string]any)
	assert.Equal(t, "•••• 6712", account["masked_number"])
	assert.NotContains(t, rec.Body.String(), "0123-4567-12", "the full account number never leaves the service")
	files := body["support_files"].([]any)
	require.Len(t, files, 1)
	assert.Equal(t, "https://firmada/sup-1", files[0].(map[string]any)["download_url"])
	assert.EqualValues(t, 1, body["triage"].(map[string]any)["current"].(map[string]any)["level"])
	assert.Len(t, body["triage"].(map[string]any)["history"], 2)
	assert.Equal(t, false, body["policy_unavailable"])
}

func TestTheDetailSaysWhenThePolicyCoreDidNotAnswer(t *testing.T) {
	f := &fakeEvaluation{detail: evaluation.Evaluation{Claim: claimInEvaluation(), PolicyUnavailable: true,
		Triage: triage.View{ClaimID: claimID, State: domain.TriagePending}}}

	rec := do(evaluationRouter(f), http.MethodGet, "/v1/claims/"+claimID+"/evaluation", "operations", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, true, body["policy_unavailable"])
	assert.Nil(t, body["policy"])
	assert.Nil(t, body["coverage"])
	assert.Nil(t, body["holder"])
}

func TestTheDetailAnswersTheErrorsOfTheRead(t *testing.T) {
	for name, tc := range map[string]struct {
		id   string
		err  error
		want int
	}{
		"bad id":      {"no-es-un-id", nil, http.StatusNotFound},
		"not found":   {claimID, ports.ErrClaimNotFound, http.StatusNotFound},
		"unavailable": {claimID, errors.New("se cayó"), http.StatusServiceUnavailable},
	} {
		rec := do(evaluationRouter(&fakeEvaluation{err: tc.err}), http.MethodGet, "/v1/claims/"+tc.id+"/evaluation",
			"operations", nil, nil)
		assert.Equal(t, tc.want, rec.Code, name)
	}
}
