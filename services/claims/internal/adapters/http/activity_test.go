package httpadapter_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/review"
)

// HU-W2.1-02: el estado del siniestro para el asegurado (A7 y A8).

var cambiadoEn = time.Date(2026, 9, 15, 14, 12, 0, 0, time.UTC)

const soporteID = "sup-0000000000000000000000000000000a"

func TestTheListSaysTheLastChangeAndTalliesTheClaims(t *testing.T) {
	c := domain.ClaimRecord{Claim: domain.Claim{ID: claimID, FilingNumber: "SIN-2026-01877", Status: domain.StatusReturned,
		FiledAt: cambiadoEn.Add(-24 * time.Hour)}, Channel: domain.ChannelWeb, LastChangedAt: cambiadoEn}
	reads := &fakeReads{page: filing.ClaimPage{Claims: []domain.ClaimRecord{c}, Summary: &domain.ClaimTally{
		ByStatus:  map[domain.Status]int{domain.StatusReturned: 1, domain.StatusInEvaluation: 1, domain.StatusPaid: 2},
		PaidSince: 1, PaidAmountMinor: 18_000_000, Currency: "COP",
		PendingAction: []domain.ClaimRef{{ID: claimID, FilingNumber: "SIN-2026-01877"}},
		Policies: []domain.PolicyRef{
			{ID: "AU-2025-118204", Line: domain.LineAuto, Product: "Autos", InsuredObject: "KJT-482"},
			{ID: "AU-2026-000001"},
		}}}}

	rec := do(webRouterOf(reads), http.MethodGet, "/v1/claims", "insured", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decode(t, rec)
	item := body["items"].([]any)[0].(map[string]any)
	assert.Equal(t, cambiadoEn.Format(time.RFC3339), item["last_changed_at"])
	assert.Equal(t, map[string]any{
		"in_progress":    2.0,
		"by_status":      map[string]any{"returned": 1.0, "in_evaluation": 1.0, "paid": 2.0},
		"paid_this_year": map[string]any{"claims": 1.0, "amount_minor": 18_000_000.0, "currency": "COP"},
		"pending_action": []any{map[string]any{"claim_id": claimID, "filing_number": "SIN-2026-01877"}},
		"policies": []any{
			map[string]any{"policy_id": "AU-2025-118204", "line": "auto", "product": "Autos", "insured_object": "KJT-482"},
			map[string]any{"policy_id": "AU-2026-000001", "line": nil, "product": nil, "insured_object": nil},
		},
	}, body["summary"])
}

func TestWithoutActivityTheListHasNoSummaryNorLastChange(t *testing.T) {
	reads := &fakeReads{page: filing.ClaimPage{Claims: []domain.ClaimRecord{{Claim: domain.Claim{ID: claimID}}}}}

	body := decode(t, do(webRouterOf(reads), http.MethodGet, "/v1/claims", "insured", nil, nil))

	assert.NotContains(t, body, "summary")
	assert.NotContains(t, body["items"].([]any)[0].(map[string]any), "last_changed_at")
}

func TestTheDetailSaysTheLastChange(t *testing.T) {
	reads := &fakeReads{claim: domain.ClaimRecord{Claim: domain.Claim{ID: claimID, Status: domain.StatusInEvaluation},
		LastChangedAt: cambiadoEn}}

	body := decode(t, do(webRouterOf(reads), http.MethodGet, "/v1/claims/"+claimID, "insured", nil, nil))

	assert.Equal(t, cambiadoEn.Format(time.RFC3339), body["last_changed_at"])
}

func TestTheInsuredGetsASignedLinkToTheirSupport(t *testing.T) {
	reads := &fakeReads{link: filing.SupportLink{
		File: domain.SupportFile{ID: soporteID, FileName: "croquis.pdf", Format: domain.FormatPDF, ByteSize: 482113},
		URL:  "https://bucket.test/supports/sub-carolina/" + soporteID + "?X-Amz-Signature=x", ExpiresAt: cambiadoEn}}

	rec := do(webRouterOf(reads), http.MethodGet, "/v1/claims/"+claimID+"/supports/"+soporteID+"/download", "insured", nil, nil)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, claimID, reads.gotID)
	assert.Equal(t, soporteID, reads.gotSupport)
	assert.Equal(t, map[string]any{"support_id": soporteID, "file_name": "croquis.pdf", "content_type": "application/pdf",
		"byte_size": 482113.0, "download_url": reads.link.URL, "expires_at": cambiadoEn.Format(time.RFC3339)},
		decode(t, rec))
}

