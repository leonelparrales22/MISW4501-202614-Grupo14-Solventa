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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

var quoteClock = time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)

type fakeQuotes struct {
	quote    domain.Quote
	customer domain.Customer
	err      error
	got      domain.QuoteRequest
	advisor  string
	now      time.Time
}

func (f *fakeQuotes) Create(_ context.Context, advisor string, req domain.QuoteRequest) (domain.Quote, error) {
	f.got, f.advisor = req, advisor
	return f.quote, f.err
}
func (f *fakeQuotes) Get(context.Context, string) (domain.Quote, error) { return f.quote, f.err }
func (f *fakeQuotes) CustomerByDocument(context.Context, string) (domain.Customer, error) {
	return f.customer, f.err
}
func (f *fakeQuotes) Now() time.Time { return f.now }

type fixedVerifier struct{ roles []security.Role }

func (v fixedVerifier) Verify(_ context.Context, raw string) (security.Claims, error) {
	if raw != "good" {
		return security.Claims{}, errors.New("bad token")
	}
	return security.Claims{Subject: "adv-1", Roles: v.roles}, nil
}

func sampleQuote() domain.Quote {
	return domain.Quote{
		ID: "q1", CustomerID: "c1", Applicant: domain.Applicant{FullName: "Ana", DocumentID: "1032456789", Email: "a@x.co"},
		InsuredAmount: 250_000_000, TermMonths: 180, Premium: 98_700, Currency: "COP",
		Coverages:     []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 250_000_000}},
		RatingVersion: "v1", Mode: domain.ModeNormal, ProfileSource: domain.SourceExternal, ProfileScore: 0.4, ProfileCategory: "standard",
		ProfileDate: quoteClock.Add(-time.Hour), CreatedAt: quoteClock, ExpiresAt: quoteClock.Add(24 * time.Hour),
	}
}

func quoteRouter(svc httpadapter.AssistedQuoteService, roles ...security.Role) http.Handler {
	if len(roles) == 0 {
		roles = []security.Role{security.RoleAdvisor}
	}
	return httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithAssistedQuotes(svc, fixedVerifier{roles: roles}))
}

func callQuotes(h http.Handler, method, path, token, body string) (*httptest.ResponseRecorder, map[string]any) {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec, m
}

const validBody = `{"document_id":"1032456789","full_name":"Ana","email":"a@x.co","birth_date":"1990-05-20",` +
	`"insured_amount":250000000,"term_months":180,"consent_active":true}`

func TestCreateQuoteReturnsTheQuote(t *testing.T) {
	svc := &fakeQuotes{quote: sampleQuote(), now: quoteClock.Add(time.Hour)}

	rec, body := callQuotes(quoteRouter(svc), http.MethodPost, "/v1/assisted-quotes", "good", validBody)

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "/v1/assisted-quotes/q1", rec.Header().Get("Location"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "q1", body["quote_id"])
	assert.EqualValues(t, 98_700, body["premium"])
	assert.Equal(t, "valid", body["status"])
	assert.Equal(t, true, body["can_formalize"])
	assert.Nil(t, body["blocked_reason"])
	assert.Nil(t, body["notice"])
	assert.Equal(t, "normal", body["mode"])
	assert.Equal(t, "adv-1", svc.advisor, "the quote is attributed to the authenticated advisor")
	assert.Equal(t, "1032456789", svc.got.DocumentID)
	assert.Equal(t, 180, svc.got.TermMonths)
	require.NotNil(t, svc.got.ConsentActive)
	assert.True(t, *svc.got.ConsentActive)
}

func TestCreateQuoteInDegradedModeCarriesTheNotice(t *testing.T) {
	for source, notice := range map[domain.ProfileSource]string{
		domain.SourceCache:         httpadapter.NoticeLastProfileInUse,
		domain.SourceBaseActuarial: httpadapter.NoticePersonalizationUnavailable,
	} {
		q := sampleQuote()
		q.Mode, q.ProfileSource, q.UnavailableSource = domain.ModeProvisional, source, "open_finance"
		svc := &fakeQuotes{quote: q, now: quoteClock}

		_, body := callQuotes(quoteRouter(svc), http.MethodPost, "/v1/assisted-quotes", "good", validBody)

		assert.Equal(t, "provisional", body["mode"])
		assert.Equal(t, notice, body["notice"])
		assert.Equal(t, "open_finance", body["unavailable_source"])
		assert.Equal(t, string(source), body["source"])
	}
}

