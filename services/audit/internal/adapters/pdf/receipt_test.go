package pdf_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/pdf"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

// The receipt of a revocation (HU-M1.3-02 criterion 4).

func revocation() (domain.Revocation, domain.Consent) {
	terms := domain.TermsV1()
	c := domain.Consent{
		ID: "cns-0123456789abcdef", CustomerID: "cust-1", Holder: domain.Holder{DocumentID: "1020304050", FullName: "Ana María Pérez"},
		TermsVersion: terms.Version, Scope: terms.Sources, Purpose: terms.Purpose, Channel: domain.ChannelEmbedded,
		PartnerID: "aliado-banco-1", GrantedAt: time.Date(2026, 3, 1, 14, 30, 0, 0, time.UTC),
	}
	rev := domain.Revocation{
		ID: "rvk-1", ConsentID: c.ID, ReceiptNumber: "REV-2026-0088", Holder: c.Holder, Scope: c.Scope,
		Entities:    []domain.Entity{{Code: "aliado-banco-1", Name: "Banco Aliado"}},
		RequestedAt: time.Date(2026, 10, 24, 19, 59, 58, 0, time.UTC), EffectiveAt: time.Date(2026, 10, 24, 20, 0, 1, 123456000, time.UTC),
		Channel: domain.RevocationChannelMobileApp, Device: "Google Pixel 8", ConfirmationMethod: domain.ConfirmationBiometric,
		Actor: domain.Actor{Type: domain.ActorHolder, ID: "sub-1"}, RequestID: "req-1",
	}
	return rev, c
}

func TestTheSameRevocationAlwaysGivesTheSameReceipt(t *testing.T) {
	rev, c := revocation()
	for _, r := range []pdf.Renderer{pdf.New(), pdf.NewUncompressed()} {
		first, err := r.Render(rev, c, "es")
		require.NoError(t, err)
		second, err := r.Render(rev, c, "es")
		require.NoError(t, err)

		assert.True(t, bytes.HasPrefix(first, []byte("%PDF-")))
		assert.Equal(t, first, second, "deterministic: the same record gives the same bytes")
	}
}

func TestTheReceiptCarriesTheFieldsOfCriterion4(t *testing.T) {
	rev, c := revocation()

	out, err := pdf.NewUncompressed().Render(rev, c, "es")

	require.NoError(t, err)
	text := string(out)
	for _, want := range []string{
		"REV-2026-0088", "cns-0123456789abcdef", "Banco Aliado", "Ana Mar", "******4050",
		"Comprobante de revocaci", "Fin de la autorizaci",
		// The exact end, with seconds and offset, in Colombia and in UTC (a
		// PDF string escapes the parentheses).
		`24/10/2026 15:00:01 -05:00 \(Colombia\)`, "24/10/2026 20:00:01 UTC",
		`24/10/2026 14:59:58 -05:00 \(Colombia\)`, "Google Pixel 8", "Aplicaci", "Biometr", "Open Finance",
		"productos de cr", "risk_profiling_mortgage_life_offer",
	} {
		assert.Contains(t, text, want)
	}
	assert.NotContains(t, text, "1020304050", "the document is masked")
}

func TestTheReceiptInEnglish(t *testing.T) {
	rev, c := revocation()

	en, err := pdf.NewUncompressed().Render(rev, c, "en-US")
	require.NoError(t, err)
	es, err := pdf.NewUncompressed().Render(rev, c, "")
	require.NoError(t, err)

	assert.Contains(t, string(en), "Consent revocation receipt")
	assert.Contains(t, string(en), "Authorization ended")
	assert.Contains(t, string(en), "Solventa mobile app")
	assert.Contains(t, string(es), "Comprobante de revocaci", "Spanish by default")
	assert.NotEqual(t, en, es)
}

func TestTheReceiptOfThePortalWithoutDeviceNorMethod(t *testing.T) {
	rev, c := revocation()
	rev.Channel, rev.Device, rev.ConfirmationMethod = "compliance_portal", "", ""

	out, err := pdf.NewUncompressed().Render(rev, c, "es")

	require.NoError(t, err)
	assert.Contains(t, string(out), "compliance_portal", "an unknown channel is printed as it is")
	assert.NotContains(t, string(out), "Dispositivo")
}

func TestTheReceiptNeedsTheRecord(t *testing.T) {
	rev, c := revocation()
	rev.ReceiptNumber = ""

	_, err := pdf.New().Render(rev, c, "es")

	assert.Error(t, err)
}

func TestMaskDocument(t *testing.T) {
	assert.Equal(t, "******4050", pdf.MaskDocument("1020304050"))
	assert.Equal(t, "****", pdf.MaskDocument("4050"))
	assert.Equal(t, "*2345", pdf.MaskDocument(" A2345 "))
	assert.Equal(t, "", pdf.MaskDocument(""))
}

func TestContentType(t *testing.T) {
	assert.Equal(t, "application/pdf", pdf.ContentType)
}
