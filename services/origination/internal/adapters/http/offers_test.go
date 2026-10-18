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

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

var offerNow = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func sampleOffer() domain.PersonalizedOffer {
	return domain.PersonalizedOffer{
		ID: "off-1", CustomerID: "cust-1", OwnerID: "sub-a", Channel: domain.OfferChannelEmbedded, ConsentID: "cns-1",
		PartnerID: "partner-1", ApplicationID: "app-1",
		Applicant: domain.Applicant{FullName: "Ana Pérez", DocumentID: "1020304050", Email: "ana@example.com"},
		Terms: domain.CoverageTerms{InsuredAmount: 250_000_000, TermMonths: 120, Coverages: []domain.Coverage{
			{Code: domain.CoverageDeath, InsuredAmount: 250_000_000},
		}},
		Premium: 137_600, Currency: "COP", Conditions: []string{"validity_24h"},
		Mode: domain.ModeNormal, ProfileSource: domain.SourceExternal,
		Profile:      domain.ProfileSnapshot{Score: 0.42, Category: "standard"},
		Rules:        []domain.AppliedRule{{Code: "age_factor", Value: 1.5}},
		RaterVersion: "v1", CreatedAt: offerNow, ExpiresAt: offerNow.Add(24 * time.Hour),
	}
}

type fakeOffers struct {
	offer   domain.PersonalizedOffer
	view    orchestrator.OfferView
	created bool
	err     error
	req     domain.EmbeddedOfferRequest
}

func (f *fakeOffers) Generate(_ context.Context, req domain.EmbeddedOfferRequest) (domain.PersonalizedOffer, error) {
	f.req = req
	return f.offer, f.err
}
func (f *fakeOffers) Get(context.Context, string) (orchestrator.OfferView, error) {
	return f.view, f.err
}
func (f *fakeOffers) Renew(context.Context, string) (domain.PersonalizedOffer, bool, error) {
	return f.offer, f.created, f.err
}
func (f *fakeOffers) Now() time.Time { return offerNow }

type offerVerifier struct{ roles []security.Role }

func (v offerVerifier) Verify(_ context.Context, raw string) (security.Claims, error) {
	if raw != "good" {
		return security.Claims{}, errors.New("bad token")
	}
	return security.Claims{Subject: "sub-a", Roles: v.roles}, nil
}

func offerRouter(svc *fakeOffers, roles ...security.Role) http.Handler {
	return httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithEmbeddedOffers(svc), httpadapter.WithOffers(svc, offerVerifier{roles: roles}))
}

func callOffers(h http.Handler, method, path, token, body string) (*httptest.ResponseRecorder, map[string]any) {
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

const generateBody = `{"customer_id":"cust-1","consent_id":"cns-1","partner_id":"partner-1","application_id":"app-1",
 "document_id":"1020304050","full_name":"Ana Pérez","email":"ana@example.com","birth_date":"1985-03-14",
 "insured_amount":250000000,"term_months":120}`

func TestGenerateOfferReturns201WithTheOfferAndItsLocation(t *testing.T) {
	svc := &fakeOffers{offer: sampleOffer()}

	rec, body := callOffers(offerRouter(svc, security.RoleInsured), http.MethodPost, "/v1/offers", "", generateBody)

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "/v1/offers/off-1", rec.Header().Get("Location"))
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "off-1", body["offer_id"])
	assert.Equal(t, "active", body["status"])
	assert.Equal(t, "personalized", body["kind"])
	assert.Equal(t, "embedded", body["channel"])
	assert.Equal(t, 137600.0, body["premium"])
	assert.Equal(t, "normal", body["mode"])
	assert.Nil(t, body["provisional_reason"])
	assert.Equal(t, map[string]any{"category": "standard", "source": "external_sources"}, body["profile"])
	assert.Equal(t, map[string]any{"full_name": "Ana Pérez"}, body["applicant"])
	assert.Equal(t, map[string]any{"partner_id": "partner-1", "application_id": "app-1"}, body["credit"])
	assert.Equal(t, "v1", body["rater_version"])
	assert.Equal(t, true, body["can_accept"])
	assert.Equal(t, false, body["can_renew"])
	terms := body["terms"].(map[string]any)
	assert.EqualValues(t, 120, terms["term_months"])
	assert.Len(t, terms["coverages"], 1)
	assert.Equal(t, "cust-1", svc.req.CustomerID)
	assert.Equal(t, 250_000_000.0, svc.req.InsuredAmount)
}

