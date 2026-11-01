package httpadapter_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// HU-M2.2-01: el asistente del móvil declara el monto estimado, como la web
// (HU-W2.2-01), para que el triage y la evaluación lo tengan.

// conMonto es validBody con el monto escrito tal cual.
func conMonto(monto string) string {
	return strings.Replace(validBody, `"declaration_accepted":true}`,
		`"declaration_accepted":true,"estimated_amount_minor":`+monto+`}`, 1)
}

func TestTheFilingCarriesTheEstimatedAmountAsItCame(t *testing.T) {
	for monto, want := range map[string]string{
		"480000000":   "480000000",
		"1.5":         "1.5",
		`"480000000"`: `"480000000"`,
		"null":        "",
	} {
		svc := &fakeFiling{claim: filedClaim(), created: true}

		call(router(svc), "good", "draft-1", conMonto(monto))

		assert.Equal(t, want, svc.gotRequest.EstimatedAmountMinor, monto)
	}
	svc := &fakeFiling{claim: filedClaim(), created: true}
	call(router(svc), "good", "draft-1", validBody)
	assert.Empty(t, svc.gotRequest.EstimatedAmountMinor, "without the field nothing is declared")
}

func TestTheAnswerShowsTheEstimatedAmountOnlyWhenDeclared(t *testing.T) {
	sinMonto := &fakeFiling{claim: filedClaim(), created: true}
	_, body := call(router(sinMonto), "good", "draft-1", validBody)
	assert.NotContains(t, body, "estimated_amount_minor")

	c := filedClaim()
	monto := int64(480_000_000)
	c.EstimatedAmountMinor = &monto
	conMontoSvc := &fakeFiling{claim: c, created: true}
	rec, body := call(router(conMontoSvc), "good", "draft-1", conMonto("480000000"))

	require.Equal(t, http.StatusCreated, rec.Code)
	assert.EqualValues(t, 480_000_000, body["estimated_amount_minor"])
}
