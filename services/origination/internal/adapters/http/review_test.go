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
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/underwriting"
)

type fakeReviews struct {
	cases  []domain.ReviewCase
	counts domain.ReviewCounts
	detail underwriting.ReviewDetail
	err    error

	gotFilter domain.ReviewFilter
	gotID     string
	gotInput  domain.DecisionInput
	calls     int
}

func (f *fakeReviews) List(_ context.Context, filter domain.ReviewFilter) ([]domain.ReviewCase, domain.ReviewCounts, error) {
	f.calls++
	f.gotFilter = filter
	return f.cases, f.counts, f.err
}

func (f *fakeReviews) Get(_ context.Context, id string) (underwriting.ReviewDetail, error) {
	f.calls++
	f.gotID = id
	return f.detail, f.err
}

func (f *fakeReviews) Decide(_ context.Context, id string, in domain.DecisionInput) (domain.ReviewCase, error) {
	f.calls++
	f.gotID, f.gotInput = id, in
	if len(f.cases) == 0 {
		return domain.ReviewCase{}, f.err
	}
	return f.cases[0], f.err
}

func callReviews(t *testing.T, svc *fakeReviews, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithReviews(svc, tokenVerifier{})).ServeHTTP(rec, req)
	return rec
}

var received = time.Date(2026, 10, 16, 14, 0, 0, 0, time.UTC)

func pendingCase() domain.ReviewCase {
	return domain.ReviewCase{
		Request: domain.ReviewRequest{
			AcceptanceID: "acc-1", OfferID: "demo-review-1", CustomerID: "cust-1",
			Applicant: domain.Applicant{FullName: "Ana Pérez", DocumentID: "CC-1032456789", Email: "ana@example.com"},
			Premium:   89500, Currency: "COP", Reason: domain.ReviewReasonProvisionalOffer, ReceivedAt: received,
		},
		Status: domain.ReviewPending,
	}
}

func adjustedCase() domain.ReviewCase {
	c := pendingCase()
	p := 10.0
	c.Status = domain.ReviewDecided
	c.Decision = &domain.HumanDecision{
		ID: "dec-1", AcceptanceID: "acc-1", Type: domain.DecisionAdjust, Advisor: domain.Advisor{ID: "adv-1", Email: "adv@example.com"},
		DecidedAt: received.Add(time.Hour), Observations: "Ingreso no verificado", PremiumAdjustmentPct: &p,
		AdjustedOffer: &domain.AdjustedOffer{ID: "adj-acc-1", Premium: 98450, Currency: "COP", ExpiresAt: received.Add(25 * time.Hour)},
	}
	c.Notification = &domain.ReviewNotification{Channel: domain.ChannelEmail, Status: domain.NotificationSent, Attempts: 2,
		SentAt: received.Add(time.Hour + 3*time.Second)}
	return c
}

// CA-1: the inbox lists the routed request with every field.
func TestListReviewRequestsReturnsTheRequestFields(t *testing.T) {
	svc := &fakeReviews{cases: []domain.ReviewCase{pendingCase()}, counts: domain.ReviewCounts{Pending: 1, Decided: 4}}

	rec := callReviews(t, svc, http.MethodGet, "/v1/review-requests", "advisor:adv-1", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, domain.ReviewFilter{Status: domain.ReviewPending}, svc.gotFilter, "pending and the oldest first by default")
	assert.JSONEq(t, `{
		"items": [{
			"acceptance_id": "acc-1", "offer_id": "demo-review-1", "customer_id": "cust-1",
			"applicant": {"full_name": "Ana Pérez", "document_id": "CC-1032456789", "email": "ana@example.com"},
			"offer": {"premium": 89500, "currency": "COP"},
			"reason": "provisional_offer", "received_at": "2026-10-16T14:00:00Z", "status": "pending", "decision": null
		}],
		"counts": {"pending": 1, "decided": 4}
	}`, rec.Body.String())
}

// CA-4: the decided list exposes the whole decision record.
func TestListDecidedReviewRequestsShowsTheDecision(t *testing.T) {
	svc := &fakeReviews{cases: []domain.ReviewCase{adjustedCase()}}

	rec := callReviews(t, svc, http.MethodGet, "/v1/review-requests?status=decided&order=desc&reason=provisional_offer", "advisor:adv-1", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, domain.ReviewFilter{Status: domain.ReviewDecided, Reason: domain.ReviewReasonProvisionalOffer, Descending: true}, svc.gotFilter)
	items := decode(t, rec)["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, map[string]any{
		"decision_id": "dec-1", "type": "adjust", "advisor": map[string]any{"id": "adv-1", "email": "adv@example.com"},
		"decided_at": "2026-10-16T15:00:00Z", "observations": "Ingreso no verificado", "premium_adjustment_pct": 10.0,
		"adjusted_offer": map[string]any{"offer_id": "adj-acc-1", "premium": 98450.0, "currency": "COP", "expires_at": "2026-10-17T15:00:00Z"},
		"notification":   map[string]any{"channel": "email", "status": "sent", "attempts": 2.0, "sent_at": "2026-10-16T15:00:03Z"},
	}, items[0].(map[string]any)["decision"])
}

