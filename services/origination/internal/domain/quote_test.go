package domain_test

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

var quoteNow = time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)

func ptr[T any](v T) *T { return &v }

func validRequest() domain.QuoteRequest {
	return domain.QuoteRequest{
		DocumentID: "1032456789", FullName: "Ana María  Pérez", Email: "ana@example.com",
		BirthDate: "1990-05-20", InsuredAmount: 250_000_000, TermMonths: 180, ConsentActive: ptr(true),
	}
}

func fieldCodes(t *testing.T, err error) map[string]string {
	t.Helper()
	var v *domain.ValidationError
	require.True(t, errors.As(err, &v), "want a *ValidationError, got %v", err)
	out := map[string]string{}
	for _, f := range v.Fields {
		out[f.Field] = f.Code
	}
	return out
}

func TestValidateAcceptsAValidRequestAndNormalizesIt(t *testing.T) {
	c, err := validRequest().Validate(quoteNow)

	require.NoError(t, err)
	assert.Equal(t, "1032456789", c.DocumentID)
	assert.Equal(t, "Ana María Pérez", c.FullName, "inner spaces are collapsed")
	assert.Equal(t, time.Date(1990, 5, 20, 0, 0, 0, 0, time.UTC), c.BirthDate)
	assert.Equal(t, domain.Applicant{FullName: "Ana María Pérez", DocumentID: "1032456789", Email: "ana@example.com"}, c.Applicant())
}

func TestValidateReportsEveryMissingField(t *testing.T) {
	_, err := domain.QuoteRequest{}.Validate(quoteNow)

	assert.Equal(t, map[string]string{
		"document_id": domain.CodeRequired, "full_name": domain.CodeRequired, "email": domain.CodeRequired,
		"birth_date": domain.CodeRequired, "insured_amount": domain.CodeRequired, "term_months": domain.CodeRequired,
		"consent_active": domain.CodeRequired,
	}, fieldCodes(t, err))
}

func TestValidateReportsTheSpecificErrorOfEachField(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*domain.QuoteRequest)
		field  string
		code   string
	}{
		{"document too short", func(r *domain.QuoteRequest) { r.DocumentID = "123" }, "document_id", domain.CodeInvalidFormat},
		{"document with symbols", func(r *domain.QuoteRequest) { r.DocumentID = "12.345-67" }, "document_id", domain.CodeInvalidFormat},
		{"name too short", func(r *domain.QuoteRequest) { r.FullName = "Al" }, "full_name", domain.CodeInvalidFormat},
		{"name without letters", func(r *domain.QuoteRequest) { r.FullName = "12345" }, "full_name", domain.CodeInvalidFormat},
		{"name too long", func(r *domain.QuoteRequest) { r.FullName = string(make([]rune, 121)) + "a" }, "full_name", domain.CodeInvalidFormat},
		{"email without domain", func(r *domain.QuoteRequest) { r.Email = "ana@" }, "email", domain.CodeInvalidFormat},
		{"email without dot in the domain", func(r *domain.QuoteRequest) { r.Email = "ana@host" }, "email", domain.CodeInvalidFormat},
		{"email with display name", func(r *domain.QuoteRequest) { r.Email = "Ana <ana@example.com>" }, "email", domain.CodeInvalidFormat},
		{"birth date not a date", func(r *domain.QuoteRequest) { r.BirthDate = "20/05/1990" }, "birth_date", domain.CodeInvalidFormat},
		{"birth date in the future", func(r *domain.QuoteRequest) { r.BirthDate = "2030-01-01" }, "birth_date", domain.CodeOutOfRange},
		{"minor", func(r *domain.QuoteRequest) { r.BirthDate = "2010-01-01" }, "birth_date", domain.CodeOutOfRange},
		{"too old", func(r *domain.QuoteRequest) { r.BirthDate = "1940-01-01" }, "birth_date", domain.CodeOutOfRange},
		{"amount below the minimum", func(r *domain.QuoteRequest) { r.InsuredAmount = 1_000 }, "insured_amount", domain.CodeOutOfRange},
		{"amount above the maximum", func(r *domain.QuoteRequest) { r.InsuredAmount = 5_000_000_000 }, "insured_amount", domain.CodeOutOfRange},
		{"negative amount", func(r *domain.QuoteRequest) { r.InsuredAmount = -5 }, "insured_amount", domain.CodeOutOfRange},
		{"term below the minimum", func(r *domain.QuoteRequest) { r.TermMonths = 6 }, "term_months", domain.CodeOutOfRange},
		{"term above the maximum", func(r *domain.QuoteRequest) { r.TermMonths = 400 }, "term_months", domain.CodeOutOfRange},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := validRequest()
			tc.mutate(&req)

			_, err := req.Validate(quoteNow)

			assert.Equal(t, map[string]string{tc.field: tc.code}, fieldCodes(t, err), "only the mutated field is invalid")
		})
	}
}

func TestValidationErrorListsTheFields(t *testing.T) {
	_, err := domain.QuoteRequest{DocumentID: "1"}.Validate(quoteNow)

	assert.Contains(t, err.Error(), "document_id: invalid_format")
}

func TestAgeBoundaryIsInclusive(t *testing.T) {
	req := validRequest()
	req.BirthDate = "2008-10-16" // turns 18 exactly today
	_, err := req.Validate(quoteNow)
	assert.NoError(t, err)

	req.BirthDate = "2008-10-17" // turns 18 tomorrow
	_, err = req.Validate(quoteNow)
	assert.Equal(t, map[string]string{"birth_date": domain.CodeOutOfRange}, fieldCodes(t, err))
}

func TestAgeAt(t *testing.T) {
	birth := time.Date(1990, 5, 20, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, 36, domain.AgeAt(birth, time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)))
	assert.Equal(t, 35, domain.AgeAt(birth, time.Date(2026, 5, 19, 0, 0, 0, 0, time.UTC)))
	assert.Equal(t, 35, domain.AgeAt(birth, time.Date(2026, 4, 30, 0, 0, 0, 0, time.UTC)))
}

func TestQuoteIsValidFor24HoursThenExpires(t *testing.T) {
	q := domain.Quote{CreatedAt: quoteNow, ExpiresAt: quoteNow.Add(domain.QuoteValidity)}

	assert.Equal(t, 24*time.Hour, domain.QuoteValidity)
	assert.Equal(t, domain.QuoteValid, q.Status(quoteNow.Add(24*time.Hour-time.Second)))
	assert.True(t, q.CanFormalize(quoteNow))
	assert.Equal(t, domain.QuoteExpired, q.Status(q.ExpiresAt), "the quote expires at the exact deadline")
	assert.False(t, q.CanFormalize(q.ExpiresAt.Add(time.Hour)))
}