func TestTheDownloadOfASupportAnswersItsErrors(t *testing.T) {
	path := "/v1/claims/" + claimID + "/supports/" + soporteID + "/download"
	for _, tc := range []struct {
		err   error
		code  int
		error string
	}{
		{domain.ErrSupportNotFound, http.StatusNotFound, "support_not_found"},
		{filing.ErrDownloadsUnavailable, http.StatusServiceUnavailable, "downloads_unavailable"},
		{errors.New("cannot sign"), http.StatusServiceUnavailable, "claims_unavailable"},
	} {
		rec := do(webRouterOf(&fakeReads{linkErr: tc.err}), http.MethodGet, path, "insured", nil, nil)
		assert.Equal(t, tc.code, rec.Code, tc.error)
		assert.Equal(t, tc.error, decode(t, rec)["error"])
	}
	for _, bad := range []string{"/v1/claims/no-es-un-id/supports/" + soporteID + "/download",
		"/v1/claims/" + claimID + "/supports/no-es-un-soporte/download"} {
		f := &fakeReads{}
		rec := do(webRouterOf(f), http.MethodGet, bad, "insured", nil, nil)
		assert.Equal(t, http.StatusNotFound, rec.Code, bad)
		assert.Empty(t, f.gotSupport, "an id that is not one never reaches the service")
	}
	for _, caller := range []string{"advisor", "operations", ""} {
		rec := do(webRouterOf(&fakeReads{}), http.MethodGet, path, caller, nil, nil)
		want := http.StatusForbidden
		if caller == "" {
			want = http.StatusUnauthorized
		}
		assert.Equal(t, want, rec.Code, caller)
	}
}

func TestTheHistoryCarriesTheDecisionAndTheProviderForTheInsured(t *testing.T) {
	paid := cambiadoEn.Add(48 * time.Hour)
	f := &fakeReviewer{history: review.History{
		Claim: domain.ClaimRecord{Claim: domain.Claim{ID: claimID, FilingNumber: "SIN-2026-01655", Status: domain.StatusPaid,
			FiledAt: cambiadoEn.Add(-72 * time.Hour)}},
		Decision: &domain.DecisionView{Outcome: domain.OutcomeApproved, DecidedAt: cambiadoEn, AmountMinor: 435_000_000,
			Currency: "COP", PayoutStatus: idempotency.StatusConfirmed, PaidAt: &paid},
		Provider: &domain.ProviderView{Name: "Servicios Hogar Norte", DistanceKm: 2.3, AssignedAt: cambiadoEn.Add(-time.Hour)},
	}}

	body := decode(t, do(reviewRouter(f), http.MethodGet, "/v1/claims/"+claimID+"/history", "insured", nil, nil))

	assert.Equal(t, map[string]any{"outcome": "approved", "decided_at": cambiadoEn.Format(time.RFC3339),
		"amount_minor": 435_000_000.0, "currency": "COP", "payout_status": "confirmed",
		"paid_at": paid.Format(time.RFC3339), "rejection_reason": nil}, body["decision"])
	assert.Equal(t, map[string]any{"name": "Servicios Hogar Norte", "distance_km": 2.3,
		"assigned_at": cambiadoEn.Add(-time.Hour).Format(time.RFC3339)}, body["provider"])

	f.history.Decision = &domain.DecisionView{Outcome: domain.OutcomeRejected, DecidedAt: cambiadoEn, Reason: "No cubierto."}
	f.history.Provider = nil
	body = decode(t, do(reviewRouter(f), http.MethodGet, "/v1/claims/"+claimID+"/history", "insured", nil, nil))
	decision := body["decision"].(map[string]any)
	assert.Nil(t, decision["amount_minor"])
	assert.Nil(t, decision["paid_at"])
	assert.Equal(t, "No cubierto.", decision["rejection_reason"])
	assert.Nil(t, body["provider"])
}