func TestOfferResponsesNeverExposeTheScoreTheRulesOrTheDocument(t *testing.T) {
	svc := &fakeOffers{offer: sampleOffer()}
	rec, _ := callOffers(offerRouter(svc, security.RoleInsured), http.MethodPost, "/v1/offers", "", generateBody)
	raw := rec.Body.String()

	for _, secret := range []string{"score", "rules", "age_factor", "1020304050", "document", "0.42", "ana@example.com"} {
		assert.NotContains(t, raw, secret)
	}
}

func TestGenerateOfferProvisionalSaysWhy(t *testing.T) {
	o := sampleOffer()
	o.Mode, o.ProvisionalReason, o.ProfileSource = domain.ModeProvisional, domain.ProvisionalCachedProfile, domain.SourceCache
	o.ProfileDate, o.UnavailableSource = offerNow.Add(-48*time.Hour), "open_finance"

	_, body := callOffers(offerRouter(&fakeOffers{offer: o}, security.RoleInsured), http.MethodPost, "/v1/offers", "", generateBody)

	assert.Equal(t, "provisional", body["mode"])
	assert.Equal(t, "cached_profile", body["provisional_reason"])
	assert.Equal(t, "2026-10-16T12:00:00Z", body["profile_date"])
	assert.Equal(t, "open_finance", body["unavailable_source"])
}

func TestGenerateOfferErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{"validation", &domain.ValidationError{Fields: []domain.FieldError{{Field: "term_months", Code: "out_of_range"}}}, 400, "validation_failed"},
		{"no consent", orchestrator.ErrConsentRequired, 422, "consent_required"},
		{"another consent", orchestrator.ErrConsentMismatch, 422, "consent_mismatch"},
		{"audit down", fmt.Errorf("verify: %w", ports.ErrConsentUnverifiable), 503, "consent_unverifiable"},
		{"anything else", errors.New("boom"), 503, "offer_unavailable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, body := callOffers(offerRouter(&fakeOffers{err: tc.err}, security.RoleInsured), http.MethodPost, "/v1/offers", "", generateBody)

			assert.Equal(t, tc.status, rec.Code)
			assert.Equal(t, tc.code, body["error"])
		})
	}
}

func TestGenerateOfferValidationNamesTheFields(t *testing.T) {
	err := &domain.ValidationError{Fields: []domain.FieldError{{Field: "term_months", Code: "out_of_range"}, {Field: "email", Code: "required"}}}

	_, body := callOffers(offerRouter(&fakeOffers{err: err}, security.RoleInsured), http.MethodPost, "/v1/offers", "", generateBody)

	assert.Equal(t, []any{
		map[string]any{"field": "term_months", "code": "out_of_range"},
		map[string]any{"field": "email", "code": "required"},
	}, body["fields"])
}

func TestGenerateOfferRejectsMalformedJSON(t *testing.T) {
	rec, body := callOffers(offerRouter(&fakeOffers{}, security.RoleInsured), http.MethodPost, "/v1/offers", "", "{nope")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", body["error"])
}

func TestGenerateOfferNeedsNoSession(t *testing.T) {
	rec, _ := callOffers(offerRouter(&fakeOffers{offer: sampleOffer()}, security.RoleInsured), http.MethodPost, "/v1/offers", "", generateBody)

	assert.Equal(t, http.StatusCreated, rec.Code, "the partner channel is authenticated by the API Gateway key")
}

func TestGetOfferReturnsThePersonalizedBody(t *testing.T) {
	o := sampleOffer()
	o.SupersededBy = "off-2"
	svc := &fakeOffers{view: orchestrator.OfferView{
		Kind: orchestrator.OfferKindPersonalized, Personalized: &o, Status: domain.OfferSuperseded,
	}}

	rec, body := callOffers(offerRouter(svc, security.RoleInsured), http.MethodGet, "/v1/offers/off-1", "good", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, "superseded", body["status"])
	assert.Equal(t, "off-2", body["superseded_by"])
	assert.Equal(t, false, body["can_accept"])
	assert.Nil(t, body["renewed_by"])
}

