package httpadapter_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/billing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

func monitorFixture() billing.IssuanceOverview {
	return billing.IssuanceOverview{
		Counts: domain.IssuanceCounts{Accepted: 1, Charging: 1, ChargingWithRetries: 1, ActiveToday: 2, NotifiedToday: 1, Exceptions: 1, Unattended: 1},
		Items: []domain.Issuance{
			{AcceptanceID: "acc-5", OfferID: "off-5", CustomerID: "cust-5", CustomerName: "Marta Ruiz", Channel: domain.ChannelWeb,
				Status: domain.IssuanceNotified, Step: domain.StepCompleted, AcceptedAt: chargeAt, UpdatedAt: chargeAt,
				AmountMinor: 8_950_000, Currency: "COP", PolicyID: "pol-acc-5", NotifiedAt: chargeAt.Add(time.Minute)},
			{AcceptanceID: "acc-4", OfferID: "off-4", CustomerID: "cust-4", Channel: domain.ChannelMobile,
				Status: domain.IssuanceAccepted, Step: domain.StepUnderwriting, AcceptedAt: chargeAt, UpdatedAt: chargeAt},
			{AcceptanceID: "acc-3", OfferID: "off-3", CustomerID: "cust-3", CustomerName: "Jorge Molina", Channel: domain.ChannelWeb,
				Status: domain.IssuanceCharging, Step: domain.StepPremiumCharge, AcceptedAt: chargeAt, UpdatedAt: chargeAt,
				AmountMinor: 8_950_000, Currency: "COP", Attempts: 2},
			{AcceptanceID: "acc-2", OfferID: "off-2", CustomerID: "cust-2", Channel: domain.ChannelWeb,
				Status: domain.IssuanceActive, Step: domain.StepNotification, AcceptedAt: chargeAt, UpdatedAt: chargeAt,
				AmountMinor: 8_950_000, Currency: "COP", PolicyID: "pol-acc-2"},
			{AcceptanceID: "acc-1", OfferID: "off-1", CustomerID: "cust-1", CustomerName: "Luis Castro", Channel: domain.ChannelWeb,
				Status: domain.IssuanceException, Step: domain.StepContractDocument, AcceptedAt: chargeAt, UpdatedAt: chargeAt,
				AmountMinor: 8_950_000, Currency: "COP", Attempts: 12, PolicyID: "pol-acc-1",
				FailureReason: domain.ReasonDocumentGenerationFails, FailedAt: chargeAt.Add(time.Hour), Unattended: true,
				ResumeCount: 1, LastResumedAt: chargeAt.Add(30 * time.Minute), LastResumedBy: "ops-1"},
		},
	}
}

