package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

func validRequest() domain.GrantRequest {
	return domain.GrantRequest{
		CustomerID:         "cust-1",
		DocumentID:         "1020304050",
		FullName:           "Ana María Pérez",
		TermsVersion:       "v1",
		AcceptedSources:    []string{"open_finance", "open_data", "kyc"},
		AcceptedDataPolicy: true,
		Channel:            "embedded",
		PartnerID:          "partner-1",
		ApplicationID:      "app-1",
		UserAgent:          "Mozilla/5.0",
	}
}

func fieldErrors(t *testing.T, err error) map[string]string {
	t.Helper()
	var ve *domain.ValidationError
	require.ErrorAs(t, err, &ve)
	out := map[string]string{}
	for _, f := range ve.Fields {
		out[f.Field] = f.Code
	}
	return out
}

func TestTermsV1ListsEverySourceOfTheE1Screen(t *testing.T) {
	terms := domain.TermsV1()

	assert.Equal(t, domain.TermsVersion("v1"), terms.Version)
	assert.Equal(t, "risk_profiling_mortgage_life_offer", terms.Purpose)
	assert.Equal(t, "Solventa S.A.", terms.Controller)
	assert.Equal(t, "personal-data-policy-v1", terms.PolicyRef)
	assert.Equal(t, []string{"open_finance", "open_data", "kyc"}, terms.SourceCodes())
	assert.Equal(t, []string{"credit_products", "balances", "payment_behavior"}, terms.Sources[0].DataCategories)
	assert.Equal(t, domain.Validity12Months, terms.Sources[0].Validity)
	assert.Equal(t, domain.ValiditySingleUse, terms.Sources[2].Validity)
}

func TestTermsV1ReturnsACopyEveryTime(t *testing.T) {
	first := domain.TermsV1()
	first.Sources[0].DataCategories[0] = "changed"
	first.Sources[0].Code = "changed"

	second := domain.TermsV1()

	assert.Equal(t, "open_finance", second.Sources[0].Code)
	assert.Equal(t, "credit_products", second.Sources[0].DataCategories[0])
}

func TestValidRequestPasses(t *testing.T) {
	assert.NoError(t, validRequest().Validate(domain.TermsV1()))
}

func TestValidateReportsEveryInvalidFieldAtOnce(t *testing.T) {
	req := domain.GrantRequest{}

	got := fieldErrors(t, req.Validate(domain.TermsV1()))

	assert.Equal(t, map[string]string{
		"customer_id":          "required",
		"partner_id":           "required",
		"application_id":       "required",
		"document_id":          "required",
		"full_name":            "required",
		"terms_version":        "required",
		"accepted_sources":     "required",
		"accepted_data_policy": "must_be_true",
		"channel":              "required",
	}, got)
}

func TestValidateFieldRules(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.GrantRequest)
		field  string
		code   string
	}{
		{"customer id with a space", func(r *domain.GrantRequest) { r.CustomerID = "cust 1" }, "customer_id", "invalid_format"},
		{"customer id with a slash", func(r *domain.GrantRequest) { r.CustomerID = "a/b" }, "customer_id", "invalid_format"},
		{"customer id 129 chars", func(r *domain.GrantRequest) { r.CustomerID = strings.Repeat("a", 129) }, "customer_id", "invalid_format"},
		{"partner id with a dot", func(r *domain.GrantRequest) { r.PartnerID = "p.1" }, "partner_id", "invalid_format"},
		{"application id too long", func(r *domain.GrantRequest) { r.ApplicationID = strings.Repeat("a", 129) }, "application_id", "invalid_format"},
		{"document with 4 chars", func(r *domain.GrantRequest) { r.DocumentID = "1234" }, "document_id", "invalid_format"},
		{"document with 21 chars", func(r *domain.GrantRequest) { r.DocumentID = strings.Repeat("1", 21) }, "document_id", "invalid_format"},
		{"document with symbols", func(r *domain.GrantRequest) { r.DocumentID = "123-456-789" }, "document_id", "invalid_format"},
		{"name with 2 chars", func(r *domain.GrantRequest) { r.FullName = "Al" }, "full_name", "invalid_format"},
		{"name with 121 chars", func(r *domain.GrantRequest) { r.FullName = strings.Repeat("a", 121) }, "full_name", "invalid_format"},
		{"name without letters", func(r *domain.GrantRequest) { r.FullName = "12345" }, "full_name", "invalid_format"},
		{"blank name", func(r *domain.GrantRequest) { r.FullName = "   " }, "full_name", "required"},
		{"only two sources", func(r *domain.GrantRequest) { r.AcceptedSources = []string{"open_finance", "open_data"} }, "accepted_sources", "incomplete"},
		{"unknown source", func(r *domain.GrantRequest) { r.AcceptedSources = []string{"open_finance", "open_data", "bureau"} }, "accepted_sources", "incomplete"},
		{"repeated source", func(r *domain.GrantRequest) { r.AcceptedSources = []string{"open_finance", "open_finance", "kyc"} }, "accepted_sources", "incomplete"},
		{"extra source", func(r *domain.GrantRequest) {
			r.AcceptedSources = []string{"open_finance", "open_data", "kyc", "kyc"}
		}, "accepted_sources", "incomplete"},
		{"policy not accepted", func(r *domain.GrantRequest) { r.AcceptedDataPolicy = false }, "accepted_data_policy", "must_be_true"},
		{"unsupported channel", func(r *domain.GrantRequest) { r.Channel = "mobile" }, "channel", "invalid_format"},
		{"user agent too long", func(r *domain.GrantRequest) { r.UserAgent = strings.Repeat("a", 513) }, "user_agent", "out_of_range"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := validRequest()
			tc.mutate(&req)

			got := fieldErrors(t, req.Validate(domain.TermsV1()))

			assert.Equal(t, map[string]string{tc.field: tc.code}, got)
		})
	}
}

