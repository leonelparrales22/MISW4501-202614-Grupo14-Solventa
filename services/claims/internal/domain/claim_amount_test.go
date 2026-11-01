package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// HU-M2.2-01: el monto estimado de la radicación del móvil, con las reglas de
// la web (HU-W2.2-01).

func TestThePhoneFilingKeepsTheEstimatedAmount(t *testing.T) {
	r := validRequest()
	r.EstimatedAmountMinor = " 480000000 "

	c, err := r.Validate(now)

	require.NoError(t, err)
	require.NotNil(t, c.EstimatedAmountMinor)
	assert.EqualValues(t, 480_000_000, *c.EstimatedAmountMinor)

	sinMonto, err := validRequest().Validate(now)
	require.NoError(t, err)
	assert.Nil(t, sinMonto.EstimatedAmountMinor, "it is optional")
}

func TestAnEstimatedAmountOutOfTheRulesIsAFieldError(t *testing.T) {
	for monto, code := range map[string]string{
		"1.5":                  domain.CodeInvalidFormat,
		"abc":                  domain.CodeInvalidFormat,
		"0":                    domain.CodeOutOfRange,
		"-100":                 domain.CodeOutOfRange,
		"10000000000001":       domain.CodeOutOfRange,
		"99999999999999999999": domain.CodeOutOfRange,
	} {
		r := validRequest()
		r.EstimatedAmountMinor = monto

		err := mustFail(t, r)

		var invalid *domain.ValidationError
		require.ErrorAs(t, err, &invalid, monto)
		assert.Equal(t, []domain.FieldError{{Field: "estimated_amount_minor", Code: code}}, invalid.Fields, monto)
	}
}

func TestTheEstimatedAmountIsPartOfThePhoneFingerprintOnlyWhenDeclared(t *testing.T) {
	sinMonto, err := validRequest().Validate(now)
	require.NoError(t, err)
	r := validRequest()
	r.EstimatedAmountMinor = "480000000"
	conMonto, err := r.Validate(now)
	require.NoError(t, err)
	r.EstimatedAmountMinor = "500000000"
	otroMonto, err := r.Validate(now)
	require.NoError(t, err)

	assert.NotEqual(t, sinMonto.Fingerprint(), conMonto.Fingerprint(), "another amount is another filing")
	assert.NotEqual(t, conMonto.Fingerprint(), otroMonto.Fingerprint())

	// Without the amount the fingerprint is the one of the facts alone, the
	// same it was before HU-M2.2-01, so the retries of older filings still
	// find them.
	conMonto.EstimatedAmountMinor = nil
	assert.Equal(t, sinMonto.Fingerprint(), conMonto.Fingerprint())
}
