package pdf_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/pdf"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

func receipt() domain.Receipt {
	return domain.Receipt{
		FilingNumber: "SIN-2026-01712", Status: domain.StatusActive,
		// 00:05 UTC on the 15th is 19:05 on the 14th in Colombia.
		FiledAt:  time.Date(2026, 9, 15, 0, 5, 0, 0, time.UTC),
		PolicyID: "AU-2025-118204", Product: "Autos", InsuredObject: "KJT-482",
		IncidentLabel: "Colisión con daños al vehículo",
		OccurredAt:    time.Date(2026, 9, 14, 23, 40, 0, 0, time.UTC),
		Place:         "Calle 80 con carrera 30, Bogotá",
		Supports: domain.SupportSummary{RequiredTotal: 3, RequiredAttached: 3, Files: 8, Items: []domain.SupportItem{
			{RequirementCode: "vehicle_photos", Label: "Fotos del vehículo", Files: make([]domain.SupportFile, 6)},
			{RequirementCode: "driver_document", Label: "Documento del conductor", Files: make([]domain.SupportFile, 1)},
			{RequirementCode: "traffic_report", Label: "Informe de tránsito o croquis", Files: make([]domain.SupportFile, 1)},
		}},
	}
}

func TestTheReceiptHasWhatTheInsuredQuotes(t *testing.T) {
	out, err := pdf.NewReceiptRenderer().Render(receipt())

	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(out, []byte("%PDF-")))
	text := string(out)
	for _, want := range []string{
		"SIN-2026-01712", "Radicado", "AU-2025-118204", "Autos", "KJT-482",
		"14/09/2026 19:05", "14/09/2026 18:40", "hora de Colombia",
		"3 de 3", "6 archivos", "1 archivo",
	} {
		assert.Contains(t, text, want)
	}
	assert.Contains(t, text, "Colisi\xf3n con da\xf1os al veh\xedculo", "accents are encoded for the PDF core fonts")
}

func TestTheReceiptIsDeterministic(t *testing.T) {
	a, err := pdf.NewReceiptRenderer().Render(receipt())
	require.NoError(t, err)
	b, err := pdf.NewReceiptRenderer().Render(receipt())
	require.NoError(t, err)

	assert.Equal(t, a, b, "downloading it twice gives the same file")
	// Both dates are the filing date, never the clock: two downloads a second
	// apart are the same file too.
	assert.Contains(t, string(a), "/CreationDate (D:20260915000500)")
	assert.Contains(t, string(a), "/ModDate (D:20260915000500)")
}

func TestAReceiptOfAMobileClaimLeavesOutWhatItDoesNotHave(t *testing.T) {
	r := receipt()
	r.Product, r.InsuredObject, r.Supports = "", "", domain.SupportSummary{}
	r.Status = "unknown"

	out, err := pdf.NewReceiptRenderer().Render(r)

	require.NoError(t, err)
	assert.NotContains(t, string(out), "Bien asegurado")
	assert.Contains(t, string(out), "0 de 0")
	assert.Contains(t, string(out), "unknown")
}

func TestAReceiptNeedsItsFilingNumber(t *testing.T) {
	_, err := pdf.NewReceiptRenderer().Render(domain.Receipt{})
	assert.Error(t, err)
}
