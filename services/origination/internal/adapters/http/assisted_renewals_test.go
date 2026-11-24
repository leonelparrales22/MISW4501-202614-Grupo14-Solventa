package httpadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/formalization"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

// HU-W1.5-03: the routes of the assisted renewal.

type fakeAssistedRenewals struct {
	view      renewal.AssistedView
	requote   renewal.RequoteResult
	sent      renewal.ConsentCodeSent
	created   bool
	err       error
	gotPolicy string
	gotIn     renewal.RequoteInput
	gotCode   string
	gotForm   renewal.FormalizeInput
}

func (f *fakeAssistedRenewals) View(_ context.Context, id string) (renewal.AssistedView, error) {
	f.gotPolicy = id
	return f.view, f.err
}

func (f *fakeAssistedRenewals) Requote(_ context.Context, id string, in renewal.RequoteInput) (renewal.RequoteResult, error) {
	f.gotPolicy, f.gotIn = id, in
	return f.requote, f.err
}

func (f *fakeAssistedRenewals) RequestConsentCode(_ context.Context, id, requoteID string) (renewal.ConsentCodeSent, error) {
	f.gotPolicy, f.gotCode = id, requoteID
	return f.sent, f.err
}

func (f *fakeAssistedRenewals) Formalize(_ context.Context, id string, in renewal.FormalizeInput) (renewal.AssistedView, bool, error) {
	f.gotPolicy, f.gotForm = id, in
	return f.view, f.created, f.err
}

var requotedOn = time.Date(2026, 9, 16, 21, 2, 0, 0, time.UTC)

func sampleRequoteSnapshot() domain.RenewalRequote {
	return domain.RenewalRequote{
		ID: "rq-1", PolicyID: "pol-1", PeriodEnd: policyEndsAt, AdvisorID: "sub-advisor",
		Terms:   domain.CoverageTerms{InsuredAmount: 68_000_000, TermMonths: 12, Coverages: []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 68_000_000}}},
		Premium: 149_800, Currency: "COP", DiscountRate: 0.08, Requoted: true, RatingVersion: "v1",
		AppliedRules: []domain.AppliedRule{{Code: "base_monthly_rate", Value: 0.00045}},
		Mode:         domain.ModeProvisional, ProfileSource: domain.SourceCache, ProfileNotice: domain.ProfileNoticeLastProfileInUse,
		ProfileScore: 0.31, ProfileCategory: "standard_secret", ProfileDate: requotedOn.Add(-20 * 24 * time.Hour), UnavailableSource: "open_finance",
		Sources:           []domain.SourceOutcome{{Source: "open_finance", Status: domain.SourceStatusTimeout, Latency: 120 * time.Millisecond}},
		ProfileCustomerID: "partner-cust-1", OriginChannel: domain.OfferChannelEmbedded, ConsentBasis: domain.ConsentBasisAuditVerified, ConsentID: "con-1",
		CreatedAt: requotedOn, ExpiresAt: requotedOn.Add(24 * time.Hour),
	}
}

func callAssisted(t *testing.T, svc *fakeAssistedRenewals, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithObservability("origination"),
		httpadapter.WithAssistedRenewals(svc, tokenVerifier{})).ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	return out
}

func TestOnlyAnAdvisorReachesTheAssistedRenewal(t *testing.T) {
	routes := []struct{ method, path string }{
		{http.MethodGet, "/v1/policies/pol-1/assisted-renewal"},
		{http.MethodPost, "/v1/policies/pol-1/assisted-renewal/requotes"},
		{http.MethodPost, "/v1/policies/pol-1/assisted-renewal/consent"},
		{http.MethodPost, "/v1/policies/pol-1/assisted-renewal/formalization"},
	}
	for _, r := range routes {
		svc := &fakeAssistedRenewals{}
		assert.Equal(t, http.StatusUnauthorized, callAssisted(t, svc, r.method, r.path, "", `{}`).Code, r.path)
		for _, role := range []string{"insured", "operations", "partner"} {
			rec := callAssisted(t, svc, r.method, r.path, role+":sub-1", `{}`)
			assert.Equal(t, http.StatusForbidden, rec.Code, role+" "+r.path)
		}
		assert.Empty(t, svc.gotPolicy, "the use case is never called")
	}
}

