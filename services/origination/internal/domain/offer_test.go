package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

var offerNow = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)

func validOfferRequest() domain.EmbeddedOfferRequest {
	return domain.EmbeddedOfferRequest{
		CustomerID: "cust-1", ConsentID: "cns-1", PartnerID: "partner-1", ApplicationID: "app-1",
		DocumentID: "1020304050", FullName: "  Ana   María Pérez ", Email: "ana@example.com",
		BirthDate: "1985-03-14", InsuredAmount: 250_000_000, TermMonths: 120,
	}
}

func fieldsOf(t *testing.T, err error) map[string]string {
	t.Helper()
	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	out := map[string]string{}
	for _, f := range invalid.Fields {
		out[f.Field] = f.Code
	}
	return out
}

func TestEmbeddedOfferRequestValidReturnsTheNormalizedHolder(t *testing.T) {
	holder, birth, err := validOfferRequest().Validate(offerNow)

	require.NoError(t, err)
	assert.Equal(t, domain.Applicant{FullName: "Ana María Pérez", DocumentID: "1020304050", Email: "ana@example.com"}, holder)
	assert.Equal(t, time.Date(1985, 3, 14, 0, 0, 0, 0, time.UTC), birth)
}

func TestEmbeddedOfferRequestNamesEveryInvalidField(t *testing.T) {
	_, _, err := domain.EmbeddedOfferRequest{}.Validate(offerNow)

	assert.Equal(t, map[string]string{
		"customer_id": "required", "consent_id": "required", "partner_id": "required", "application_id": "required",
		"document_id": "required", "full_name": "required", "email": "required", "birth_date": "required",
		"insured_amount": "required", "term_months": "required",
	}, fieldsOf(t, err))
}