func TestGetOfferReducedViewHasNullDetails(t *testing.T) {
	svc := &fakeOffers{view: orchestrator.OfferView{
		Kind:    orchestrator.OfferKindAdjusted,
		Reduced: domain.Offer{ID: "adj-1", Premium: 120_000, Currency: "COP", Mode: domain.ModeNormal, ExpiresAt: offerNow.Add(time.Hour)},
		Status:  domain.OfferActive, CanAccept: true,
	}}

	rec, body := callOffers(offerRouter(svc, security.RoleInsured), http.MethodGet, "/v1/offers/adj-1", "good", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "adjusted", body["kind"])
	assert.Equal(t, 120000.0, body["premium"])
	assert.Equal(t, "active", body["status"])
	for _, f := range []string{"terms", "conditions", "profile", "applicant", "credit", "rater_version"} {
		assert.Contains(t, body, f)
		assert.Nil(t, body[f], f)
	}
}

func TestOfferRoutesErrors(t *testing.T) {
	cases := []struct {
		name         string
		method, path string
		err          error
		status       int
		code         string
	}{
		{"read unknown", "GET", "/v1/offers/off-1", ports.ErrOfferNotFound, 404, "offer_not_found"},
		{"read forbidden", "GET", "/v1/offers/off-1", security.ErrForbidden, 403, "forbidden"},
		{"read broken", "GET", "/v1/offers/off-1", errors.New("db"), 503, "offer_unavailable"},
		{"renew unknown", "POST", "/v1/offers/off-1/renewal", ports.ErrOfferNotFound, 404, "offer_not_found"},
		{"renew forbidden", "POST", "/v1/offers/off-1/renewal", security.ErrForbidden, 403, "forbidden"},
		{"renew without consent", "POST", "/v1/offers/off-1/renewal", orchestrator.ErrConsentRequired, 422, "consent_required"},
		{"renew audit down", "POST", "/v1/offers/off-1/renewal", ports.ErrConsentUnverifiable, 503, "consent_unverifiable"},
		{"renew not insurable", "POST", "/v1/offers/off-1/renewal",
			&domain.ValidationError{Fields: []domain.FieldError{{Field: "birth_date", Code: "out_of_range"}}}, 422, "validation_failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, body := callOffers(offerRouter(&fakeOffers{err: tc.err}, security.RoleInsured), tc.method, tc.path, "good", "")

			assert.Equal(t, tc.status, rec.Code)
			assert.Equal(t, tc.code, body["error"])
		})
	}
}

func TestRenewConflicts(t *testing.T) {
	still := &orchestrator.OfferStillValidError{OfferID: "off-1", ExpiresAt: offerNow}
	rec, body := callOffers(offerRouter(&fakeOffers{err: still}, security.RoleInsured), http.MethodPost, "/v1/offers/off-1/renewal", "good", "")
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "offer_still_valid", body["error"])
	assert.Equal(t, "2026-10-18T12:00:00Z", body["expires_at"])

	sup := &orchestrator.OfferSupersededError{OfferID: "off-1", SupersededBy: "off-2"}
	rec, body = callOffers(offerRouter(&fakeOffers{err: sup}, security.RoleInsured), http.MethodPost, "/v1/offers/off-1/renewal", "good", "")
	assert.Equal(t, http.StatusConflict, rec.Code)
	assert.Equal(t, "offer_superseded", body["error"])
	assert.Equal(t, "off-2", body["superseded_by"])
}