func TestTheViewOfTheAssistedRenewalShowsTheScreenWithoutSecrets(t *testing.T) {
	p := renewalPolicy()
	p.Holder.DocumentID = "1020345678"
	rq := sampleRequoteSnapshot()
	method := domain.PaymentMethod{Kind: domain.PaymentCard, Token: "tok_secret", Brand: "visa", Last4: "1111", Installments: 1, CapturedAt: requotedOn}
	r := domain.Renewal{ID: "ren-pol-1-20261002", PeriodEnd: rq.PeriodEnd, Status: domain.RenewalAccepted, ChargeID: "ren-pol-1-20261002",
		AcceptedAt: requotedOn, RetryUntil: requotedOn.Add(72 * time.Hour), Terms: rq.Terms, Premium: rq.Premium, Currency: rq.Currency,
		DiscountRate: rq.DiscountRate, Requoted: true}
	svc := &fakeAssistedRenewals{view: renewal.AssistedView{
		Policy: p, WindowOpensAt: policyEndsAt.AddDate(0, 0, -30), WindowClosesAt: policyEndsAt, WindowOpen: true,
		PaymentMethod: &method, Requote: &rq, RequoteStatus: domain.RequoteValid, Renewal: &r, Channel: renewal.ChannelAdvisor,
		Formalization: &domain.RenewalFormalization{RenewalID: r.ID, RequoteID: "rq-1", AdvisorID: "sub-advisor", AdvisorEmail: "asesor@example.com",
			AdvisorName: "Laura Gómez", Acceptance: domain.ConsentAcceptance{Method: domain.ConsentRecordedCall, AcceptedAt: requotedOn, Reference: "GRAB-1"},
			FormalizedAt: requotedOn.Add(time.Minute)},
		Charge: &domain.Charge{Status: domain.ChargeFailed, FailureReason: domain.ChargePaymentRejected, DeclineCode: "51",
			LastFailureAt: requotedOn.Add(2 * time.Minute), Attempts: 1, PaymentMethod: method},
		ConsentMode: renewal.ConsentModeAudit, BlockedReason: renewal.BlockedClosed,
	}}

	rec := callAssisted(t, svc, http.MethodGet, "/v1/policies/pol-1/assisted-renewal", "advisor:sub-advisor", "")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "pol-1", svc.gotPolicy)
	body := decodeBody(t, rec)
	assert.Equal(t, "audit", body["consent_mode"])
	assert.Equal(t, "renewal_closed", body["blocked_reason"])
	assert.Equal(t, false, body["can_formalize"])
	assert.Equal(t, map[string]any{"full_name": "Carolina Pérez", "document_type": nil, "document_id": "1020345678"}, body["policy"].(map[string]any)["holder"])
	assert.Equal(t, true, body["renewal_window"].(map[string]any)["open"])
	renewalBody := body["renewal"].(map[string]any)
	assert.Equal(t, "advisor", renewalBody["channel"])
	assert.Equal(t, "2026-09-19T21:02:00Z", renewalBody["retry_until"], "until when the insured can retry (01·D-3 a+)")
	terms := renewalBody["terms"].(map[string]any)
	assert.Equal(t, rq.Premium, terms["premium"], "the conditions accepted for the period, from either channel")
	assert.Equal(t, "2026-10-02T23:59:00Z", terms["coverage_start"])
	assert.Equal(t, "2027-10-02T23:59:00Z", terms["coverage_end"])
	assert.InDelta(t, 0.0527, renewalBody["premium_change_rate"], 0.0001)
	assert.Equal(t, rq.DiscountRate, renewalBody["discount_rate"])
	charge := body["charge"].(map[string]any)
	assert.Equal(t, "failed", charge["status"])
	assert.Equal(t, "payment_rejected", charge["failure_reason"])
	assert.Equal(t, "51", charge["decline_code"])
	assert.Equal(t, "2026-09-16T21:04:00Z", charge["last_failure_at"])
	f := body["formalization"].(map[string]any)
	assert.Equal(t, "Laura Gómez", f["advisor_name"])
	assert.Equal(t, "recorded_call", f["acceptance"].(map[string]any)["method"])
	requote := body["requote"].(map[string]any)
	assert.Equal(t, "last_profile_in_use", requote["notice"])
	assert.Equal(t, "cache", requote["source"])
	assert.Equal(t, "valid", requote["status"])
	raw := rec.Body.String()
	for _, secret := range []string{"tok_secret", "score", "standard_secret", "partner-cust-1", "con-1", "carolina@example.com"} {
		assert.NotContains(t, raw, secret)
	}
}