func TestListReviewRequestsReturnsAnEmptyList(t *testing.T) {
	rec := callReviews(t, &fakeReviews{}, http.MethodGet, "/v1/review-requests?order=asc", "advisor:adv-1", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"items": [], "counts": {"pending": 0, "decided": 0}}`, rec.Body.String())
}

// CA-5: invalid filters are rejected; the reasons are those of the domain.
func TestListReviewRequestsRejectsAnInvalidFilter(t *testing.T) {
	for _, q := range []string{"status=open", "status=PENDING", "order=newest", "reason=low_risk_score", "reason=x"} {
		svc := &fakeReviews{}
		rec := callReviews(t, svc, http.MethodGet, "/v1/review-requests?"+q, "advisor:adv-1", "")
		assert.Equal(t, http.StatusBadRequest, rec.Code, q)
		assert.Equal(t, "invalid_request", decode(t, rec)["error"], q)
		assert.Zero(t, svc.calls, q)
	}
	rec := callReviews(t, &fakeReviews{}, http.MethodGet, "/v1/review-requests?reason=high_risk_score", "advisor:adv-1", "")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestGetReviewRequestReturnsTheOfferDetail(t *testing.T) {
	expires := received.Add(23 * time.Hour)
	svc := &fakeReviews{detail: underwriting.ReviewDetail{Case: pendingCase(), OfferMode: domain.ModeProvisional, OfferExpiresAt: expires}}

	rec := callReviews(t, svc, http.MethodGet, "/v1/review-requests/acc-1", "advisor:adv-1", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "acc-1", svc.gotID)
	m := decode(t, rec)
	assert.Equal(t, map[string]any{"premium": 89500.0, "currency": "COP", "mode": "provisional", "expires_at": "2026-10-17T13:00:00Z"}, m["offer"])
	assert.Equal(t, "pending", m["status"])
	assert.Nil(t, m["decision"])
}

func TestGetReviewRequestWithAnUnreadableOffer(t *testing.T) {
	svc := &fakeReviews{detail: underwriting.ReviewDetail{Case: pendingCase()}}

	rec := callReviews(t, svc, http.MethodGet, "/v1/review-requests/acc-1", "advisor:adv-1", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, map[string]any{"premium": 89500.0, "currency": "COP", "mode": nil, "expires_at": nil}, decode(t, rec)["offer"])
}

// CA-2 and CA-3: the decision reaches the use case and the decided case returns.
func TestDecideReturns201WithTheDecidedCase(t *testing.T) {
	svc := &fakeReviews{cases: []domain.ReviewCase{adjustedCase()}}

	rec := callReviews(t, svc, http.MethodPost, "/v1/review-requests/acc-1/decision", "advisor:adv-1",
		`{"type":"adjust","observations":"Ingreso no verificado","premium_adjustment_pct":10}`)

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "acc-1", svc.gotID)
	assert.Equal(t, domain.DecisionAdjust, svc.gotInput.Type)
	assert.Equal(t, "Ingreso no verificado", svc.gotInput.Observations)
	require.NotNil(t, svc.gotInput.PremiumAdjustmentPct)
	assert.InDelta(t, 10.0, *svc.gotInput.PremiumAdjustmentPct, 1e-9)
	m := decode(t, rec)
	assert.Equal(t, "decided", m["status"])
	assert.Equal(t, "adjust", m["decision"].(map[string]any)["type"])
}

func TestDecideWithoutAdjustmentSendsNoPercentage(t *testing.T) {
	c := pendingCase()
	c.Status = domain.ReviewDecided
	c.Decision = &domain.HumanDecision{ID: "dec-1", Type: domain.DecisionAccept, DecidedAt: received}
	svc := &fakeReviews{cases: []domain.ReviewCase{c}}

	rec := callReviews(t, svc, http.MethodPost, "/v1/review-requests/acc-1/decision", "advisor:adv-1", `{"type":"accept"}`)

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Nil(t, svc.gotInput.PremiumAdjustmentPct)
	d := decode(t, rec)["decision"].(map[string]any)
	assert.Nil(t, d["premium_adjustment_pct"])
	assert.Nil(t, d["adjusted_offer"])
	assert.Nil(t, d["notification"])
}

func TestReviewErrors(t *testing.T) {
	cases := []struct {
		name, method, path, body string
		err                      error
		status                   int
		code                     string
	}{
		{"invalid json", http.MethodPost, "/v1/review-requests/acc-1/decision", `{`, nil, http.StatusBadRequest, "invalid_request"},
		{"long id", http.MethodPost, "/v1/review-requests/" + strings.Repeat("x", 129) + "/decision", `{}`, nil, http.StatusBadRequest, "invalid_request"},
		{"long id on get", http.MethodGet, "/v1/review-requests/" + strings.Repeat("x", 129), "", nil, http.StatusBadRequest, "invalid_request"},
		{"unknown type", http.MethodPost, "/v1/review-requests/acc-1/decision", `{"type":"x"}`,
			fmt.Errorf("%w: type", domain.ErrInvalidDecision), http.StatusBadRequest, "invalid_request"},
		{"observations required", http.MethodPost, "/v1/review-requests/acc-1/decision", `{"type":"reject"}`,
			domain.ErrObservationsRequired, http.StatusBadRequest, "observations_required"},
		{"bad adjustment", http.MethodPost, "/v1/review-requests/acc-1/decision", `{"type":"adjust","observations":"x","premium_adjustment_pct":0}`,
			fmt.Errorf("%w: range", domain.ErrInvalidPremiumAdjustment), http.StatusBadRequest, "premium_adjustment_invalid"},
		{"not found", http.MethodPost, "/v1/review-requests/acc-1/decision", `{"type":"accept"}`,
			ports.ErrReviewRequestNotFound, http.StatusNotFound, "review_request_not_found"},
		{"not found on get", http.MethodGet, "/v1/review-requests/acc-1", "", ports.ErrReviewRequestNotFound, http.StatusNotFound, "review_request_not_found"},
		{"already decided", http.MethodPost, "/v1/review-requests/acc-1/decision", `{"type":"accept"}`,
			ports.ErrReviewAlreadyDecided, http.StatusConflict, "review_request_already_decided"},
		{"continuation pending", http.MethodPost, "/v1/review-requests/acc-1/decision", `{"type":"accept"}`,
			fmt.Errorf("%w: down", underwriting.ErrContinuationPending), http.StatusServiceUnavailable, "decision_continuation_pending"},
		{"not registered", http.MethodPost, "/v1/review-requests/acc-1/decision", `{"type":"accept"}`,
			fmt.Errorf("%w: down", underwriting.ErrDecisionNotRegistered), http.StatusServiceUnavailable, "decision_not_registered"},
		{"store down on list", http.MethodGet, "/v1/review-requests", "", errors.New("down"), http.StatusServiceUnavailable, "review_unavailable"},
		{"store down on get", http.MethodGet, "/v1/review-requests/acc-1", "", errors.New("down"), http.StatusServiceUnavailable, "review_unavailable"},
		{"forbidden by the use case", http.MethodGet, "/v1/review-requests", "", security.ErrForbidden, http.StatusForbidden, "forbidden"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := callReviews(t, &fakeReviews{err: tc.err}, tc.method, tc.path, "advisor:adv-1", tc.body)
			assert.Equal(t, tc.status, rec.Code)
			assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			m := decode(t, rec)
			assert.Equal(t, tc.code, m["error"])
			assert.NotEmpty(t, m["message"])
		})
	}
}

// The 401/403/200 matrix of the inbox routes (template: pkg/security/catalog_test.go):
// only an advisor gets in, and the use case never runs for anybody else.
func TestReviewRoutesAuthMatrix(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/review-requests", ""},
		{http.MethodGet, "/v1/review-requests/acc-1", ""},
		{http.MethodPost, "/v1/review-requests/acc-1/decision", `{"type":"accept"}`},
	}
	for _, rt := range routes {
		for _, token := range []string{"", "garbage"} {
			svc := &fakeReviews{cases: []domain.ReviewCase{pendingCase()}}
			rec := callReviews(t, svc, rt.method, rt.path, token, rt.body)
			assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s token=%q", rt.method, rt.path, token)
			assert.Equal(t, "unauthorized", decode(t, rec)["error"])
			assert.Zero(t, svc.calls, "the use case does not run")
		}
		for _, role := range []string{"insured", "operations", "partner"} {
			svc := &fakeReviews{cases: []domain.ReviewCase{pendingCase()}}
			rec := callReviews(t, svc, rt.method, rt.path, role+":user-1", rt.body)
			assert.Equal(t, http.StatusForbidden, rec.Code, "%s %s as %s", rt.method, rt.path, role)
			assert.Zero(t, svc.calls, "the use case does not run")
		}
		svc := &fakeReviews{cases: []domain.ReviewCase{pendingCase()}, detail: underwriting.ReviewDetail{Case: pendingCase()}}
		rec := callReviews(t, svc, rt.method, rt.path, "advisor:adv-1", rt.body)
		assert.Contains(t, []int{http.StatusOK, http.StatusCreated}, rec.Code, "%s %s as advisor", rt.method, rt.path)
		assert.Equal(t, 1, svc.calls)
	}
}

func TestReviewRoutesAreNotMountedWithoutTheOption(t *testing.T) {
	rec := httptest.NewRecorder()
	httpadapter.NewRouter(&fakeResolver{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/review-requests", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}
