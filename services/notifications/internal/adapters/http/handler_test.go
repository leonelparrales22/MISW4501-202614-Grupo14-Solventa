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
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/ports"
)

// tokenVerifier reads "role:subject" tokens.
type tokenVerifier struct{}

func (tokenVerifier) Verify(_ context.Context, raw string) (security.Claims, error) {
	role, subject, ok := strings.Cut(raw, ":")
	if !ok {
		return security.Claims{}, errors.New("bad token")
	}
	return security.Claims{Subject: subject, Roles: []security.Role{security.Role(role)}}, nil
}

var paidAt = time.Date(2026, 9, 11, 21, 40, 0, 0, time.UTC)

func notification() domain.Notification {
	p := domain.ParametricPayout{
		PayoutID: "ppo-1", ClaimID: "SIN-2026-01702", PolicyID: "VJ-2026-000931", Subject: "sub-a", AmountMinor: 18_000_000,
		Currency: "COP", Trigger: domain.Trigger{Parameter: "flight_delay", EventReference: "AV-8421", Threshold: 180,
			Observed: 215, Unit: "minutes", ObservedAt: paidAt.Add(-time.Minute)},
		Method: domain.PaymentMethod{Brand: "visa", Last4: "1111"}, IdempotencyKey: "pay-ppo-1", PaidAt: paidAt,
	}
	return domain.NewPayoutNotification(p, paidAt.Add(2*time.Second))
}

type fakeCenter struct {
	items   []domain.Notification
	err     error
	limit   int
	readID  string
	allRead bool
	device  domain.Device
}

func (f *fakeCenter) List(_ context.Context, limit int) ([]domain.Notification, int, error) {
	f.limit = limit
	return f.items, len(f.items), f.err
}

func (f *fakeCenter) Get(_ context.Context, id string) (domain.Notification, error) {
	for _, n := range f.items {
		if n.ID == id {
			return n, nil
		}
	}
	if f.err != nil {
		return domain.Notification{}, f.err
	}
	return domain.Notification{}, ports.ErrNotificationNotFound
}

func (f *fakeCenter) MarkRead(_ context.Context, id string) error {
	f.readID = id
	return f.err
}

func (f *fakeCenter) MarkAllRead(context.Context) error {
	f.allRead = true
	return f.err
}

func (f *fakeCenter) RegisterDevice(_ context.Context, d domain.Device) (domain.Device, error) {
	if f.err != nil {
		return domain.Device{}, f.err
	}
	d.UpdatedAt = paidAt
	f.device = d
	return d, nil
}

func call(t *testing.T, svc *fakeCenter, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	httpadapter.NewRouter(httpadapter.WithObservability("notifications"), httpadapter.WithCenter(svc, tokenVerifier{})).
		ServeHTTP(rec, req)
	return rec
}

func TestTheListCarriesEverythingTheCenterAndTheDetailShow(t *testing.T) {
	svc := &fakeCenter{items: []domain.Notification{notification()}}

	rec := call(t, svc, http.MethodGet, "/v1/notifications?limit=20", "insured:sub-a", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, 20, svc.limit)
	assert.JSONEq(t, `{"unread_count":1,"items":[{"notification_id":"ntf-ppo-1","kind":"parametric_payout",
		"occurred_at":"2026-09-11T21:40:00Z","created_at":"2026-09-11T21:40:02Z","read_at":null,"stage":null,"expiry":null,
		"payout":{"payout_id":"ppo-1","claim_id":"SIN-2026-01702","policy_id":"VJ-2026-000931","amount":180000,
		"currency":"COP","trigger":{"parameter":"flight_delay","event_reference":"AV-8421","threshold":180,"observed":215,
		"unit":"minutes","observed_at":"2026-09-11T21:39:00Z"},"payment_method":{"brand":"visa","last4":"1111"},
		"idempotency_key":"pay-ppo-1","paid_at":"2026-09-11T21:40:00Z"}}]}`, rec.Body.String())
}

