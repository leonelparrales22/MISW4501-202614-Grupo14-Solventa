package httpadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/wallet"
)

type fakeExpiringPolicies struct {
	page  domain.ExpiringPage
	err   error
	got   domain.ExpiringQuery
	calls int
}

func (f *fakeExpiringPolicies) List(_ context.Context, q domain.ExpiringQuery) (domain.ExpiringPage, error) {
	f.calls++
	f.got = q
	return f.page, f.err
}

func callExpiring(t *testing.T, svc *fakeExpiringPolicies, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithObservability("origination"),
		httpadapter.WithExpiringPolicies(svc, tokenVerifier{})).ServeHTTP(rec, req)
	return rec
}

func mustCivilDay(t *testing.T, raw string) time.Time {
	t.Helper()
	d, err := domain.ParseCivilDay(raw)
	require.NoError(t, err)
	return d
}

func expiringPage(t *testing.T) domain.ExpiringPage {
	t.Helper()
	end := time.Date(2026, 10, 2, 23, 59, 0, 0, time.UTC)
	open := domain.ExpiringPolicy{
		Policy: domain.Policy{ID: "pol-acc-1", CustomerID: "sub-1", Premium: 142_300, Currency: "COP", CoverageEnd: end,
			Holder: domain.Applicant{FullName: "Carolina Pérez Ruiz", DocumentID: "1020345678", Email: "carolina@example.com"}},
		Status: domain.ExpiringStatusExpiring, CanRenew: true,
		WindowOpensAt: end.AddDate(0, 0, -30), WindowClosesAt: end,
	}
	accepted := open
	accepted.Policy.ID, accepted.Status, accepted.CanRenew = "pol-acc-2", domain.ExpiringStatusRenewalInProgress, false
	accepted.Renewal = &domain.Renewal{ID: "ren-pol-acc-2-20261002", Status: domain.RenewalAccepted, ChargeID: "ren-pol-acc-2-20261002",
		AcceptedAt: end.AddDate(0, 0, -3), RetryUntil: end}
	return domain.ExpiringPage{Items: []domain.ExpiringPolicy{open, accepted}, Total: 2, DueWithinWindow: 12,
		From: mustCivilDay(t, "2026-09-16"), To: mustCivilDay(t, "2026-10-16"), WindowDays: 30, Page: 1, PageSize: 20}
}

func TestExpiringPoliciesAreOnlyForTheAdvisor(t *testing.T) {
	svc := &fakeExpiringPolicies{page: expiringPage(t)}

	assert.Equal(t, http.StatusUnauthorized, callExpiring(t, svc, "/v1/policies/expiring", "").Code)
	assert.Equal(t, http.StatusForbidden, callExpiring(t, svc, "/v1/policies/expiring", "insured:sub-1").Code)
	assert.Equal(t, http.StatusForbidden, callExpiring(t, svc, "/v1/policies/expiring", "operations:ops").Code)
	assert.Zero(t, svc.calls, "nobody but an advisor reaches the use case")

	rec := callExpiring(t, svc, "/v1/policies/expiring", "advisor:laura")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}

func TestExpiringPoliciesAnswerTheRowsTheRangeAndTheIndicator(t *testing.T) {
	svc := &fakeExpiringPolicies{page: expiringPage(t)}

	rec := callExpiring(t, svc, "/v1/policies/expiring", "advisor:laura")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "carolina@example.com", "the holder never carries the e-mail")
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "2026-09-16", body["expires_from"])
	assert.Equal(t, "2026-10-16", body["expires_to"])
	assert.EqualValues(t, 30, body["window_days"])
	assert.EqualValues(t, 12, body["due_within_window"])
	assert.EqualValues(t, 2, body["total"])
	assert.EqualValues(t, 1, body["page"])
	assert.EqualValues(t, 20, body["page_size"])

	items := body["items"].([]any)
	require.Len(t, items, 2)
	open := items[0].(map[string]any)
	assert.Equal(t, "pol-acc-1", open["policy_id"])
	assert.Equal(t, "life", open["line"])
	assert.Nil(t, open["product"])
	assert.Equal(t, map[string]any{"full_name": "Carolina Pérez Ruiz", "document_type": nil, "document_id": "1020345678"}, open["holder"])
	assert.InDelta(t, 142_300, open["premium"], 0.001)
	assert.Equal(t, "COP", open["currency"])
	assert.Equal(t, "2026-10-02T23:59:00Z", open["coverage_end"])
	assert.Equal(t, "expiring", open["status"])
	assert.Equal(t, true, open["can_renew"])
	assert.Equal(t, map[string]any{"opens_at": "2026-09-02T23:59:00Z", "closes_at": "2026-10-02T23:59:00Z"}, open["renewal_window"])
	assert.Nil(t, open["renewal"])

	inProgress := items[1].(map[string]any)
	assert.Equal(t, "renewal_in_progress", inProgress["status"])
	assert.Equal(t, false, inProgress["can_renew"])
	summary := inProgress["renewal"].(map[string]any)
	assert.Equal(t, "ren-pol-acc-2-20261002", summary["renewal_id"])
	assert.Equal(t, "accepted", summary["status"])
	assert.Equal(t, "ren-pol-acc-2-20261002", summary["charge_id"])
	assert.Nil(t, summary["closed_at"])
}

