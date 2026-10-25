package httpadapter_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/formalization"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/issuance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

type fakeFormalizations struct {
	consent formalization.ConsentRequest
	result  formalization.Result
	err     error
	quoteID string
	input   formalization.Input
}

func (f *fakeFormalizations) RequestConsent(_ context.Context, quoteID string) (formalization.ConsentRequest, error) {
	f.quoteID = quoteID
	return f.consent, f.err
}

func (f *fakeFormalizations) Formalize(_ context.Context, quoteID string, in formalization.Input) (formalization.Result, error) {
	f.quoteID, f.input = quoteID, in
	return f.result, f.err
}

func (f *fakeFormalizations) Get(_ context.Context, quoteID string) (formalization.Result, error) {
	f.quoteID = quoteID
	return f.result, f.err
}

func formalizationRouter(svc httpadapter.FormalizationService, roles ...security.Role) http.Handler {
	if len(roles) == 0 {
		roles = []security.Role{security.RoleAdvisor}
	}
	return httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithFormalizations(svc, fixedVerifier{roles: roles}))
}

func sampleResult(created bool) formalization.Result {
	at := time.Date(2026, 10, 25, 15, 0, 0, 0, time.UTC)
	return formalization.Result{
		Created: created,
		Formalization: domain.Formalization{
			ID: "frm-q1", QuoteID: "q1", OfferID: "off-q-q1", CustomerID: "c1", AdvisorID: "adv-1", AdvisorEmail: "laura@example.com",
			Consent:      domain.ConsentAcceptance{Method: domain.ConsentRecordedCall, AcceptedAt: at.Add(-time.Minute), Reference: "GRAB-2026-88910"},
			FormalizedAt: at,
		},
		Acceptance: domain.Acceptance{ID: "acc-1", Status: domain.AcceptanceRegistered, Channel: domain.ChannelAdvisor},
	}
}

const (
	callBody = `{"acceptance":{"method":"recorded_call","accepted_at":"2026-10-25T14:59:00Z","reference":"GRAB-2026-88910"}}`
	codeBody = `{"acceptance":{"method":"confirmation_code","accepted_at":"2026-10-25T14:59:00Z"},"confirmation_code":"123456"}`
)

func TestRequestConsentReturnsWhereTheCodeWent(t *testing.T) {
	exp := time.Date(2026, 10, 25, 15, 10, 0, 0, time.UTC)
	svc := &fakeFormalizations{consent: formalization.ConsentRequest{QuoteID: "q1", SentTo: "an***@x.co", ExpiresAt: exp}}

	rec, body := callQuotes(formalizationRouter(svc), http.MethodPost, "/v1/assisted-quotes/q1/consent", "good", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "an***@x.co", body["sent_to"])
	assert.NotContains(t, rec.Body.String(), "123456", "the code never travels to the advisor")
	assert.Equal(t, "q1", svc.quoteID)
}

func TestFormalizeCreatesTheFormalization(t *testing.T) {
	svc := &fakeFormalizations{result: sampleResult(true)}

	rec, body := callQuotes(formalizationRouter(svc), http.MethodPost, "/v1/assisted-quotes/q1/formalization", "good", callBody)

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "/v1/assisted-quotes/q1/formalization", rec.Header().Get("Location"))
	assert.Equal(t, "frm-q1", body["formalization_id"])
	assert.Equal(t, "acc-1", body["acceptance_id"])
	assert.Equal(t, "adv-1", body["advisor_id"])
	assert.Equal(t, "recorded_call", body["consent_method"])
	assert.Equal(t, "GRAB-2026-88910", body["consent_reference"])
	assert.Equal(t, "2026-10-25T14:59:00Z", body["accepted_at"])
	assert.Equal(t, "laura@example.com", body["advisor_email"])
	assert.Equal(t, "registered", body["status"])
	assert.Equal(t, domain.ConsentAcceptance{
		Method: domain.ConsentRecordedCall, AcceptedAt: time.Date(2026, 10, 25, 14, 59, 0, 0, time.UTC), Reference: "GRAB-2026-88910",
	}, svc.input.Acceptance)
}

func TestRepeatedFormalizeAnswersOKWithTheSameRecord(t *testing.T) {
	svc := &fakeFormalizations{result: sampleResult(false)}

	rec, body := callQuotes(formalizationRouter(svc), http.MethodPost, "/v1/assisted-quotes/q1/formalization", "good", callBody)

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, rec.Header().Get("Location"))
	assert.Equal(t, "frm-q1", body["formalization_id"])
}

func TestGetFormalization(t *testing.T) {
	svc := &fakeFormalizations{result: sampleResult(false)}
	rec, body := callQuotes(formalizationRouter(svc), http.MethodGet, "/v1/assisted-quotes/q1/formalization", "good", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "off-q-q1", body["offer_id"])

	svc.err = ports.ErrFormalizationNotFound
	rec, body = callQuotes(formalizationRouter(svc), http.MethodGet, "/v1/assisted-quotes/q1/formalization", "good", "")
	require.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "formalization_not_found", body["error"])
}

func TestFormalizeWithTheCodeMethodPassesTheCode(t *testing.T) {
	svc := &fakeFormalizations{result: sampleResult(true)}

	rec, _ := callQuotes(formalizationRouter(svc), http.MethodPost, "/v1/assisted-quotes/q1/formalization", "good", codeBody)

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, domain.ConsentConfirmationCode, svc.input.Acceptance.Method)
	assert.Equal(t, "123456", svc.input.Code)
}