func TestAnEmptyCenterIsAnEmptyList(t *testing.T) {
	rec := call(t, &fakeCenter{}, http.MethodGet, "/v1/notifications", "insured:sub-a", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"items":[]`)
}

func TestTheListRefusesABadLimit(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, call(t, &fakeCenter{}, http.MethodGet, "/v1/notifications?limit=x", "insured:sub-a", "").Code)
	assert.Equal(t, http.StatusBadRequest, call(t, &fakeCenter{}, http.MethodGet, "/v1/notifications?limit=0", "insured:sub-a", "").Code)
}

func TestOneNotificationAndOneThatIsNotTheirs(t *testing.T) {
	svc := &fakeCenter{items: []domain.Notification{notification()}}

	ok := call(t, svc, http.MethodGet, "/v1/notifications/ntf-ppo-1", "insured:sub-a", "")
	missing := call(t, svc, http.MethodGet, "/v1/notifications/ntf-other", "insured:sub-a", "")

	require.Equal(t, http.StatusOK, ok.Code)
	assert.Contains(t, ok.Body.String(), `"event_reference":"AV-8421"`)
	assert.Equal(t, http.StatusNotFound, missing.Code)
	assert.Contains(t, missing.Body.String(), "notification_not_found")
	assert.Equal(t, http.StatusBadRequest, call(t, svc, http.MethodGet, "/v1/notifications/"+strings.Repeat("x", 129),
		"insured:sub-a", "").Code)
}

func TestReadingOneOrAll(t *testing.T) {
	svc := &fakeCenter{}

	assert.Equal(t, http.StatusNoContent, call(t, svc, http.MethodPost, "/v1/notifications/ntf-ppo-1/read", "insured:sub-a", "").Code)
	assert.Equal(t, "ntf-ppo-1", svc.readID)
	assert.Equal(t, http.StatusNoContent, call(t, svc, http.MethodPost, "/v1/notifications/read-all", "insured:sub-a", "").Code)
	assert.True(t, svc.allRead)

	svc.err = ports.ErrNotificationNotFound
	assert.Equal(t, http.StatusNotFound, call(t, svc, http.MethodPost, "/v1/notifications/x/read", "insured:sub-a", "").Code)
}

func TestRegisterADevice(t *testing.T) {
	svc := &fakeCenter{}

	rec := call(t, svc, http.MethodPut, "/v1/devices/inst-1", "insured:sub-a",
		`{"platform":"android","token":"tok-1","push_enabled":true,"locale":"es"}`)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, domain.Device{InstallationID: "inst-1", Platform: "android", Token: "tok-1", PushEnabled: true,
		Locale: "es", UpdatedAt: paidAt}, svc.device)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.NotContains(t, body, "token", "the token is never answered back")
	assert.Equal(t, true, body["push_enabled"])
}

func TestAnInvalidDeviceIsABadRequest(t *testing.T) {
	assert.Equal(t, http.StatusBadRequest, call(t, &fakeCenter{}, http.MethodPut, "/v1/devices/inst-1", "insured:sub-a",
		`{"platform":"android","unknown":1}`).Code)
	assert.Equal(t, http.StatusBadRequest, call(t, &fakeCenter{err: domain.ErrInvalidDevice}, http.MethodPut,
		"/v1/devices/inst-1", "insured:sub-a", `{"platform":"ios","locale":"es"}`).Code)
}

func TestEveryRouteIsOnlyForTheInsured(t *testing.T) {
	for _, route := range [][2]string{
		{http.MethodGet, "/v1/notifications"}, {http.MethodGet, "/v1/notifications/ntf-1"},
		{http.MethodPost, "/v1/notifications/ntf-1/read"}, {http.MethodPost, "/v1/notifications/read-all"},
		{http.MethodPut, "/v1/devices/inst-1"},
	} {
		assert.Equal(t, http.StatusUnauthorized, call(t, &fakeCenter{}, route[0], route[1], "", "{}").Code, route[1])
		assert.Equal(t, http.StatusForbidden, call(t, &fakeCenter{}, route[0], route[1], "operations:ops", "{}").Code, route[1])
	}
}

func TestAStoreFailureIsA503(t *testing.T) {
	rec := call(t, &fakeCenter{err: errors.New("database down")}, http.MethodGet, "/v1/notifications", "insured:sub-a", "")

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "notifications_unavailable")
}

func TestAMissingCallerIsA401(t *testing.T) {
	rec := call(t, &fakeCenter{err: security.ErrUnauthenticated}, http.MethodPost, "/v1/notifications/read-all", "insured:sub-a", "")

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHealth(t *testing.T) {
	rec := httptest.NewRecorder()
	httpadapter.NewRouter().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}

// HU-M3.3-01: a change of stage carries what the center shows (the stage, the
// claim and when it changed) and the id the app opens the timeline with.
func TestAChangeOfStageCarriesItsStageItsClaimAndItsAmount(t *testing.T) {
	amount := int64(413_250_000)
	changed := time.Date(2026, 9, 20, 14, 40, 5, 0, time.UTC)
	c := domain.ClaimStageChange{ChangeID: 42, ClaimID: "cl-1", FilingNumber: "SIN-2026-01655", PolicyID: "HG-2025-880231",
		Subject: "sub-a", Stage: domain.StageApproved, ChangedAt: changed, AmountMinor: &amount, Currency: "COP"}
	svc := &fakeCenter{items: []domain.Notification{domain.NewStageNotification(c, changed)}}

	rec := call(t, svc, http.MethodGet, "/v1/notifications", "insured:sub-a", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"unread_count":1,"items":[{"notification_id":"ntf-stage-42","kind":"claim_stage_changed",
		"occurred_at":"2026-09-20T14:40:05Z","created_at":"2026-09-20T14:40:05Z","read_at":null,"payout":null,"expiry":null,
		"stage":{"change_id":42,"claim_id":"cl-1","filing_number":"SIN-2026-01655","policy_id":"HG-2025-880231",
		"stage":"approved","changed_at":"2026-09-20T14:40:05Z","amount":4132500,"currency":"COP","paid_at":null}}]}`,
		rec.Body.String())
}

