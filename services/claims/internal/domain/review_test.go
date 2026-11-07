package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// HU-W2.1-03: the review of the advisor before a claim enters evaluation.

var revisadoEn = time.Date(2026, 11, 9, 15, 0, 0, 0, time.FixedZone("UTC-5", -5*60*60))

func radicacion(status domain.Status) domain.ClaimRecord {
	return domain.ClaimRecord{Claim: domain.Claim{ID: "cl-1", FilingNumber: "SIN-2026-00007", Subject: "sub-carolina",
		IncidentType: domain.IncidentWaterDamage, Status: status}, CatalogVersion: "claim-catalog-v1"}
}

func TestOnlyAFilingWaitingForReviewIsReviewed(t *testing.T) {
	assert.True(t, domain.Reviewable(domain.StatusActive))
	for _, s := range []domain.Status{domain.StatusReturned, domain.StatusInEvaluation, domain.StatusApproved,
		domain.StatusPaid, domain.StatusRejected, domain.StatusPaymentException} {
		assert.False(t, domain.Reviewable(s), s)
		_, err := domain.ApproveFiling(radicacion(s), "asesor", revisadoEn)
		assert.ErrorIs(t, err, domain.ErrNotReviewable, s)
		_, err = domain.ReturnFiling(radicacion(s), "asesor",
			domain.ReturnRequest{Reason: "x", FactsToCorrect: []domain.Fact{domain.FactPlace}}, revisadoEn)
		assert.ErrorIs(t, err, domain.ErrNotReviewable, s)
	}
}

func TestAnApprovedFilingEntersEvaluation(t *testing.T) {
	r, err := domain.ApproveFiling(radicacion(domain.StatusActive), "asesor@solventa.example", revisadoEn)

	require.NoError(t, err)
	assert.Equal(t, domain.ReviewApproved, r.Outcome)
	assert.Equal(t, "cl-1", r.ClaimID)
	assert.Equal(t, "SIN-2026-00007", r.FilingNumber)
	assert.Equal(t, "asesor@solventa.example", r.ReviewedBy)
	assert.Equal(t, time.UTC, r.ReviewedAt.Location())
	assert.True(t, r.ReviewedAt.Equal(revisadoEn))
	assert.Empty(t, r.Reason)
	assert.Equal(t, domain.StatusInEvaluation, r.StatusOf())
	assert.True(t, domain.Triageable(r.StatusOf()), "it enters the triage")
}

func TestAReturnedFilingKeepsTheReasonAndWhatIsMissing(t *testing.T) {
	r, err := domain.ReturnFiling(radicacion(domain.StatusActive), "asesor", domain.ReturnRequest{
		Reason:          "  La factura no es legible.  ",
		MissingSupports: []string{domain.SupportRepairQuote, " " + domain.SupportRepairQuote, domain.SupportDamagePhotos},
		FactsToCorrect:  []domain.Fact{domain.FactEstimatedAmount, domain.FactEstimatedAmount},
	}, revisadoEn)

	require.NoError(t, err)
	assert.Equal(t, domain.ReviewReturned, r.Outcome)
	assert.Equal(t, "La factura no es legible.", r.Reason)
	assert.Equal(t, []string{domain.SupportRepairQuote, domain.SupportDamagePhotos}, r.MissingSupports, "each once")
	assert.Equal(t, []domain.Fact{domain.FactEstimatedAmount}, r.FactsToCorrect)
	assert.Equal(t, domain.StatusReturned, r.StatusOf())
	assert.False(t, domain.Triageable(r.StatusOf()), "a returned filing is not triaged")
	assert.False(t, domain.Decidable(r.StatusOf()), "nor decided")
}

func TestAReturnOnlyOfFactsIsEnough(t *testing.T) {
	r, err := domain.ReturnFiling(radicacion(domain.StatusActive), "asesor", domain.ReturnRequest{
		Reason: "La fecha no coincide con la denuncia.", FactsToCorrect: []domain.Fact{domain.FactOccurredAt}}, revisadoEn)

	require.NoError(t, err)
	assert.Empty(t, r.MissingSupports)
	assert.Equal(t, []domain.Fact{domain.FactOccurredAt}, r.FactsToCorrect)
}

func TestAReturnIsRefusedWithoutWhatTheInsuredHasToDo(t *testing.T) {
	for name, tc := range map[string]struct {
		in   domain.ReturnRequest
		want error
	}{
		"no reason":    {domain.ReturnRequest{Reason: " \t", FactsToCorrect: []domain.Fact{domain.FactPlace}}, domain.ErrReturnReasonRequired},
		"long reason":  {domain.ReturnRequest{Reason: strings.Repeat("ñ", 1001), FactsToCorrect: []domain.Fact{domain.FactPlace}}, domain.ErrReturnReasonTooLong},
		"nothing":      {domain.ReturnRequest{Reason: "Falta información"}, domain.ErrNothingRequested},
		"blank lists":  {domain.ReturnRequest{Reason: "x", MissingSupports: []string{}, FactsToCorrect: []domain.Fact{}}, domain.ErrNothingRequested},
		"other type":   {domain.ReturnRequest{Reason: "x", MissingSupports: []string{domain.SupportPoliceReport}}, domain.ErrSupportNotApplicable},
		"unknown code": {domain.ReturnRequest{Reason: "x", MissingSupports: []string{"selfie"}}, domain.ErrSupportNotApplicable},
		"unknown fact": {domain.ReturnRequest{Reason: "x", FactsToCorrect: []domain.Fact{"policy_id"}}, domain.ErrSupportNotApplicable},
	} {
		_, err := domain.ReturnFiling(radicacion(domain.StatusActive), "asesor", tc.in, revisadoEn)
		assert.ErrorIs(t, err, tc.want, name)
	}
	_, err := domain.ReturnFiling(radicacion(domain.StatusActive), "asesor",
		domain.ReturnRequest{Reason: strings.Repeat("ñ", 1000), FactsToCorrect: []domain.Fact{domain.FactPlace}}, revisadoEn)
	assert.NoError(t, err, "1000 characters is the limit, whatever their bytes")
}

func TestAFilingOfThePhoneIsReturnedWithTheSupportsOfItsType(t *testing.T) {
	phone := domain.ClaimRecord{Claim: domain.Claim{ID: "cl-2", IncidentType: domain.IncidentTheft,
		Status: domain.StatusActive}, Channel: domain.ChannelMobile}

	r, err := domain.ReturnFiling(phone, "asesor",
		domain.ReturnRequest{Reason: "Falta la denuncia.", MissingSupports: []string{domain.SupportPoliceReport}}, revisadoEn)

	require.NoError(t, err)
	assert.Equal(t, []string{domain.SupportPoliceReport}, r.MissingSupports)
}

func TestTheFactsThatCanBeCorrected(t *testing.T) {
	for _, f := range []domain.Fact{domain.FactOccurredAt, domain.FactPlace, domain.FactDescription, domain.FactEstimatedAmount} {
		assert.True(t, f.Valid(), f)
	}
	for _, f := range []domain.Fact{"", "policy_id", "incident_type", "evidence"} {
		assert.False(t, f.Valid(), f)
	}
}
