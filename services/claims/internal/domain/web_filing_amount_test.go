package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// The estimated amount the insured declares in the web filing (HU-W2.2-01,
// decision D12).

func TestTheEstimatedAmountIsOptional(t *testing.T) {
	u := newUploads()
	c, err := validateWeb(validWebFiling(u), u)

	require.NoError(t, err)
	assert.Nil(t, c.EstimatedAmountMinor)
}

func TestTheEstimatedAmountAtItsLimits(t *testing.T) {
	cases := []struct {
		raw  string
		want int64
		code string
	}{
		{raw: "1", want: 1},
		{raw: "480000000", want: 480000000},
		{raw: " 480000000 ", want: 480000000},
		{raw: "10000000000000", want: domain.MaxEstimatedAmountMinor},
		{raw: "0", code: domain.CodeOutOfRange},
		{raw: "-5", code: domain.CodeOutOfRange},
		{raw: "10000000000001", code: domain.CodeOutOfRange},
		{raw: "99999999999999999999", code: domain.CodeOutOfRange},
		{raw: "4.8e8", code: domain.CodeInvalidFormat},
		{raw: "4800000.50", code: domain.CodeInvalidFormat},
		{raw: `"480000000"`, code: domain.CodeInvalidFormat},
		{raw: "true", code: domain.CodeInvalidFormat},
		{raw: "{}", code: domain.CodeInvalidFormat},
	}
	for _, c := range cases {
		u := newUploads()
		r := validWebFiling(u)
		r.EstimatedAmountMinor = c.raw

		claim, err := validateWeb(r, u)

		if c.code == "" {
			require.NoError(t, err, c.raw)
			require.NotNil(t, claim.EstimatedAmountMinor, c.raw)
			assert.Equal(t, c.want, *claim.EstimatedAmountMinor, c.raw)
			continue
		}
		var invalid *domain.ValidationError
		require.ErrorAs(t, err, &invalid, c.raw)
		assert.Equal(t, []domain.FieldError{{Field: "estimated_amount_minor", Code: c.code}}, invalid.Fields, c.raw)
	}
}

func TestAWrongAmountIsReportedWithTheOtherFields(t *testing.T) {
	u := newUploads()
	r := validWebFiling(u)
	r.Place, r.EstimatedAmountMinor = "", "0"

	_, err := validateWeb(r, u)

	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.Contains(t, invalid.Fields, domain.FieldError{Field: "place", Code: domain.CodeRequired})
	assert.Contains(t, invalid.Fields, domain.FieldError{Field: "estimated_amount_minor", Code: domain.CodeOutOfRange})
}

// A claim filed without amount keeps the fingerprint it had before
// HU-W2.2-01, so a retry of a filing made before it still finds its claim.
func TestTheFingerprintWithoutAmountIsTheOneOfBefore(t *testing.T) {
	c := domain.ClaimRecord{
		Claim: domain.Claim{PolicyID: "HG-2025-880231", IncidentType: domain.IncidentWaterDamage,
			OccurredAt: time.Date(2026, 9, 3, 3, 15, 0, 0, time.UTC), Place: "Carrera 15 # 93-40, apto 402, Bogotá",
			Description: "Filtración desde el apartamento superior.", Details: map[string]string{}},
		Supports: []domain.AttachedSupport{
			{RequirementCode: "damage_photos", File: domain.SupportFile{ID: "sup-00000000000000000000000000000001"}},
			{RequirementCode: "repair_quote", File: domain.SupportFile{ID: "sup-00000000000000000000000000000002"}},
		},
	}
	// Captured from the code of HU-W2.1-01 before the amount existed.
	const before = "d5dcb07cfd8cbd0aa89e788c458b367bfc9e3172132fd13eb966d403e7e2d867"
	assert.Equal(t, before, c.WebFingerprint())

	amount := int64(480000000)
	c.EstimatedAmountMinor = &amount
	withAmount := c.WebFingerprint()
	assert.NotEqual(t, before, withAmount, "the amount is part of what was filed")

	other := int64(480000001)
	c.EstimatedAmountMinor = &other
	assert.NotEqual(t, withAmount, c.WebFingerprint(), "another amount is another filing")
}