// HU-M3.2-01: an expiry alert carries the policy, its exact expiry date and
// the days left; a source that does not name the product leaves it null.
func TestAnExpiryAlertCarriesThePolicyItsDateAndTheDaysLeft(t *testing.T) {
	detected := time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC)
	core := domain.ExpiryAlert{PolicyID: "AU-2025-118204", Subject: "sub-a", Source: domain.ExpirySourcePolicyCore,
		Line: "auto", Product: "Autos", InsuredObject: "KJT-482", CoverageEnd: time.Date(2026, 11, 30, 5, 0, 0, 0, time.UTC),
		MilestoneDays: 30, DaysLeft: 16, DetectedAt: detected}
	life := domain.ExpiryAlert{PolicyID: "pol-acc-1", Subject: "sub-a", Source: domain.ExpirySourceOrigination,
		Line: "life", CoverageEnd: time.Date(2026, 11, 19, 4, 59, 59, 0, time.UTC), MilestoneDays: 5, DaysLeft: 4,
		DetectedAt: detected}
	svc := &fakeCenter{items: []domain.Notification{domain.NewExpiryNotification(core, detected),
		domain.NewExpiryNotification(life, detected)}}

	rec := call(t, svc, http.MethodGet, "/v1/notifications", "insured:sub-a", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"unread_count":2,"items":[
		{"notification_id":"ntf-expiry-AU-2025-118204-20261130-30","kind":"policy_expiry",
		"occurred_at":"2026-11-14T15:00:00Z","created_at":"2026-11-14T15:00:00Z","read_at":null,"payout":null,"stage":null,
		"expiry":{"policy_id":"AU-2025-118204","line":"auto","product":"Autos","insured_object":"KJT-482",
		"coverage_end":"2026-11-30T05:00:00Z","milestone_days":30,"days_left":16}},
		{"notification_id":"ntf-expiry-pol-acc-1-20261118-5","kind":"policy_expiry",
		"occurred_at":"2026-11-14T15:00:00Z","created_at":"2026-11-14T15:00:00Z","read_at":null,"payout":null,"stage":null,
		"expiry":{"policy_id":"pol-acc-1","line":"life","product":null,"insured_object":null,
		"coverage_end":"2026-11-19T04:59:59Z","milestone_days":5,"days_left":4}}]}`, rec.Body.String())

	one := call(t, svc, http.MethodGet, "/v1/notifications/ntf-expiry-AU-2025-118204-20261130-30", "insured:sub-a", "")
	require.Equal(t, http.StatusOK, one.Code)
	assert.Contains(t, one.Body.String(), `"milestone_days":30`)
}