func TestIssuancesAreForOperationsAndShowEachStatusAndTheCounts(t *testing.T) {
	svc := &fakeCharges{overview: monitorFixture()}

	rec := callCharges(t, svc, http.MethodGet,
		"/v1/issuances?status=exception&channel=web&from=2026-10-16T00:00:00Z&to=2026-10-17T00:00:00Z&day_from=2026-10-16T05:00:00Z&limit=20",
		"operations:ops-1", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, domain.IssuanceFilter{
		Status: domain.IssuanceException, Channel: domain.ChannelWeb, From: time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC),
		To: time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), Limit: 20,
	}, svc.gotFilter)
	assert.Equal(t, time.Date(2026, 10, 16, 5, 0, 0, 0, time.UTC), svc.gotDayFrom, "the start of the day of the caller")
	body := decode(t, rec)
	assert.Equal(t, map[string]any{
		"accepted": float64(1), "charging": float64(1), "charging_with_retries": float64(1), "active_today": float64(2),
		"notified_today": float64(1), "exceptions": float64(1), "unattended": float64(1),
	}, body["counts"])
	items := body["items"].([]any)
	require.Len(t, items, 5)

	notified := items[0].(map[string]any)
	assert.Equal(t, "notified", notified["status"])
	assert.Equal(t, "completed", notified["step"])
	assert.Equal(t, "Marta Ruiz", notified["customer_name"])
	assert.Equal(t, "2026-10-16T15:01:00Z", notified["notified_at"])

	accepted := items[1].(map[string]any)
	assert.Equal(t, "accepted", accepted["status"])
	assert.Equal(t, "mobile", accepted["channel"])
	for _, k := range []string{"customer_name", "amount", "currency", "policy_id", "reason", "failed_at", "notified_at", "last_resumed_by"} {
		v, present := accepted[k]
		assert.True(t, present, k)
		assert.Nil(t, v, k)
	}
	assert.Equal(t, false, accepted["unattended"])

	assert.Nil(t, items[3].(map[string]any)["reason"], "only an exception has a reason")
	assert.Equal(t, map[string]any{
		"acceptance_id": "acc-1", "offer_id": "off-1", "customer_id": "cust-1", "customer_name": "Luis Castro", "channel": "web",
		"status": "exception", "step": "contract_document", "accepted_at": "2026-10-16T15:00:00Z", "updated_at": "2026-10-16T15:00:00Z",
		"amount": float64(89_500), "currency": "COP", "attempts": float64(12), "policy_id": "pol-acc-1",
		"reason": "document_generation_failed", "failed_at": "2026-10-16T16:00:00Z", "notified_at": nil, "unattended": true,
		"resume_count": float64(1), "last_resumed_at": "2026-10-16T15:30:00Z", "last_resumed_by": "ops-1",
	}, items[4])

	for _, token := range []string{"insured:cust-1", "advisor:adv-1"} {
		denied := &fakeCharges{}
		assert.Equal(t, http.StatusForbidden, callCharges(t, denied, http.MethodGet, "/v1/issuances", token, "").Code, token)
		assert.Zero(t, denied.calls)
	}
	assert.Equal(t, http.StatusUnauthorized, callCharges(t, &fakeCharges{}, http.MethodGet, "/v1/issuances", "", "").Code)
}

func TestIssuancesWithNoMatchAreAnEmptyListNotAnError(t *testing.T) {
	svc := &fakeCharges{}

	rec := callCharges(t, svc, http.MethodGet, "/v1/issuances?status=active", "operations:ops-1", "")

	require.Equal(t, http.StatusOK, rec.Code)
	body := decode(t, rec)
	assert.Equal(t, []any{}, body["items"])
	assert.NotNil(t, body["counts"])
	assert.False(t, svc.gotDayFrom.IsZero(), "today starts at midnight UTC when the caller does not say")
	assert.Zero(t, svc.gotDayFrom.Hour())
}

func TestIssuancesRejectABadFilter(t *testing.T) {
	for name, query := range map[string]string{
		"unknown status":  "status=other",
		"unknown channel": "channel=fax",
		"bad from":        "from=yesterday",
		"bad to":          "to=2026-10-16",
		"bad day_from":    "day_from=today",
		"to before from":  "from=2026-10-17T00:00:00Z&to=2026-10-16T00:00:00Z",
		"limit zero":      "limit=0",
		"limit too big":   "limit=201",
		"limit text":      "limit=ten",
	} {
		t.Run(name, func(t *testing.T) {
			svc := &fakeCharges{}
			rec := callCharges(t, svc, http.MethodGet, "/v1/issuances?"+query, "operations:ops-1", "")
			assert.Equal(t, http.StatusBadRequest, rec.Code)
			assert.Equal(t, "invalid_request", decode(t, rec)["error"])
			assert.Zero(t, svc.calls)
		})
	}
}

func TestIssuancesErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"limit from the service":  {billing.ErrInvalidLimit, http.StatusBadRequest, "invalid_request"},
		"filter from the service": {billing.ErrInvalidFilter, http.StatusBadRequest, "invalid_request"},
		"forbidden":               {security.ErrForbidden, http.StatusForbidden, "forbidden"},
		"unavailable":             {errors.New("database down"), http.StatusServiceUnavailable, "charge_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := callCharges(t, &fakeCharges{err: tc.err}, http.MethodGet, "/v1/issuances", "operations:ops-1", "")
			assert.Equal(t, tc.status, rec.Code)
			assert.Equal(t, tc.code, decode(t, rec)["error"])
		})
	}
}