func TestTheViewTellsTheConsentModeAndWhatBlocksIt(t *testing.T) {
	svc := &fakeAssistedRenewals{view: renewal.AssistedView{Policy: renewalPolicy(), ConsentMode: renewal.ConsentModeAdvisorAffirmation,
		CanRequote: true, BlockedReason: renewal.BlockedPaymentMethodMissing}}

	body := decodeBody(t, callAssisted(t, svc, http.MethodGet, "/v1/policies/pol-1/assisted-renewal", "advisor:a", ""))

	assert.Equal(t, "advisor_affirmation", body["consent_mode"], "S7 shows the checkbox")
	assert.Equal(t, "payment_method_missing", body["blocked_reason"])
	assert.Nil(t, body["requote"])
	assert.Nil(t, body["renewal"])
	assert.Nil(t, body["charge"])
	assert.Nil(t, body["payment_method"])

	svc.view.BlockedReason = renewal.BlockedNone
	body = decodeBody(t, callAssisted(t, svc, http.MethodGet, "/v1/policies/pol-1/assisted-renewal", "advisor:a", ""))
	assert.Nil(t, body["blocked_reason"])
}

func TestARequoteAnswersTheRenewedConditionsAndHowTheyWerePriced(t *testing.T) {
	svc := &fakeAssistedRenewals{requote: renewal.RequoteResult{Policy: renewalPolicy(), Requote: sampleRequoteSnapshot()}}

	rec := callAssisted(t, svc, http.MethodPost, "/v1/policies/pol-1/assisted-renewal/requotes", "advisor:a", `{"consent_affirmed": true}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.True(t, svc.gotIn.ConsentAffirmed)
	body := decodeBody(t, rec)
	assert.Equal(t, "rq-1", body["requote_id"])
	assert.Equal(t, "valid", body["status"])
	assert.InDelta(t, 0.0527, body["premium_change_rate"], 0.0001)
	assert.Equal(t, "v1", body["rating_version"])
	assert.Equal(t, []any{map[string]any{"code": "base_monthly_rate", "value": 0.00045}}, body["applied_rules"])
	assert.Equal(t, []any{map[string]any{"source": "open_finance", "status": "timeout", "latency_ms": float64(120)}}, body["sources"])
	assert.Equal(t, "provisional", body["mode"])
	assert.Equal(t, "2026-08-27T21:02:00Z", body["profile_date"])
	assert.Equal(t, "audit_verified", body["consent_basis"])
	assert.Nil(t, body["consent_affirmed_at"])
	assert.Nil(t, body["consent_affirmed_by_email"], "only an affirmed consent says who affirmed it")
	assert.Nil(t, body["consent_affirmed_by_name"])
	renewed := body["renewal"].(map[string]any)
	assert.Equal(t, "2026-10-02T23:59:00Z", renewed["coverage_start"])
	assert.Equal(t, "2027-10-02T23:59:00Z", renewed["coverage_end"])
	assert.Equal(t, float64(142_300), body["current"].(map[string]any)["premium"])
	assert.NotContains(t, rec.Body.String(), "score")
	assert.NotContains(t, rec.Body.String(), "standard_secret")
}

func TestARequoteBodyIsOptionalButMustBeJSON(t *testing.T) {
	svc := &fakeAssistedRenewals{requote: renewal.RequoteResult{Policy: renewalPolicy(), Requote: sampleRequoteSnapshot()}}

	rec := callAssisted(t, svc, http.MethodPost, "/v1/policies/pol-1/assisted-renewal/requotes", "advisor:a", "")
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.False(t, svc.gotIn.ConsentAffirmed)

	rec = callAssisted(t, svc, http.MethodPost, "/v1/policies/pol-1/assisted-renewal/requotes", "advisor:a", `{"consent_affirmed":`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestAnAssistedPolicyWithoutTheCheckboxIsAValidationError(t *testing.T) {
	svc := &fakeAssistedRenewals{err: domain.RequireConsentAffirmation(false)}

	rec := callAssisted(t, svc, http.MethodPost, "/v1/policies/pol-1/assisted-renewal/requotes", "advisor:a", `{}`)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	body := decodeBody(t, rec)
	assert.Equal(t, "validation_failed", body["error"])
	assert.Equal(t, []any{map[string]any{"field": "consent_affirmed", "code": "required"}}, body["fields"])
}

func TestTheConfirmationCodeOfARenewalIsSentToTheInsured(t *testing.T) {
	svc := &fakeAssistedRenewals{sent: renewal.ConsentCodeSent{RequoteID: "rq-1", SentTo: "ca***@example.com", ExpiresAt: requotedOn.Add(10 * time.Minute)}}

	rec := callAssisted(t, svc, http.MethodPost, "/v1/policies/pol-1/assisted-renewal/consent", "advisor:a", `{"requote_id":"rq-1"}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "rq-1", svc.gotCode)
	assert.Equal(t, map[string]any{"requote_id": "rq-1", "sent_to": "ca***@example.com", "expires_at": "2026-09-16T21:12:00Z"}, decodeBody(t, rec))

	rec = callAssisted(t, svc, http.MethodPost, "/v1/policies/pol-1/assisted-renewal/consent", "advisor:a", `{}`)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, []any{map[string]any{"field": "requote_id", "code": "required"}}, decodeBody(t, rec)["fields"])

	rec = callAssisted(t, svc, http.MethodPost, "/v1/policies/pol-1/assisted-renewal/consent", "advisor:a", `[`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestFormalizingTheRenewalAnswersTheView(t *testing.T) {
	view := renewal.AssistedView{Policy: renewalPolicy(), ConsentMode: renewal.ConsentModeAudit, BlockedReason: renewal.BlockedClosed}
	svc := &fakeAssistedRenewals{view: view, created: true}
	body := `{"requote_id":" rq-1 ","acceptance":{"method":"confirmation_code","accepted_at":"2026-09-16T21:05:00Z","reference":""},
		"confirmation_code":"482913"}`

	rec := callAssisted(t, svc, http.MethodPost, "/v1/policies/pol-1/assisted-renewal/formalization", "advisor:a", body)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "/v1/policies/pol-1/assisted-renewal", rec.Header().Get("Location"))
	assert.Equal(t, renewal.FormalizeInput{RequoteID: "rq-1", Code: "482913", Acceptance: domain.ConsentAcceptance{
		Method: domain.ConsentConfirmationCode, AcceptedAt: time.Date(2026, 9, 16, 21, 5, 0, 0, time.UTC)}}, svc.gotForm)
	assert.NotContains(t, rec.Body.String(), "482913", "the code never comes back")

	svc.created = false
	rec = callAssisted(t, svc, http.MethodPost, "/v1/policies/pol-1/assisted-renewal/formalization", "advisor:a", body)
	assert.Equal(t, http.StatusOK, rec.Code, "the same advisor repeating it")
}

func TestAMalformedFormalizationIsRefusedBeforeTheUseCase(t *testing.T) {
	svc := &fakeAssistedRenewals{}

	rec := callAssisted(t, svc, http.MethodPost, "/v1/policies/pol-1/assisted-renewal/formalization", "advisor:a",
		`{"acceptance":{"method":"confirmation_code","accepted_at":"16/09/2026"},"confirmation_code":"12"}`)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.ElementsMatch(t, []any{
		map[string]any{"field": "requote_id", "code": "required"},
		map[string]any{"field": "acceptance.accepted_at", "code": "invalid_format"},
		map[string]any{"field": "confirmation_code", "code": "invalid_format"},
	}, decodeBody(t, rec)["fields"])
	assert.Empty(t, svc.gotPolicy)

	rec = callAssisted(t, svc, http.MethodPost, "/v1/policies/pol-1/assisted-renewal/formalization", "advisor:a", `nope`)
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestEveryErrorOfTheAssistedRenewalHasItsAnswer(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{ports.ErrPolicyNotFound, http.StatusNotFound, "policy_not_found"},
		{ports.ErrRenewalRequoteNotFound, http.StatusNotFound, "requote_not_found"},
		{domain.ErrRenewalNotAvailable, http.StatusConflict, "renewal_not_available"},
		{domain.ErrRenewalClosed, http.StatusConflict, "renewal_closed"},
		{domain.ErrAssistedRenewalTaken, http.StatusConflict, "already_formalized"},
		{domain.ErrPaymentMethodMissing, http.StatusConflict, "payment_method_missing"},
		{domain.ErrRequoteExpired, http.StatusUnprocessableEntity, "requote_expired"},
		{domain.ErrRequoteSuperseded, http.StatusUnprocessableEntity, "requote_superseded"},
		{profiling.ErrConsentRequired, http.StatusUnprocessableEntity, "consent_required"},
		{fmt.Errorf("%w: timeout", ports.ErrConsentUnverifiable), http.StatusServiceUnavailable, "consent_unverifiable"},
		{&domain.ValidationError{Fields: []domain.FieldError{{Field: "acceptance.reference", Code: "required"}}}, http.StatusUnprocessableEntity, "validation_failed"},
		{formalization.ErrConsentCodeRequired, http.StatusUnprocessableEntity, "consent_code_required"},
		{formalization.ErrConsentCodeInvalid, http.StatusUnprocessableEntity, "invalid_confirmation_code"},
		{formalization.ErrConsentCodeExpired, http.StatusUnprocessableEntity, "confirmation_code_expired"},
		{formalization.ErrConsentCodeLocked, http.StatusTooManyRequests, "too_many_attempts"},
		{formalization.ErrConsentCodeLimit, http.StatusTooManyRequests, "too_many_codes"},
		{fmt.Errorf("%w: smtp", formalization.ErrCodeDeliveryUnavailable), http.StatusServiceUnavailable, "code_delivery_unavailable"},
		{errors.New("db down"), http.StatusServiceUnavailable, "renewal_unavailable"},
	} {
		svc := &fakeAssistedRenewals{err: tc.err}
		for _, call := range []struct{ method, path, body string }{
			{http.MethodGet, "/v1/policies/pol-1/assisted-renewal", ""},
			{http.MethodPost, "/v1/policies/pol-1/assisted-renewal/requotes", `{}`},
			{http.MethodPost, "/v1/policies/pol-1/assisted-renewal/consent", `{"requote_id":"rq-1"}`},
			{http.MethodPost, "/v1/policies/pol-1/assisted-renewal/formalization",
				`{"requote_id":"rq-1","acceptance":{"method":"recorded_call","accepted_at":"2026-09-16T21:05:00Z","reference":"GRAB-1"}}`},
		} {
			rec := callAssisted(t, svc, call.method, call.path, "advisor:a", call.body)
			assert.Equal(t, tc.status, rec.Code, "%v %s", tc.err, call.path)
			assert.Equal(t, tc.code, decodeBody(t, rec)["error"], "%v %s", tc.err, call.path)
		}
	}
}

func TestAnInvalidPolicyIDIsABadRequest(t *testing.T) {
	long := strings.Repeat("p", 200)
	for _, call := range []struct{ method, path string }{
		{http.MethodGet, "/v1/policies/" + long + "/assisted-renewal"},
		{http.MethodPost, "/v1/policies/" + long + "/assisted-renewal/requotes"},
		{http.MethodPost, "/v1/policies/" + long + "/assisted-renewal/consent"},
		{http.MethodPost, "/v1/policies/" + long + "/assisted-renewal/formalization"},
	} {
		rec := callAssisted(t, &fakeAssistedRenewals{}, call.method, call.path, "advisor:a", `{}`)
		assert.Equal(t, http.StatusBadRequest, rec.Code, call.path)
	}
}

func TestAnIDNoRowCanHoldIsNotFound(t *testing.T) {
	svc := &fakeAssistedRenewals{}

	rec := callAssisted(t, svc, http.MethodGet, "/v1/policies/pol%00x/assisted-renewal", "advisor:a", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "policy_not_found", decodeBody(t, rec)["error"])

	rec = callAssisted(t, svc, http.MethodPost, "/v1/policies/pol-1/assisted-renewal/consent", "advisor:a", `{"requote_id":"rq\u0000"}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "requote_not_found", decodeBody(t, rec)["error"])

	rec = callAssisted(t, svc, http.MethodPost, "/v1/policies/pol-1/assisted-renewal/formalization", "advisor:a",
		`{"requote_id":"rq\u0000","acceptance":{"method":"recorded_call","accepted_at":"2026-09-16T21:05:00Z","reference":"GRAB-1"}}`)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "requote_not_found", decodeBody(t, rec)["error"])
	assert.Empty(t, svc.gotPolicy, "the use case is never called")
}