func TestCreateQuoteReportsEveryInvalidField(t *testing.T) {
	svc := &fakeQuotes{err: &domain.ValidationError{Fields: []domain.FieldError{
		{Field: "email", Code: "invalid_format"}, {Field: "birth_date", Code: "required"},
	}}}

	rec, body := callQuotes(quoteRouter(svc), http.MethodPost, "/v1/assisted-quotes", "good", `{}`)

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "validation_failed", body["error"])
	assert.Equal(t, []any{
		map[string]any{"field": "email", "code": "invalid_format"},
		map[string]any{"field": "birth_date", "code": "required"},
	}, body["fields"])
}

func TestCreateQuoteErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		body   string
		status int
		code   string
	}{
		{"malformed body", nil, `{`, http.StatusBadRequest, "invalid_request"},
		{"consent", orchestrator.ErrConsentRequired, validBody, http.StatusUnprocessableEntity, "consent_required"},
		{"infrastructure", errors.New("db down"), validBody, http.StatusServiceUnavailable, "quote_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, body := callQuotes(quoteRouter(&fakeQuotes{err: tc.err}), http.MethodPost, "/v1/assisted-quotes", "good", tc.body)
			assert.Equal(t, tc.status, rec.Code)
			assert.Equal(t, tc.code, body["error"])
		})
	}
}

func TestGetQuoteShowsAnExpiredQuoteAsNotFormalizable(t *testing.T) {
	svc := &fakeQuotes{quote: sampleQuote(), now: quoteClock.Add(25 * time.Hour)}

	rec, body := callQuotes(quoteRouter(svc), http.MethodGet, "/v1/assisted-quotes/q1", "good", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "expired", body["status"])
	assert.Equal(t, false, body["can_formalize"])
	assert.Equal(t, "quote_expired", body["blocked_reason"])
}

func TestGetQuoteErrors(t *testing.T) {
	rec, body := callQuotes(quoteRouter(&fakeQuotes{err: ports.ErrQuoteNotFound}), http.MethodGet, "/v1/assisted-quotes/nope", "good", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "quote_not_found", body["error"])

	rec, _ = callQuotes(quoteRouter(&fakeQuotes{err: errors.New("db down")}), http.MethodGet, "/v1/assisted-quotes/q1", "good", "")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)

	rec, body = callQuotes(quoteRouter(&fakeQuotes{}), http.MethodGet, "/v1/assisted-quotes/"+strings.Repeat("a", 129), "good", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", body["error"])
}

func TestGetCustomerByDocument(t *testing.T) {
	svc := &fakeQuotes{customer: domain.Customer{
		ID: "c1", DocumentID: "1032456789", FullName: "Ana", Email: "a@x.co", BirthDate: time.Date(1990, 5, 20, 0, 0, 0, 0, time.UTC),
	}}

	rec, body := callQuotes(quoteRouter(svc), http.MethodGet, "/v1/customers?document_id=1032456789", "good", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "c1", body["customer_id"])
	assert.Equal(t, "1990-05-20", body["birth_date"])
	assert.Equal(t, "Ana", body["full_name"])
}

func TestGetCustomerByDocumentErrors(t *testing.T) {
	rec, body := callQuotes(quoteRouter(&fakeQuotes{}), http.MethodGet, "/v1/customers", "good", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", body["error"])

	rec, body = callQuotes(quoteRouter(&fakeQuotes{err: ports.ErrCustomerNotFound}), http.MethodGet, "/v1/customers?document_id=1032456789", "good", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "customer_not_found", body["error"])

	invalid := &domain.ValidationError{Fields: []domain.FieldError{{Field: "document_id", Code: "invalid_format"}}}
	rec, body = callQuotes(quoteRouter(&fakeQuotes{err: invalid}), http.MethodGet, "/v1/customers?document_id=x", "good", "")
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "validation_failed", body["error"])

	rec, _ = callQuotes(quoteRouter(&fakeQuotes{err: errors.New("db down")}), http.MethodGet, "/v1/customers?document_id=1032456789", "good", "")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestQuoteRoutesRequireAnAdvisor(t *testing.T) {
	routes := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/assisted-quotes", validBody},
		{http.MethodGet, "/v1/assisted-quotes/q1", ""},
		{http.MethodGet, "/v1/customers?document_id=1032456789", ""},
	}
	for _, r := range routes {
		svc := &fakeQuotes{quote: sampleQuote()}
		rec, _ := callQuotes(quoteRouter(svc), r.method, r.path, "", r.body)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s without a token", r.method, r.path)
		rec, _ = callQuotes(quoteRouter(svc), r.method, r.path, "bad", r.body)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, "%s %s with a bad token", r.method, r.path)
		for _, role := range []security.Role{security.RoleInsured, security.RoleOperations, security.RolePartner} {
			rec, _ = callQuotes(quoteRouter(svc, role), r.method, r.path, "good", r.body)
			assert.Equal(t, http.StatusForbidden, rec.Code, "%s %s as %s", r.method, r.path, role)
		}
	}
}