func TestFormalizeRejectsABodyThatIsNotJSON(t *testing.T) {
	svc := &fakeFormalizations{}
	for _, body := range []string{``, `{`, `[]`} {
		rec, got := callQuotes(formalizationRouter(svc), http.MethodPost, "/v1/assisted-quotes/q1/formalization", "good", body)
		require.Equal(t, http.StatusBadRequest, rec.Code, body)
		assert.Equal(t, "invalid_request", got["error"])
	}
	assert.Empty(t, svc.quoteID, "the service was not called")
}

func TestFormalizeReportsTheFieldsItCannotRead(t *testing.T) {
	svc := &fakeFormalizations{}
	cases := map[string]struct{ body, field, code string }{
		"date that is not RFC 3339": {`{"acceptance":{"method":"recorded_call","accepted_at":"ayer","reference":"r"}}`, "acceptance.accepted_at", "invalid_format"},
		"code with letters":         {`{"acceptance":{"method":"confirmation_code","accepted_at":"2026-10-25T14:59:00Z"},"confirmation_code":"12ab"}`, "confirmation_code", "invalid_format"},
		"no code":                   {`{"acceptance":{"method":"confirmation_code","accepted_at":"2026-10-25T14:59:00Z"}}`, "confirmation_code", "invalid_format"},
	}
	for name, c := range cases {
		rec, got := callQuotes(formalizationRouter(svc), http.MethodPost, "/v1/assisted-quotes/q1/formalization", "good", c.body)
		require.Equal(t, http.StatusUnprocessableEntity, rec.Code, name)
		assert.Equal(t, "validation_failed", got["error"], name)
		assert.Contains(t, got["fields"], map[string]any{"field": c.field, "code": c.code}, name)
	}
	assert.Empty(t, svc.quoteID, "the service was not called")
}

func TestAnInvalidAcceptanceIsAValidationFailure(t *testing.T) {
	svc := &fakeFormalizations{err: &domain.ValidationError{Fields: []domain.FieldError{{Field: "acceptance.reference", Code: "required"}}}}

	rec, got := callQuotes(formalizationRouter(svc), http.MethodPost, "/v1/assisted-quotes/q1/formalization", "good",
		`{"acceptance":{"method":"recorded_call","accepted_at":"2026-10-25T14:59:00Z"}}`)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "validation_failed", got["error"])
	assert.Contains(t, got["fields"], map[string]any{"field": "acceptance.reference", "code": "required"})
}

func TestFormalizationErrorsMapToTheirStatus(t *testing.T) {
	exp := time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC)
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{ports.ErrQuoteNotFound, http.StatusNotFound, "quote_not_found"},
		{&formalization.QuoteExpiredError{QuoteID: "q1", ExpiresAt: exp}, http.StatusUnprocessableEntity, "quote_expired"},
		{&issuance.OfferExpiredError{OfferID: "o", ExpiresAt: exp}, http.StatusUnprocessableEntity, "quote_expired"},
		{formalization.ErrAlreadyFormalized, http.StatusConflict, "already_formalized"},
		{formalization.ErrConsentCodeRequired, http.StatusUnprocessableEntity, "consent_code_required"},
		{formalization.ErrConsentCodeInvalid, http.StatusUnprocessableEntity, "invalid_confirmation_code"},
		{formalization.ErrConsentCodeExpired, http.StatusUnprocessableEntity, "confirmation_code_expired"},
		{formalization.ErrConsentCodeLocked, http.StatusTooManyRequests, "too_many_attempts"},
		{formalization.ErrConsentCodeLimit, http.StatusTooManyRequests, "too_many_codes"},
		{formalization.ErrCodeDeliveryUnavailable, http.StatusServiceUnavailable, "code_delivery_unavailable"},
		{context.DeadlineExceeded, http.StatusServiceUnavailable, "formalization_unavailable"},
	}
	for _, c := range cases {
		svc := &fakeFormalizations{err: c.err}
		rec, body := callQuotes(formalizationRouter(svc), http.MethodPost, "/v1/assisted-quotes/q1/formalization", "good", codeBody)
		require.Equal(t, c.status, rec.Code, c.code)
		assert.Equal(t, c.code, body["error"])
	}
}

func TestAnExpiredQuoteOffersToGenerateANewOne(t *testing.T) {
	exp := time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC)
	svc := &fakeFormalizations{err: &formalization.QuoteExpiredError{QuoteID: "q1", ExpiresAt: exp}}

	_, body := callQuotes(formalizationRouter(svc), http.MethodPost, "/v1/assisted-quotes/q1/consent", "good", "")

	assert.Equal(t, true, body["suggest_new_quote"], "CA-2")
	assert.Equal(t, "2026-10-26T15:00:00Z", body["expires_at"])
}

func TestOnlyAnAdvisorUsesTheFormalizationRoutes(t *testing.T) {
	svc := &fakeFormalizations{result: sampleResult(true)}
	for _, route := range []struct{ method, path, body string }{
		{http.MethodPost, "/v1/assisted-quotes/q1/consent", ""},
		{http.MethodPost, "/v1/assisted-quotes/q1/formalization", codeBody},
		{http.MethodGet, "/v1/assisted-quotes/q1/formalization", ""},
	} {
		rec, _ := callQuotes(formalizationRouter(svc), route.method, route.path, "", route.body)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, route.path)
		rec, _ = callQuotes(formalizationRouter(svc, security.RoleInsured), route.method, route.path, "good", route.body)
		assert.Equal(t, http.StatusForbidden, rec.Code, route.path)
	}
	assert.Empty(t, svc.quoteID)
}