func TestExpiringAnEmptyRangeAnswersAnEmptyListNotAnError(t *testing.T) {
	page := expiringPage(t)
	page.Items, page.Total = nil, 0
	svc := &fakeExpiringPolicies{page: page}

	rec := callExpiring(t, svc, "/v1/policies/expiring?expires_from=2026-12-01&expires_to=2026-12-07", "advisor:laura")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"items":[]`)
	assert.Contains(t, rec.Body.String(), `"total":0`)
}

func TestExpiringTheQueryOfTheListReachesTheUseCase(t *testing.T) {
	svc := &fakeExpiringPolicies{page: expiringPage(t)}

	rec := callExpiring(t, svc,
		"/v1/policies/expiring?expires_from=2026-10-01&expires_to=2026-12-31&status=renewal_in_progress&page=3&page_size=10",
		"advisor:laura")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, domain.ExpiringQuery{From: mustCivilDay(t, "2026-10-01"), To: mustCivilDay(t, "2026-12-31"),
		Status: domain.ExpiringStatusRenewalInProgress, Page: 3, PageSize: 10}, svc.got)

	rec = callExpiring(t, svc, "/v1/policies/expiring", "advisor:laura")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, domain.ExpiringQuery{}, svc.got, "without parameters the use case applies the defaults")
}

func TestExpiringABadParameterIsAnInvalidRequest(t *testing.T) {
	for _, query := range []string{
		"expires_from=16/09/2026", "expires_to=2026-13-01", "status=renewed", "status=cancelled_non_payment",
		"page=0", "page=two", "page_size=-1",
	} {
		svc := &fakeExpiringPolicies{page: expiringPage(t)}
		rec := callExpiring(t, svc, "/v1/policies/expiring?"+query, "advisor:laura")
		assert.Equal(t, http.StatusBadRequest, rec.Code, query)
		assert.Contains(t, rec.Body.String(), `"error":"invalid_request"`, query)
		assert.Zero(t, svc.calls, query)
	}
}

func TestExpiringErrorsOfTheUseCaseHaveStableCodes(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"to before from":  {domain.ErrExpiringRangeInverted, http.StatusBadRequest, "out_of_range"},
		"page too large":  {errors.Join(domain.ErrInvalidExpiringQuery, errors.New("page_size")), http.StatusBadRequest, "invalid_request"},
		"not an advisor":  {security.ErrForbidden, http.StatusForbidden, ""},
		"no session":      {security.ErrUnauthenticated, http.StatusUnauthorized, ""},
		"database down":   {errors.New("connection refused"), http.StatusServiceUnavailable, "renewal_unavailable"},
		"context timeout": {context.DeadlineExceeded, http.StatusServiceUnavailable, "renewal_unavailable"},
	} {
		svc := &fakeExpiringPolicies{err: tc.err}
		rec := callExpiring(t, svc, "/v1/policies/expiring?expires_from=2026-10-10&expires_to=2026-10-01", "advisor:laura")
		assert.Equal(t, tc.status, rec.Code, name)
		if tc.code != "" {
			assert.Contains(t, rec.Body.String(), `"error":"`+tc.code+`"`, name)
		}
	}
}

// The fixed segment of the list never covers the routes of one policy nor the
// wallet of the insured, whatever the order they are mounted in.
func TestExpiringListDoesNotCoverTheOtherPolicyRoutes(t *testing.T) {
	expiring := &fakeExpiringPolicies{page: expiringPage(t)}
	renewals := &fakeRenewals{}
	walletSvc := &fakeWallet{wallet: wallet.Wallet{}}
	router := httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithObservability("origination"),
		httpadapter.WithPolicies(renewals, tokenVerifier{}),
		httpadapter.WithExpiringPolicies(expiring, tokenVerifier{}),
		httpadapter.WithPolicyWallet(walletSvc, tokenVerifier{}))
	call := func(method, path, token string) int {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec.Code
	}

	assert.Equal(t, http.StatusOK, call(http.MethodGet, "/v1/policies/expiring", "advisor:laura"))
	assert.Equal(t, 1, expiring.calls)

	assert.Equal(t, http.StatusOK, call(http.MethodGet, "/v1/policies/pol-1/renewal", "insured:sub-1"))
	assert.Equal(t, "pol-1", renewals.gotID)
	assert.Equal(t, http.StatusOK, call(http.MethodGet, "/v1/policies/expiring/renewal", "insured:sub-1"),
		"a policy whose id were expiring would still reach its renewal")
	assert.Equal(t, "expiring", renewals.gotID)
	assert.Equal(t, http.StatusOK, call(http.MethodGet, "/v1/policies", "insured:sub-1"), "the wallet of the insured")
	assert.Equal(t, 1, expiring.calls, "neither reached the list")

	assert.Equal(t, http.StatusMethodNotAllowed, call(http.MethodPost, "/v1/policies/expiring", "advisor:laura"),
		"the list is read only")
	assert.Equal(t, 1, expiring.calls)
}
