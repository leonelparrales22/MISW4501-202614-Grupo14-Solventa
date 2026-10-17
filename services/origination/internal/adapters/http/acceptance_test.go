package httpadapter_test

import (
	"context"
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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/issuance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// tokenVerifier accepts "Bearer <role>:<subject>".
type tokenVerifier struct{}

func (tokenVerifier) Verify(_ context.Context, raw string) (security.Claims, error) {
	role, subject, ok := strings.Cut(raw, ":")
	if !ok {
		return security.Claims{}, errors.New("bad token")
	}
	return security.Claims{Subject: subject, Roles: []security.Role{security.Role(role)}}, nil
}

type fakeAcceptances struct {
	acceptance domain.Acceptance
	created    bool
	err        error
	gotOffer   string
	gotID      string
	gotChannel domain.Channel
	calls      int
}

func (f *fakeAcceptances) Accept(_ context.Context, offerID string, channel domain.Channel) (domain.Acceptance, bool, error) {
	f.calls++
	f.gotOffer, f.gotChannel = offerID, channel
	return f.acceptance, f.created, f.err
}

func (f *fakeAcceptances) Get(_ context.Context, id string) (domain.Acceptance, error) {
	f.calls++
	f.gotID = id
	return f.acceptance, f.err
}

func (f *fakeAcceptances) GetByOffer(_ context.Context, offerID string) (domain.Acceptance, error) {
	f.calls++
	f.gotOffer = offerID
	return f.acceptance, f.err
}

func call(t *testing.T, svc *fakeAcceptances, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithAcceptances(svc, tokenVerifier{})).ServeHTTP(rec, req)
	return rec
}

var accepted = time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC)

func registered() domain.Acceptance {
	return domain.Acceptance{
		ID: "acc-1", OfferID: "demo-active-1", CustomerID: "cust-1", Channel: domain.ChannelWeb,
		AcceptedAt: accepted, Status: domain.AcceptanceRegistered, NextAttemptAt: accepted, UpdatedAt: accepted,
	}
}

func TestAcceptReturns202WithTheRegisteredAcceptance(t *testing.T) {
	svc := &fakeAcceptances{acceptance: registered(), created: true}

	rec := call(t, svc, http.MethodPost, "/v1/offers/demo-active-1/acceptance", "insured:cust-1", `{"channel":"web"}`)

	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, "/v1/acceptances/acc-1", rec.Header().Get("Location"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "demo-active-1", svc.gotOffer)
	assert.Equal(t, domain.ChannelWeb, svc.gotChannel)
	assert.JSONEq(t, `{
		"acceptance_id": "acc-1", "offer_id": "demo-active-1", "customer_id": "cust-1", "channel": "web",
		"accepted_at": "2026-10-11T15:00:00Z", "status": "registered", "underwriting": null,
		"processing": {"attempts": 0, "last_failure_at": null, "next_attempt_at": "2026-10-11T15:00:00Z"},
		"updated_at": "2026-10-11T15:00:00Z"
	}`, rec.Body.String())
}

