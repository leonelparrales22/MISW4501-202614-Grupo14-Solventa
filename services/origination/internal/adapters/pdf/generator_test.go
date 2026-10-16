package pdf_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/pdf"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

func contract() domain.ContractData {
	at := time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)
	return domain.ContractData{
		PolicyID: "pol-123", Version: 1,
		Holder:  domain.Applicant{FullName: "Ana María Pérez", DocumentID: "1032456789", Email: "ana@example.com"},
		Premium: 132_800, Currency: "COP", InsuredAmount: 250_000_000,
		Coverages: []domain.Coverage{
			{Code: domain.CoverageDeath, InsuredAmount: 250_000_000},
			{Code: domain.CoverageDisability, InsuredAmount: 250_000_000},
		},
		CoverageStart: at, CoverageEnd: at.AddDate(10, 0, 0), ActivatedAt: at, GeneratedAt: at.Add(3 * time.Second),
	}
}

func TestGenerateRendersAPDFWithTheContractualContent(t *testing.T) {
	out, err := pdf.New().Generate(context.Background(), contract())

	require.NoError(t, err)
	assert.True(t, bytes.HasPrefix(out, []byte("%PDF-")))
	text := string(out)
	for _, want := range []string{
		"pol-123", "1032456789", "ana@example.com", "COP 132.800", "COP 250.000.000",
		"16/10/2026", "16/10/2036", "Fallecimiento del asegurado", "Incapacidad total y permanente", "Condiciones generales",
	} {
		assert.Contains(t, text, want)
	}
	assert.Contains(t, text, "Ana Mar\xeda P\xe9rez", "accents are encoded for the PDF core fonts")
}

func TestGenerateIsDeterministic(t *testing.T) {
	a, err := pdf.New().Generate(context.Background(), contract())
	require.NoError(t, err)
	b, err := pdf.New().Generate(context.Background(), contract())
	require.NoError(t, err)
	assert.Equal(t, a, b, "the checksum of a document proves what the insured received")
}

func TestGenerateRejectsIncompleteData(t *testing.T) {
	empty := contract()
	empty.Coverages = nil
	_, err := pdf.New().Generate(context.Background(), empty)
	assert.Error(t, err)

	noID := contract()
	noID.PolicyID = ""
	_, err = pdf.New().Generate(context.Background(), noID)
	assert.Error(t, err)
}

func TestGenerateHonoursACanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := pdf.New().Generate(ctx, contract())
	assert.ErrorIs(t, err, context.Canceled)
}