func TestTheActionsOfAnIssuanceAreForOperations(t *testing.T) {
	svc := &fakeCharges{actions: []domain.IssuanceAction{
		{ID: 2, AcceptanceID: "acc-1", Action: domain.IssuanceActionRetry, Actor: "ops-2", Note: "second", CreatedAt: chargeAt.Add(time.Hour)},
		{ID: 1, AcceptanceID: "acc-1", Action: domain.IssuanceActionResume, Actor: "ops-1", Note: "first", CreatedAt: chargeAt},
	}}

	rec := callCharges(t, svc, http.MethodGet, "/v1/issuances/acc-1/actions", "operations:ops-1", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "acc-1", svc.gotID)
	items := decode(t, rec)["items"].([]any)
	require.Len(t, items, 2)
	assert.Equal(t, map[string]any{
		"id": float64(2), "action": "retry", "actor": "ops-2", "note": "second", "created_at": "2026-10-16T16:00:00Z",
	}, items[0])

	empty := callCharges(t, &fakeCharges{}, http.MethodGet, "/v1/issuances/acc-9/actions", "operations:ops-1", "")
	assert.Equal(t, map[string]any{"items": []any{}}, decode(t, empty), "an empty list, not null")

	for _, token := range []string{"insured:cust-1", "advisor:adv-1"} {
		denied := &fakeCharges{}
		assert.Equal(t, http.StatusForbidden, callCharges(t, denied, http.MethodGet, "/v1/issuances/acc-1/actions", token, "").Code)
		assert.Zero(t, denied.calls)
	}
	assert.Equal(t, http.StatusServiceUnavailable, callCharges(t, &fakeCharges{err: errors.New("db")},
		http.MethodGet, "/v1/issuances/acc-1/actions", "operations:ops-1", "").Code)
}

func TestRetryingAnIssuanceIsForOperationsWithANote(t *testing.T) {
	svc := &fakeCharges{retried: domain.IssuanceActionRetry}

	rec := callCharges(t, svc, http.MethodPost, "/v1/issuances/acc-1/retry", "operations:ops-1", `{"note":"El generador volvió"}`)

	require.Equal(t, http.StatusAccepted, rec.Code)
	assert.Equal(t, map[string]any{"acceptance_id": "acc-1", "action": "retry"}, decode(t, rec))
	assert.Equal(t, "acc-1", svc.gotID)
	assert.Equal(t, "El generador volvió", svc.gotNote)

	for _, token := range []string{"insured:cust-1", "advisor:adv-1"} {
		denied := &fakeCharges{}
		assert.Equal(t, http.StatusForbidden, callCharges(t, denied, http.MethodPost, "/v1/issuances/acc-1/retry", token, `{"note":"x"}`).Code)
		assert.Zero(t, denied.calls)
	}
	assert.Equal(t, http.StatusBadRequest, callCharges(t, &fakeCharges{}, http.MethodPost, "/v1/issuances/acc-1/retry",
		"operations:ops-1", `not json`).Code)
}

func TestRetryingAnIssuanceErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"no note":       {billing.ErrInvalidNote, http.StatusBadRequest, "invalid_request"},
		"unknown":       {billing.ErrIssuanceNotFound, http.StatusNotFound, "issuance_not_found"},
		"nothing fails": {billing.ErrIssuanceNotRetryable, http.StatusConflict, "issuance_not_retryable"},
		"forbidden":     {security.ErrForbidden, http.StatusForbidden, "forbidden"},
		"unavailable":   {errors.New("database down"), http.StatusServiceUnavailable, "charge_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			rec := callCharges(t, &fakeCharges{err: tc.err}, http.MethodPost, "/v1/issuances/acc-1/retry", "operations:ops-1", `{"note":"x"}`)
			assert.Equal(t, tc.status, rec.Code)
			assert.Equal(t, tc.code, decode(t, rec)["error"])
		})
	}
}