func TestEmbeddedOfferRequestBoundaries(t *testing.T) {
	long := func(n int) string {
		b := make([]byte, n)
		for i := range b {
			b[i] = 'a'
		}
		return string(b)
	}
	cases := []struct {
		name   string
		mutate func(*domain.EmbeddedOfferRequest)
		field  string
		code   string // empty: valid
	}{
		{"customer_id at 128 characters", func(r *domain.EmbeddedOfferRequest) { r.CustomerID = long(128) }, "customer_id", ""},
		{"customer_id at 129 characters", func(r *domain.EmbeddedOfferRequest) { r.CustomerID = long(129) }, "customer_id", "invalid_format"},
		{"customer_id with a space", func(r *domain.EmbeddedOfferRequest) { r.CustomerID = "cust 1" }, "customer_id", "invalid_format"},
		{"consent_id with a slash", func(r *domain.EmbeddedOfferRequest) { r.ConsentID = "cns/1" }, "consent_id", "invalid_format"},
		{"partner_id at 129 characters", func(r *domain.EmbeddedOfferRequest) { r.PartnerID = long(129) }, "partner_id", "invalid_format"},
		{"document_id of 4", func(r *domain.EmbeddedOfferRequest) { r.DocumentID = "1234" }, "document_id", "invalid_format"},
		{"document_id of 5", func(r *domain.EmbeddedOfferRequest) { r.DocumentID = "12345" }, "document_id", ""},
		{"full_name of 2", func(r *domain.EmbeddedOfferRequest) { r.FullName = "Al" }, "full_name", "invalid_format"},
		{"full_name of 3", func(r *domain.EmbeddedOfferRequest) { r.FullName = "Ali" }, "full_name", ""},
		{"full_name of 120", func(r *domain.EmbeddedOfferRequest) { r.FullName = long(120) }, "full_name", ""},
		{"full_name of 121", func(r *domain.EmbeddedOfferRequest) { r.FullName = long(121) }, "full_name", "invalid_format"},
		{"full_name without letters", func(r *domain.EmbeddedOfferRequest) { r.FullName = "123 456" }, "full_name", "invalid_format"},
		{"email with a display name", func(r *domain.EmbeddedOfferRequest) { r.Email = "Ana <ana@example.com>" }, "email", "invalid_format"},
		{"email without a dot in the domain", func(r *domain.EmbeddedOfferRequest) { r.Email = "ana@example" }, "email", "invalid_format"},
		{"birth_date not a date", func(r *domain.EmbeddedOfferRequest) { r.BirthDate = "14/03/1985" }, "birth_date", "invalid_format"},
		{"age 18 today", func(r *domain.EmbeddedOfferRequest) { r.BirthDate = "2008-10-18" }, "birth_date", ""},
		{"age 17 until tomorrow", func(r *domain.EmbeddedOfferRequest) { r.BirthDate = "2008-10-19" }, "birth_date", "out_of_range"},
		{"age 70 today", func(r *domain.EmbeddedOfferRequest) { r.BirthDate = "1956-10-18" }, "birth_date", ""},
		{"age 70 until the day before the 71st birthday", func(r *domain.EmbeddedOfferRequest) { r.BirthDate = "1955-10-19" }, "birth_date", ""},
		{"age 71 today", func(r *domain.EmbeddedOfferRequest) { r.BirthDate = "1955-10-18" }, "birth_date", "out_of_range"},
		{"born in the future", func(r *domain.EmbeddedOfferRequest) { r.BirthDate = "2030-01-01" }, "birth_date", "out_of_range"},
		{"minimum insured amount", func(r *domain.EmbeddedOfferRequest) { r.InsuredAmount = domain.MinInsuredAmount }, "insured_amount", ""},
		{"below the minimum amount", func(r *domain.EmbeddedOfferRequest) { r.InsuredAmount = domain.MinInsuredAmount - 1 }, "insured_amount", "out_of_range"},
		{"maximum insured amount", func(r *domain.EmbeddedOfferRequest) { r.InsuredAmount = domain.MaxInsuredAmount }, "insured_amount", ""},
		{"above the maximum amount", func(r *domain.EmbeddedOfferRequest) { r.InsuredAmount = domain.MaxInsuredAmount + 1 }, "insured_amount", "out_of_range"},
		{"minimum term", func(r *domain.EmbeddedOfferRequest) { r.TermMonths = domain.MinTermMonths }, "term_months", ""},
		{"below the minimum term", func(r *domain.EmbeddedOfferRequest) { r.TermMonths = domain.MinTermMonths - 1 }, "term_months", "out_of_range"},
		{"maximum term", func(r *domain.EmbeddedOfferRequest) { r.TermMonths = domain.MaxTermMonths }, "term_months", ""},
		{"above the maximum term", func(r *domain.EmbeddedOfferRequest) { r.TermMonths = domain.MaxTermMonths + 1 }, "term_months", "out_of_range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := validOfferRequest()
			tc.mutate(&req)

			_, _, err := req.Validate(offerNow)

			if tc.code == "" {
				assert.NoError(t, err)
				return
			}
			assert.Equal(t, map[string]string{tc.field: tc.code}, fieldsOf(t, err))
		})
	}
}

func TestValidateBirthDateAtTheAgeLimitsOfARenewal(t *testing.T) {
	assert.NoError(t, domain.ValidateBirthDate(time.Date(1956, 10, 18, 0, 0, 0, 0, time.UTC), offerNow), "70 today")
	assert.Equal(t, map[string]string{"birth_date": "out_of_range"},
		fieldsOf(t, domain.ValidateBirthDate(time.Date(1955, 10, 18, 0, 0, 0, 0, time.UTC), offerNow)), "71 today")
	assert.Equal(t, map[string]string{"birth_date": "out_of_range"},
		fieldsOf(t, domain.ValidateBirthDate(time.Date(2008, 10, 19, 0, 0, 0, 0, time.UTC), offerNow)), "17")
}

