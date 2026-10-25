package httpadapter_test

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

type fakeRenewals struct {
	views      []renewal.PolicyView
	conditions renewal.Conditions
	accepted   renewal.Accepted
	renewal    domain.Renewal
	charge     domain.Charge
	err        error
	gotID      string
}

func (f *fakeRenewals) ListPolicies(context.Context) ([]renewal.PolicyView, error) {
	return f.views, f.err
}

func (f *fakeRenewals) Conditions(_ context.Context, id string) (renewal.Conditions, error) {
	f.gotID = id
	return f.conditions, f.err
}

func (f *fakeRenewals) Accept(_ context.Context, id string) (renewal.Accepted, error) {
	f.gotID = id
	return f.accepted, f.err
}

func (f *fakeRenewals) Status(_ context.Context, id string) (domain.Renewal, domain.Charge, error) {
	f.gotID = id
	return f.renewal, f.charge, f.err
}

var policyEndsAt = time.Date(2026, 10, 2, 23, 59, 0, 0, time.UTC)

func renewalPolicy() domain.Policy {
	return domain.Policy{ID: "pol-1", AcceptanceID: "acc-1", CustomerID: "sub-1",
		Holder:  domain.Applicant{FullName: "Carolina Pérez", Email: "carolina@example.com"},
		Premium: 142_300, Currency: "COP", InsuredAmount: 68_000_000,
		Coverages:     []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 68_000_000}},
		CoverageStart: policyEndsAt.AddDate(-1, 0, 0), CoverageEnd: policyEndsAt}
}

func callPolicies(t *testing.T, svc *fakeRenewals, method, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithObservability("origination"),
		httpadapter.WithPolicies(svc, tokenVerifier{})).ServeHTTP(rec, req)
	return rec
}

func TestListPoliciesShowsTheWindowAndTheRenewalOfEachOne(t *testing.T) {
	p := renewalPolicy()
	svc := &fakeRenewals{views: []renewal.PolicyView{{
		Policy: p, CanRenew: true, WindowOpensAt: policyEndsAt.AddDate(0, 0, -30), WindowClosesAt: policyEndsAt,
	}}}

	rec := callPolicies(t, svc, http.MethodGet, "/v1/policies", "insured:sub-1")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var body struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	require.Len(t, body.Items, 1)
	assert.Equal(t, "pol-1", body.Items[0]["policy_id"])
	assert.Equal(t, true, body.Items[0]["can_renew"])
	assert.Nil(t, body.Items[0]["renewal"])
	assert.NotContains(t, rec.Body.String(), "email", "the list carries no contact data")
}

func TestRenewalConditionsShowBothSidesAndNeverTheToken(t *testing.T) {
	p := renewalPolicy()
	token := domain.PaymentMethod{Kind: domain.PaymentCard, Token: "tok_secret", Brand: "visa", Last4: "1111"}
	svc := &fakeRenewals{conditions: renewal.Conditions{
		RenewalConditions: domain.RenewalConditions{
			Policy: p, Requoted: true, DiscountRate: 0.08,
			Current: domain.RenewalTerms{Premium: 142_300, Currency: "COP", CoverageEnd: policyEndsAt},
			Renewal: domain.RenewalTerms{Premium: 149_800, Currency: "COP", CoverageStart: policyEndsAt, CoverageEnd: policyEndsAt.AddDate(1, 0, 0)},
		},
		PaymentMethod: &token,
	}}

	rec := callPolicies(t, svc, http.MethodGet, "/v1/policies/pol-1/renewal", "insured:sub-1")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "pol-1", svc.gotID)
	assert.Contains(t, rec.Body.String(), `"discount_rate":0.08`)
	assert.Contains(t, rec.Body.String(), `"last4":"1111"`)
	assert.NotContains(t, rec.Body.String(), "tok_secret")
}

func TestAcceptRenewalAnswers201ThenTheSameRenewalWith200(t *testing.T) {
	r := domain.Renewal{ID: "ren-pol-1-20261002", PolicyID: "pol-1", PeriodEnd: policyEndsAt, Status: domain.RenewalAccepted,
		ChargeID: "ren-pol-1-20261002", Premium: 149_800, Currency: "COP", Terms: domain.CoverageTerms{TermMonths: 12}}
	c := domain.Charge{AcceptanceID: r.ChargeID, Status: domain.ChargePending, AmountMinor: 14_980_000, Currency: "COP"}
	svc := &fakeRenewals{accepted: renewal.Accepted{Renewal: r, Charge: c, Created: true}}

	rec := callPolicies(t, svc, http.MethodPost, "/v1/policies/pol-1/renewal", "insured:sub-1")
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Contains(t, rec.Body.String(), `"charge_id":"ren-pol-1-20261002"`)

	svc.accepted.Created = false
	rec = callPolicies(t, svc, http.MethodPost, "/v1/policies/pol-1/renewal", "insured:sub-1")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestRenewalErrorsHaveStableCodes(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"outside the window": {domain.ErrRenewalNotAvailable, http.StatusConflict, "renewal_not_available"},
		"closed period":      {domain.ErrRenewalClosed, http.StatusConflict, "renewal_closed"},
		"unknown policy":     {ports.ErrPolicyNotFound, http.StatusNotFound, "policy_not_found"},
		"no renewal":         {ports.ErrRenewalNotFound, http.StatusNotFound, "renewal_not_found"},
		"not the owner":      {security.ErrForbidden, http.StatusForbidden, ""},
		"storage down":       {errors.New("boom"), http.StatusServiceUnavailable, "renewal_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			svc := &fakeRenewals{err: tc.err}
			for _, route := range [][2]string{
				{http.MethodGet, "/v1/policies/pol-1/renewal"}, {http.MethodPost, "/v1/policies/pol-1/renewal"},
				{http.MethodGet, "/v1/policies/pol-1/renewal/status"},
			} {
				rec := callPolicies(t, svc, route[0], route[1], "insured:sub-1")
				assert.Equal(t, tc.status, rec.Code, route[1])
				if tc.code != "" {
					assert.Contains(t, rec.Body.String(), tc.code)
				}
			}
		})
	}
}

func TestPolicyRoutesAreOnlyForTheInsured(t *testing.T) {
	svc := &fakeRenewals{}
	for _, route := range [][2]string{
		{http.MethodGet, "/v1/policies"}, {http.MethodGet, "/v1/policies/pol-1/renewal"},
		{http.MethodPost, "/v1/policies/pol-1/renewal"}, {http.MethodGet, "/v1/policies/pol-1/renewal/status"},
	} {
		assert.Equal(t, http.StatusUnauthorized, callPolicies(t, svc, route[0], route[1], "").Code, "no token: "+route[1])
		assert.Equal(t, http.StatusForbidden, callPolicies(t, svc, route[0], route[1], "advisor:ana").Code, "advisor: "+route[1])
		assert.Equal(t, http.StatusForbidden, callPolicies(t, svc, route[0], route[1], "operations:ops").Code, "operations: "+route[1])
	}
}
