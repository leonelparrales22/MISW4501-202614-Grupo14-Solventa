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

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/billing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

type fakeCharges struct {
	charge     domain.Charge
	exceptions []domain.Charge
	err        error
	gotID      string
	gotNote    string
	gotLimit   int
	gotMethod  domain.PaymentMethod
	calls      int
}

func (f *fakeCharges) Get(_ context.Context, id string) (domain.Charge, error) {
	f.calls++
	f.gotID = id
	return f.charge, f.err
}

func (f *fakeCharges) ListExceptions(_ context.Context, limit int) ([]domain.Charge, error) {
	f.calls++
	f.gotLimit = limit
	return f.exceptions, f.err
}

func (f *fakeCharges) Resume(_ context.Context, id, note string) (domain.Charge, error) {
	f.calls++
	f.gotID, f.gotNote = id, note
	return f.charge, f.err
}

func (f *fakeCharges) AttachPaymentMethod(_ context.Context, id string,
	m domain.PaymentMethod) (domain.Charge, error) {
	f.calls++
	f.gotID, f.gotMethod = id, m
	return f.charge, f.err
}

var chargeAt = time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)

func pendingCharge() domain.Charge {
	c := domain.NewCharge(
		domain.Acceptance{ID: "acc-1", Channel: domain.ChannelWeb, AcceptedAt: chargeAt.Add(-time.Minute)},
		domain.Offer{ID: "demo-active-1", CustomerID: "cust-1", Premium: 89_500, Currency: "COP"},
		domain.CoverageTerms{TermMonths: 120}, chargeAt, chargeAt.Add(24*time.Hour))
	c.Attempts, c.LastAttemptAt, c.LastFailureAt = 2, chargeAt.Add(2*time.Second), chargeAt.Add(2*time.Second)
	c.NextAttemptAt = chargeAt.Add(3 * time.Second)
	return c
}

func callCharges(t *testing.T, svc *fakeCharges, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set(observability.CorrelationHeader, "corr-ha-d02")
	rec := httptest.NewRecorder()
	httpadapter.NewRouter(&fakeResolver{},
		httpadapter.WithObservability("origination"),
		httpadapter.WithCharges(svc, tokenVerifier{})).ServeHTTP(rec, req)
	return rec
}

func TestGetChargeShowsAPendingChargeWithoutPolicy(t *testing.T) {
	svc := &fakeCharges{charge: pendingCharge()}

	rec := callCharges(t, svc, http.MethodGet, "/v1/acceptances/acc-1/charge", "insured:cust-1", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "corr-ha-d02", rec.Header().Get(observability.CorrelationHeader), "registered after WithObservability")
	assert.Equal(t, "acc-1", svc.gotID)
	body := decode(t, rec)
	assert.Equal(t, "pending", body["status"])
	assert.Equal(t, "cobro-acc-1", body["idempotency_key"])
	assert.EqualValues(t, 89_500, body["amount"])
	assert.Equal(t, "COP", body["currency"])
	assert.EqualValues(t, 2, body["attempts"])
	assert.Equal(t, "2026-10-16T15:00:03Z", body["next_attempt_at"])
	assert.Equal(t, map[string]any{"started_at": "2026-10-16T15:00:00Z", "ends_at": "2026-10-17T15:00:00Z"}, body["retry_window"],
		"a 24-hour window")
	for _, k := range []string{"policy_id", "coverage_activated_at", "activation_published_at", "confirmed_at", "failure_reason"} {
		v, present := body[k]
		assert.True(t, present, k)
		assert.Nil(t, v, k)
	}
}

func TestGetChargeShowsThePolicyOnceConfirmed(t *testing.T) {
	c := pendingCharge().Confirmed(chargeAt.Add(5*time.Second), "ch-1")
	c.ActivationPublishedAt = chargeAt.Add(6 * time.Second)

	rec := callCharges(t, &fakeCharges{charge: c}, http.MethodGet, "/v1/acceptances/acc-1/charge", "advisor:adv-1", "")

	require.Equal(t, http.StatusOK, rec.Code)
	body := decode(t, rec)
	assert.Equal(t, "confirmed", body["status"])
	assert.Equal(t, "pol-acc-1", body["policy_id"])
	assert.Equal(t, "2026-10-16T15:00:05Z", body["coverage_activated_at"])
	assert.Equal(t, "2026-10-16T15:00:05Z", body["confirmed_at"])
	assert.Equal(t, "2026-10-16T15:00:06Z", body["activation_published_at"])
	assert.Nil(t, body["next_attempt_at"], "finished")
}

func TestGetChargeErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"not found":   {ports.ErrChargeNotFound, http.StatusNotFound, "charge_not_found"},
		"not owner":   {security.ErrForbidden, http.StatusForbidden, "forbidden"},
		"unavailable": {errors.New("database down"), http.StatusServiceUnavailable, "charge_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := callCharges(t, &fakeCharges{err: tc.err}, http.MethodGet, "/v1/acceptances/acc-1/charge", "insured:cust-2", "")
			assert.Equal(t, tc.status, rec.Code)
			assert.Equal(t, tc.code, decode(t, rec)["error"])
		})
	}

	rec := callCharges(t, &fakeCharges{}, http.MethodGet, "/v1/acceptances/acc-1/charge", "", "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	rec = callCharges(t, &fakeCharges{}, http.MethodGet, "/v1/acceptances/acc-1/charge", "partner:p-1", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	rec = callCharges(t, &fakeCharges{}, http.MethodGet, "/v1/acceptances/"+strings.Repeat("a", 129)+"/charge", "insured:c", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestIssuanceExceptionsAreForOperations(t *testing.T) {
	failed := pendingCharge()
	failed.Status, failed.FailureReason, failed.NextAttemptAt = domain.ChargeFailed, domain.ChargeRetryWindowExpired, time.Time{}
	failed.LastFailureAt, failed.ResumeCount = chargeAt.Add(24*time.Hour), 1
	failed.LastResumedAt, failed.LastResumedBy = chargeAt.Add(time.Hour), "ops-1"
	svc := &fakeCharges{exceptions: []domain.Charge{failed}}

	rec := callCharges(t, svc, http.MethodGet, "/v1/issuance-exceptions?limit=10", "operations:ops-1", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, 10, svc.gotLimit)
	items := decode(t, rec)["items"].([]any)
	require.Len(t, items, 1)
	assert.Equal(t, map[string]any{
		"acceptance_id": "acc-1", "offer_id": "demo-active-1", "customer_id": "cust-1", "step": "premium_charge",
		"reason": "retry_window_expired", "attempts": float64(2), "idempotency_key": "cobro-acc-1", "amount": float64(89_500),
		"currency": "COP", "accepted_at": "2026-10-16T14:59:00Z", "failed_at": "2026-10-17T15:00:00Z",
		"resume_count": float64(1), "last_resumed_at": "2026-10-16T16:00:00Z", "last_resumed_by": "ops-1",
	}, items[0])

	rec = callCharges(t, &fakeCharges{}, http.MethodGet, "/v1/issuance-exceptions", "operations:ops-1", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, map[string]any{"items": []any{}}, decode(t, rec), "an empty list, not null")

	for _, token := range []string{"insured:cust-1", "advisor:adv-1"} {
		svc := &fakeCharges{}
		rec := callCharges(t, svc, http.MethodGet, "/v1/issuance-exceptions", token, "")
		assert.Equal(t, http.StatusForbidden, rec.Code, token)
		assert.Zero(t, svc.calls)
	}
}

func TestIssuanceExceptionsErrors(t *testing.T) {
	for _, limit := range []string{"0", "201", "x"} {
		rec := callCharges(t, &fakeCharges{}, http.MethodGet, "/v1/issuance-exceptions?limit="+limit, "operations:ops-1", "")
		assert.Equal(t, http.StatusBadRequest, rec.Code, limit)
	}
	for err, status := range map[error]int{
		billing.ErrInvalidLimit:     http.StatusBadRequest,
		security.ErrForbidden:       http.StatusForbidden,
		errors.New("database down"): http.StatusServiceUnavailable,
	} {
		rec := callCharges(t, &fakeCharges{err: err}, http.MethodGet, "/v1/issuance-exceptions", "operations:ops-1", "")
		assert.Equal(t, status, rec.Code, err.Error())
	}
}

func TestResumeIsForOperationsWithANote(t *testing.T) {
	resumed := pendingCharge()
	resumed.Attempts, resumed.ResumeCount = 0, 1
	svc := &fakeCharges{charge: resumed}

	rec := callCharges(t, svc, http.MethodPost, "/v1/acceptances/acc-1/charge/resume", "operations:ops-1",
		`{"note":"Pasarela restablecida"}`)

	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, "acc-1", svc.gotID)
	assert.Equal(t, "Pasarela restablecida", svc.gotNote)
	body := decode(t, rec)
	assert.Equal(t, "pending", body["status"])
	assert.Equal(t, "cobro-acc-1", body["idempotency_key"], "the same key")
	assert.EqualValues(t, 1, body["resume_count"])

	for _, token := range []string{"insured:cust-1", "advisor:adv-1"} {
		svc := &fakeCharges{}
		rec := callCharges(t, svc, http.MethodPost, "/v1/acceptances/acc-1/charge/resume", token, `{"note":"x"}`)
		assert.Equal(t, http.StatusForbidden, rec.Code, token)
		assert.Zero(t, svc.calls)
	}
}

func TestResumeErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		body   string
		err    error
		status int
		code   string
	}{
		"invalid json":  {`{`, nil, http.StatusBadRequest, "invalid_request"},
		"no note":       {`{}`, billing.ErrInvalidNote, http.StatusBadRequest, "invalid_request"},
		"not found":     {`{"note":"x"}`, ports.ErrChargeNotFound, http.StatusNotFound, "charge_not_found"},
		"not resumable": {`{"note":"x"}`, ports.ErrChargeNotResumable, http.StatusConflict, "charge_not_resumable"},
		"unavailable":   {`{"note":"x"}`, errors.New("db"), http.StatusServiceUnavailable, "charge_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := callCharges(t, &fakeCharges{err: tc.err}, http.MethodPost, "/v1/acceptances/acc-1/charge/resume",
				"operations:ops-1", tc.body)
			assert.Equal(t, tc.status, rec.Code)
			assert.Equal(t, tc.code, decode(t, rec)["error"])
		})
	}
	rec := callCharges(t, &fakeCharges{}, http.MethodPost, "/v1/acceptances/"+strings.Repeat("a", 129)+"/charge/resume",
		"operations:ops-1", `{"note":"x"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