func TestRenewReturns201ForANewRenewalAnd200ForAnExistingOne(t *testing.T) {
	r := sampleOffer()
	r.ID, r.Renews = "off-2", "off-1"

	rec, body := callOffers(offerRouter(&fakeOffers{offer: r, created: true}, security.RoleInsured), http.MethodPost, "/v1/offers/off-1/renewal", "good", "")
	assert.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "off-2", body["offer_id"])
	assert.Equal(t, "/v1/offers/off-2", rec.Header().Get("Location"))

	rec, _ = callOffers(offerRouter(&fakeOffers{offer: r, created: false}, security.RoleInsured), http.MethodPost, "/v1/offers/off-1/renewal", "good", "")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestOfferRoutesAuthMatrix(t *testing.T) {
	svc := &fakeOffers{offer: sampleOffer(), view: orchestrator.OfferView{Personalized: &domain.PersonalizedOffer{}}}
	cases := []struct {
		name         string
		method, path string
		token        string
		roles        []security.Role
		want         int
	}{
		{"read without token", "GET", "/v1/offers/off-1", "", []security.Role{security.RoleInsured}, 401},
		{"read with a bad token", "GET", "/v1/offers/off-1", "bad", []security.Role{security.RoleInsured}, 401},
		{"read as insured", "GET", "/v1/offers/off-1", "good", []security.Role{security.RoleInsured}, 200},
		{"read as advisor", "GET", "/v1/offers/off-1", "good", []security.Role{security.RoleAdvisor}, 200},
		{"read as operations", "GET", "/v1/offers/off-1", "good", []security.Role{security.RoleOperations}, 200},
		{"read as partner", "GET", "/v1/offers/off-1", "good", []security.Role{security.RolePartner}, 403},
		{"renew without token", "POST", "/v1/offers/off-1/renewal", "", []security.Role{security.RoleInsured}, 401},
		{"renew as insured", "POST", "/v1/offers/off-1/renewal", "good", []security.Role{security.RoleInsured}, 200},
		{"renew as advisor", "POST", "/v1/offers/off-1/renewal", "good", []security.Role{security.RoleAdvisor}, 403},
		{"renew as operations", "POST", "/v1/offers/off-1/renewal", "good", []security.Role{security.RoleOperations}, 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, _ := callOffers(offerRouter(svc, tc.roles...), tc.method, tc.path, tc.token, "")

			assert.Equal(t, tc.want, rec.Code)
		})
	}
}

func TestOfferRoutesRejectAnOversizedId(t *testing.T) {
	rec, body := callOffers(offerRouter(&fakeOffers{}, security.RoleInsured), http.MethodGet, "/v1/offers/"+strings.Repeat("x", 129), "good", "")

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.Equal(t, "invalid_request", body["error"])
}

func TestOfferRoutesAreNotMountedWithoutTheirOptions(t *testing.T) {
	h := httpadapter.NewRouter(&fakeResolver{})

	rec, _ := callOffers(h, http.MethodPost, "/v1/offers", "", generateBody)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	rec, _ = callOffers(h, http.MethodGet, "/v1/offers/off-1", "good", "")
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestOfferRoutesCoexistWithTheAcceptanceRoutes(t *testing.T) {
	h := httpadapter.NewRouter(&fakeResolver{},
		httpadapter.WithEmbeddedOffers(&fakeOffers{offer: sampleOffer()}),
		httpadapter.WithOffers(&fakeOffers{view: orchestrator.OfferView{Personalized: &domain.PersonalizedOffer{}}}, offerVerifier{roles: []security.Role{security.RoleInsured}}),
		httpadapter.WithAcceptances(&fakeAcceptances{}, tokenVerifier{}))

	rec, _ := callOffers(h, http.MethodGet, "/v1/offers/off-1", "good", "")
	assert.Equal(t, http.StatusOK, rec.Code)
	rec, _ = callOffers(h, http.MethodGet, "/v1/offers/off-1/acceptance", "good", "")
	assert.NotEqual(t, http.StatusNotFound, rec.Code)
}

func TestOfferRoutesRejectAnIdThatCannotNameAnOffer(t *testing.T) {
	for _, id := range []string{"%00", "off%2F1", "off%201", "off%3B1"} {
		for _, route := range []struct{ method, suffix string }{{"GET", ""}, {"POST", "/renewal"}} {
			rec, body := callOffers(offerRouter(&fakeOffers{}, security.RoleInsured), route.method, "/v1/offers/"+id+route.suffix, "good", "")

			assert.Equal(t, http.StatusBadRequest, rec.Code, route.method+" "+id)
			assert.Equal(t, "invalid_request", body["error"])
		}
	}
}