func TestAcceptingAgainReturns200(t *testing.T) {
	a := registered()
	a.Status = domain.AcceptanceInReview
	a.NextAttemptAt = time.Time{}
	a.Attempts = 1
	a.LastFailureAt = accepted.Add(time.Second)
	a.Decision = &domain.UnderwritingDecision{Outcome: domain.UnderwritingHumanReview, Reason: domain.ReviewReasonHighRiskScore, DecidedAt: accepted}
	svc := &fakeAcceptances{acceptance: a}

	rec := call(t, svc, http.MethodPost, "/v1/offers/demo-active-1/acceptance", "insured:cust-1", `{"channel":"mobile"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	m := decode(t, rec)
	assert.Equal(t, "in_review", m["status"])
	assert.Equal(t, map[string]any{"outcome": "human_review", "reason": "high_risk_score", "decided_at": "2026-10-11T15:00:00Z"}, m["underwriting"])
	assert.Equal(t, map[string]any{"attempts": float64(1), "last_failure_at": "2026-10-11T15:00:01Z", "next_attempt_at": nil}, m["processing"])
}

func TestApprovedDecisionHasNoReason(t *testing.T) {
	a := registered()
	a.Status = domain.AcceptanceIssuancePending
	a.Decision = &domain.UnderwritingDecision{Outcome: domain.UnderwritingApproved, DecidedAt: accepted}
	rec := call(t, &fakeAcceptances{acceptance: a}, http.MethodGet, "/v1/acceptances/acc-1", "insured:cust-1", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, map[string]any{"outcome": "approved", "reason": nil, "decided_at": "2026-10-11T15:00:00Z"}, decode(t, rec)["underwriting"])
}

func TestAcceptErrors(t *testing.T) {
	expiresAt := time.Date(2026, 10, 10, 9, 30, 0, 0, time.UTC)
	cases := []struct {
		name, path, body string
		err              error
		status           int
		code             string
	}{
		{"invalid json", "/v1/offers/o-1/acceptance", `{`, nil, http.StatusBadRequest, "invalid_request"},
		{"missing channel", "/v1/offers/o-1/acceptance", `{}`, nil, http.StatusBadRequest, "invalid_request"},
		{"unknown channel", "/v1/offers/o-1/acceptance", `{"channel":"fax"}`, nil, http.StatusBadRequest, "invalid_request"},
		{"long offer id", "/v1/offers/" + strings.Repeat("x", 129) + "/acceptance", `{"channel":"web"}`, nil, http.StatusBadRequest, "invalid_request"},
		{"offer not found", "/v1/offers/o-1/acceptance", `{"channel":"web"}`, ports.ErrOfferNotFound, http.StatusNotFound, "offer_not_found"},
		{"not the owner", "/v1/offers/o-1/acceptance", `{"channel":"web"}`, security.ErrForbidden, http.StatusForbidden, "forbidden"},
		{"no caller", "/v1/offers/o-1/acceptance", `{"channel":"web"}`, security.ErrUnauthenticated, http.StatusUnauthorized, "unauthorized"},
		{"repository down", "/v1/offers/o-1/acceptance", `{"channel":"web"}`, errors.New("down"), http.StatusServiceUnavailable, "acceptance_not_registered"},
		{"expired", "/v1/offers/o-1/acceptance", `{"channel":"web"}`, &issuance.OfferExpiredError{OfferID: "o-1", ExpiresAt: expiresAt}, http.StatusUnprocessableEntity, "offer_expired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeAcceptances{err: tc.err}
			rec := call(t, svc, http.MethodPost, tc.path, "insured:cust-1", tc.body)
			assert.Equal(t, tc.status, rec.Code)
			assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			m := decode(t, rec)
			assert.Equal(t, tc.code, m["error"])
			assert.NotEmpty(t, m["message"])
			if tc.code == "offer_expired" {
				assert.Equal(t, "o-1", m["offer_id"])
				assert.Equal(t, "2026-10-10T09:30:00Z", m["expires_at"])
			}
		})
	}
}

func TestReadAcceptanceRoutes(t *testing.T) {
	svc := &fakeAcceptances{acceptance: registered()}

	rec := call(t, svc, http.MethodGet, "/v1/acceptances/acc-1", "advisor:adv-1", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "acc-1", svc.gotID)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	rec = call(t, svc, http.MethodGet, "/v1/offers/demo-active-1/acceptance", "operations:ops-1", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "demo-active-1", svc.gotOffer)
	assert.Equal(t, "acc-1", decode(t, rec)["acceptance_id"])
}

func TestReadAcceptanceErrors(t *testing.T) {
	cases := []struct {
		name, path string
		err        error
		status     int
		code       string
	}{
		{"not found", "/v1/acceptances/acc-1", ports.ErrAcceptanceNotFound, http.StatusNotFound, "acceptance_not_found"},
		{"offer without acceptance", "/v1/offers/o-1/acceptance", ports.ErrAcceptanceNotFound, http.StatusNotFound, "acceptance_not_found"},
		{"not the owner", "/v1/acceptances/acc-1", security.ErrForbidden, http.StatusForbidden, "forbidden"},
		{"repository down", "/v1/acceptances/acc-1", errors.New("down"), http.StatusServiceUnavailable, "acceptance_unavailable"},
		{"long acceptance id", "/v1/acceptances/" + strings.Repeat("x", 129), nil, http.StatusBadRequest, "invalid_request"},
		{"long offer id", "/v1/offers/" + strings.Repeat("x", 129) + "/acceptance", nil, http.StatusBadRequest, "invalid_request"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := call(t, &fakeAcceptances{err: tc.err}, http.MethodGet, tc.path, "insured:cust-1", "")
			assert.Equal(t, tc.status, rec.Code)
			assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			assert.Equal(t, tc.code, decode(t, rec)["error"])
		})
	}
}

// The 401/403 matrix of the acceptance routes (template: pkg/security/catalog_test.go).
func TestAcceptanceRoutesAuthMatrix(t *testing.T) {
	routes := []struct {
		method, path, body string
		allowed            []string
	}{
		{http.MethodPost, "/v1/offers/o-1/acceptance", `{"channel":"web"}`, []string{"insured"}},
		{http.MethodGet, "/v1/offers/o-1/acceptance", "", []string{"insured", "advisor", "operations"}},
		{http.MethodGet, "/v1/acceptances/acc-1", "", []string{"insured", "advisor", "operations"}},
	}
	for _, rt := range routes {
		for _, token := range []string{"", "garbage"} {
			svc := &fakeAcceptances{acceptance: registered(), created: true}
			rec := call(t, svc, rt.method, rt.path, token, rt.body)
			assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s token=%q", rt.method, rt.path, token)
			assert.Equal(t, "unauthorized", decode(t, rec)["error"])
			assert.Zero(t, svc.calls, "the business operation does not run")
		}
		for _, role := range []string{"insured", "advisor", "operations", "partner"} {
			svc := &fakeAcceptances{acceptance: registered(), created: true}
			rec := call(t, svc, rt.method, rt.path, role+":user-1", rt.body)
			if contains(rt.allowed, role) {
				assert.Less(t, rec.Code, 300, "%s %s as %s", rt.method, rt.path, role)
				assert.Equal(t, 1, svc.calls)
				continue
			}
			assert.Equal(t, http.StatusForbidden, rec.Code, "%s %s as %s", rt.method, rt.path, role)
			assert.Zero(t, svc.calls, "the business operation does not run")
		}
	}
}

func TestAcceptanceRoutesAreNotMountedWithoutTheOption(t *testing.T) {
	rec := httptest.NewRecorder()
	httpadapter.NewRouter(&fakeResolver{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/acceptances/acc-1", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