func TestValidateBoundaryValuesPass(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*domain.GrantRequest)
	}{
		{"customer id 128 chars", func(r *domain.GrantRequest) { r.CustomerID = strings.Repeat("a", 128) }},
		{"customer id with _ and -", func(r *domain.GrantRequest) { r.CustomerID = "a_b-C9" }},
		{"document 5 chars", func(r *domain.GrantRequest) { r.DocumentID = "12345" }},
		{"document 20 chars", func(r *domain.GrantRequest) { r.DocumentID = strings.Repeat("9", 20) }},
		{"name 3 chars", func(r *domain.GrantRequest) { r.FullName = "Ana" }},
		{"name 120 chars", func(r *domain.GrantRequest) { r.FullName = strings.Repeat("a", 120) }},
		{"sources in another order", func(r *domain.GrantRequest) { r.AcceptedSources = []string{"kyc", "open_data", "open_finance"} }},
		{"user agent 512 chars", func(r *domain.GrantRequest) { r.UserAgent = strings.Repeat("a", 512) }},
		{"no user agent", func(r *domain.GrantRequest) { r.UserAgent = "" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := validRequest()
			tc.mutate(&req)

			assert.NoError(t, req.Validate(domain.TermsV1()))
		})
	}
}

func TestValidateOutdatedTermsIsNotAFieldError(t *testing.T) {
	req := validRequest()
	req.TermsVersion = "v0"

	err := req.Validate(domain.TermsV1())

	assert.True(t, errors.Is(err, domain.ErrTermsOutdated))
}

func TestValidateReportsFieldErrorsBeforeOutdatedTerms(t *testing.T) {
	req := validRequest()
	req.TermsVersion = "v0"
	req.CustomerID = ""

	err := req.Validate(domain.TermsV1())

	assert.False(t, errors.Is(err, domain.ErrTermsOutdated))
	assert.Equal(t, map[string]string{"customer_id": "required"}, fieldErrors(t, err))
}

func TestHolderIsNormalized(t *testing.T) {
	req := validRequest()
	req.DocumentID = " 1020304050 "
	req.FullName = "  Ana   María  Pérez "

	assert.Equal(t, domain.Holder{DocumentID: "1020304050", FullName: "Ana María Pérez"}, req.Holder())
}

func TestValidationErrorListsTheFields(t *testing.T) {
	err := &domain.ValidationError{Fields: []domain.FieldError{{Field: "a", Code: "required"}, {Field: "b", Code: "invalid_format"}}}

	assert.Equal(t, "invalid consent request (a: required, b: invalid_format)", err.Error())
}

func grantedConsent(at time.Time) domain.Consent {
	terms := domain.TermsV1()
	return domain.Consent{ID: "cns-1", CustomerID: "cust-1", Scope: terms.Sources, GrantedAt: at}
}

func TestConsentValidityBoundaries(t *testing.T) {
	granted := time.Date(2026, 1, 31, 10, 0, 0, 0, time.UTC)
	c := grantedConsent(granted)
	expiry := c.ExpiresAt()

	tests := []struct {
		name   string
		now    time.Time
		active bool
	}{
		{"the moment it is granted", granted, true},
		{"one nanosecond before 12 months", expiry.Add(-time.Nanosecond), true},
		{"exactly 12 months later", expiry, false},
		{"one nanosecond after 12 months", expiry.Add(time.Nanosecond), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.active, c.Active(tc.now))
			assert.Equal(t, tc.active, c.ActiveFor(domain.SourceOpenFinance, tc.now))
			assert.Equal(t, tc.active, c.ActiveFor(domain.SourceKYC, tc.now), "the single-use KYC lasts as long as the consent")
		})
	}
}

func TestExpiresAtIsTwelveCalendarMonthsAfter(t *testing.T) {
	c := grantedConsent(time.Date(2026, 3, 15, 8, 30, 0, 0, time.UTC))

	assert.Equal(t, time.Date(2027, 3, 15, 8, 30, 0, 0, time.UTC), c.ExpiresAt())
}

func TestActiveForAnUnknownSourceIsFalse(t *testing.T) {
	c := grantedConsent(time.Now())

	assert.False(t, c.ActiveFor("bureau", c.GrantedAt))
}

func TestActiveSourcesFollowsTheScope(t *testing.T) {
	c := grantedConsent(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))

	assert.Equal(t, []string{"open_finance", "open_data", "kyc"}, c.ActiveSources(c.GrantedAt))
	assert.Empty(t, c.ActiveSources(c.ExpiresAt()))
}