func offerExpiringAt(expires time.Time) domain.PersonalizedOffer {
	return domain.PersonalizedOffer{
		ID: "off-1", OwnerID: "sub-1", Applicant: domain.Applicant{FullName: "Ana", DocumentID: "1020304050", Email: "ana@example.com"},
		Premium: 98_400, Currency: "COP", Mode: domain.ModeProvisional,
		Profile: domain.ProfileSnapshot{Score: 0.5}, ExpiresAt: expires,
	}
}

func TestOfferStatusAroundTheExpiry(t *testing.T) {
	o := offerExpiringAt(offerNow)

	assert.Equal(t, domain.OfferActive, o.Status(offerNow.Add(-time.Nanosecond)), "1 ns before")
	assert.Equal(t, domain.OfferExpired, o.Status(offerNow), "at the expiry")
	assert.Equal(t, domain.OfferExpired, o.Status(offerNow.Add(time.Nanosecond)), "1 ns after")
	assert.True(t, o.CanAccept(offerNow.Add(-time.Nanosecond)))
	assert.False(t, o.CanAccept(offerNow))
}

func TestASupersededOfferIsSupersededEvenBeforeItsExpiry(t *testing.T) {
	o := offerExpiringAt(offerNow.Add(time.Hour))
	o.SupersededBy, o.SupersededAt = "off-2", offerNow

	assert.Equal(t, domain.OfferSuperseded, o.Status(offerNow))
	assert.False(t, o.CanAccept(offerNow))
	assert.False(t, o.CanRenew(offerNow), "a superseded offer is not renewed: the newer one is used")
}

func TestOnlyAnExpiredAndNotRenewedOfferCanBeRenewed(t *testing.T) {
	o := offerExpiringAt(offerNow)

	assert.False(t, o.CanRenew(offerNow.Add(-time.Nanosecond)), "still valid")
	assert.True(t, o.CanRenew(offerNow))

	o.RenewedBy = "off-3"
	assert.False(t, o.CanRenew(offerNow), "already renewed: the renewal is used")
	assert.Equal(t, domain.OfferExpired, o.Status(offerNow), "a renewed offer stays expired")
}

func TestAsOfferCarriesWhatTheAcceptanceNeeds(t *testing.T) {
	o := offerExpiringAt(offerNow)

	got := o.AsOffer()

	assert.Equal(t, domain.Offer{
		ID: "off-1", CustomerID: "sub-1", Applicant: o.Applicant, Premium: 98_400, Currency: "COP",
		Mode: domain.ModeProvisional, ProfileScore: 0.5, ExpiresAt: offerNow,
	}, got)
	assert.False(t, got.IsExpired(offerNow.Add(-time.Nanosecond)))
	assert.True(t, got.IsExpired(offerNow))
}

func TestAsOfferOfAnUnclaimedOfferHasNoOwner(t *testing.T) {
	o := offerExpiringAt(offerNow)
	o.OwnerID = ""

	assert.Empty(t, o.AsOffer().CustomerID, "nobody can accept it: the ownership check never matches an empty subject")
}

func TestAsOfferOfASupersededOfferExpiresWhenItWasReplaced(t *testing.T) {
	o := offerExpiringAt(offerNow.Add(23 * time.Hour))
	o.SupersededBy, o.SupersededAt = "off-2", offerNow

	got := o.AsOffer()

	assert.Equal(t, offerNow, got.ExpiresAt)
	assert.True(t, got.IsExpired(offerNow), "the acceptance answers offer_expired")
}

func TestAsOfferKeepsTheExpiryWhenTheReplacementCameLater(t *testing.T) {
	o := offerExpiringAt(offerNow)
	o.SupersededBy, o.SupersededAt = "off-2", offerNow.Add(time.Hour)

	assert.Equal(t, offerNow, o.AsOffer().ExpiresAt)
}

func TestRequoteIdempotencyKeyIsPerReplacedOffer(t *testing.T) {
	assert.Equal(t, "offer-requoted:off-1", domain.RequoteIdempotencyKey("off-1"))
}
