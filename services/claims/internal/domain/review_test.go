package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// HU-W2.1-03: the review of the advisor before a claim enters evaluation,
// with a verdict on each support (screen S8).

var (
	revisadoEn = time.Date(2026, 11, 9, 15, 0, 0, 0, time.FixedZone("UTC-5", -5*60*60))
	juan       = domain.Reviewer{ID: "juan@solventa.example", Name: "Juan Ríos"}
)

// colision is a web filing of a collision with the photos, the document of
// the driver and the traffic report, and no photograph of the plate.
func colision(status domain.Status) domain.ClaimRecord {
	attach := func(code string, n int) []domain.AttachedSupport {
		out := make([]domain.AttachedSupport, n)
		for i := range out {
			out[i] = domain.AttachedSupport{RequirementCode: code, Position: i}
		}
		return out
	}
	var supports []domain.AttachedSupport
	supports = append(supports, attach(domain.SupportVehiclePhotos, 6)...)
	supports = append(supports, attach(domain.SupportDriverDocument, 1)...)
	supports = append(supports, attach(domain.SupportTrafficReport, 1)...)
	return domain.ClaimRecord{Claim: domain.Claim{ID: "cl-1", FilingNumber: "SIN-2026-01877", Subject: "sub-carolina",
		IncidentType: domain.IncidentCollision, Status: status}, Channel: domain.ChannelWeb,
		CatalogVersion: domain.CatalogVersionV1, Supports: supports}
}

func devolucion() domain.ReturnRequest {
	return domain.ReturnRequest{
		ReasonCode: domain.ReasonIllegibleOrIncompleteSupports,
		Message:    "  El informe de tránsito no se puede leer y falta la foto de la placa.  ",
		Supports: []domain.SupportVerdict{
			{RequirementCode: domain.SupportTrafficReport, Verdict: domain.VerdictIllegible, Note: " borroso, no se lee el número de croquis "},
			{RequirementCode: domain.SupportPlatePhoto, Verdict: domain.VerdictMissing},
		},
	}
}

func verdicts(r domain.Review) map[string]domain.Verdict {
	out := map[string]domain.Verdict{}
	for _, a := range r.Supports {
		out[a.RequirementCode] = a.Verdict
	}
	return out
}

func TestTheChecklistOfAWebFilingIsTheCatalogOfItsType(t *testing.T) {
	got := domain.Checklist(colision(domain.StatusActive))

	require.Len(t, got, 4)
	assert.Equal(t, domain.ChecklistItem{RequirementCode: domain.SupportVehiclePhotos, Label: "Fotos del vehículo",
		Required: true, Files: 6}, got[0])
	assert.Equal(t, domain.ChecklistItem{RequirementCode: domain.SupportPlatePhoto, Label: "Fotografía de la placa",
		Required: false, Files: 0}, got[3], "the photograph of the plate is optional")
}

func TestTheChecklistOfAFilingOfThePhoneIsItsEvidence(t *testing.T) {
	phone := domain.ClaimRecord{Claim: domain.Claim{IncidentType: domain.IncidentTheft,
		Evidence: make([]domain.Evidence, 4)}, Channel: domain.ChannelMobile}

	got := domain.Checklist(phone)

	require.Len(t, got, 3)
	assert.Equal(t, domain.ChecklistItem{RequirementCode: domain.SupportVehiclePhotos, Label: "Fotos del vehículo",
		Required: true, Files: 4}, got[0], "the evidence is the photos of the vehicle")
	for _, item := range got[1:] {
		assert.False(t, item.Required, "%s can be asked for, not required", item.RequirementCode)
		assert.Zero(t, item.Files)
	}
}

func TestOnlyAFilingWaitingForReviewIsReviewed(t *testing.T) {
	assert.True(t, domain.Reviewable(domain.StatusActive))
	for _, s := range []domain.Status{domain.StatusReturned, domain.StatusInEvaluation, domain.StatusApproved,
		domain.StatusPaid, domain.StatusRejected, domain.StatusPaymentException} {
		assert.False(t, domain.Reviewable(s), s)
		_, err := domain.ApproveFiling(colision(s), juan, revisadoEn)
		assert.ErrorIs(t, err, domain.ErrNotReviewable, s)
		_, err = domain.ReturnFiling(colision(s), juan, devolucion(), revisadoEn)
		assert.ErrorIs(t, err, domain.ErrNotReviewable, s)
	}
}

func TestAnApprovedFilingHasEveryAttachedSupportValid(t *testing.T) {
	r, err := domain.ApproveFiling(colision(domain.StatusActive), juan, revisadoEn)

	require.NoError(t, err)
	assert.Equal(t, domain.ReviewApproved, r.Outcome)
	assert.Equal(t, "SIN-2026-01877", r.FilingNumber)
	assert.Equal(t, "juan@solventa.example", r.ReviewedBy)
	assert.Equal(t, "Juan Ríos", r.ReviewerName)
	assert.Equal(t, time.UTC, r.ReviewedAt.Location())
	assert.Equal(t, map[string]domain.Verdict{domain.SupportVehiclePhotos: domain.VerdictValid,
		domain.SupportDriverDocument: domain.VerdictValid, domain.SupportTrafficReport: domain.VerdictValid},
		verdicts(r), "the optional photograph of the plate that is not attached is not listed")
	assert.Empty(t, r.ReasonCode)
	assert.Empty(t, r.Message)
	assert.Equal(t, domain.StatusInEvaluation, r.StatusOf())
	assert.True(t, domain.Triageable(r.StatusOf()), "it enters the triage")
}

func TestAFilingWithARequiredSupportMissingIsNotApproved(t *testing.T) {
	c := colision(domain.StatusActive)
	c.Supports = c.Supports[:7] // without the traffic report

	_, err := domain.ApproveFiling(c, juan, revisadoEn)
	assert.ErrorIs(t, err, domain.ErrSupportsIncomplete)

	phone := domain.ClaimRecord{Claim: domain.Claim{IncidentType: domain.IncidentCollision, Status: domain.StatusActive},
		Channel: domain.ChannelMobile}
	_, err = domain.ApproveFiling(phone, juan, revisadoEn)
	assert.ErrorIs(t, err, domain.ErrSupportsIncomplete, "a filing of the phone without evidence")

	phone.Evidence = make([]domain.Evidence, 4)
	_, err = domain.ApproveFiling(phone, juan, revisadoEn)
	assert.NoError(t, err, "with its evidence it is approved: the other supports of the phone are not required")
}

func TestAReturnedFilingKeepsTheVerdictOfEachSupport(t *testing.T) {
	r, err := domain.ReturnFiling(colision(domain.StatusActive), juan, devolucion(), revisadoEn)

	require.NoError(t, err)
	assert.Equal(t, domain.ReviewReturned, r.Outcome)
	assert.Equal(t, domain.ReasonIllegibleOrIncompleteSupports, r.ReasonCode)
	assert.Equal(t, "El informe de tránsito no se puede leer y falta la foto de la placa.", r.Message)
	assert.Equal(t, []domain.SupportAssessment{
		{RequirementCode: domain.SupportVehiclePhotos, Label: "Fotos del vehículo", Verdict: domain.VerdictValid},
		{RequirementCode: domain.SupportDriverDocument, Label: "Documento del conductor", Verdict: domain.VerdictValid},
		{RequirementCode: domain.SupportTrafficReport, Label: "Informe de tránsito o croquis", Verdict: domain.VerdictIllegible,
			Note: "borroso, no se lee el número de croquis"},
		{RequirementCode: domain.SupportPlatePhoto, Label: "Fotografía de la placa", Verdict: domain.VerdictMissing},
	}, r.Supports, "in the order of the catalog; what the advisor does not mention is valid")
	assert.Equal(t, domain.StatusReturned, r.StatusOf())
	assert.False(t, domain.Triageable(r.StatusOf()), "a returned filing is not triaged")
	assert.False(t, domain.Decidable(r.StatusOf()), "nor decided")
}

func TestARequiredSupportThatIsNotAttachedIsMissingWithoutSayingIt(t *testing.T) {
	c := colision(domain.StatusActive)
	c.Supports = c.Supports[:7]

	r, err := domain.ReturnFiling(c, juan, domain.ReturnRequest{ReasonCode: domain.ReasonMissingInformation,
		Message: "Falta el informe de tránsito."}, revisadoEn)

	require.NoError(t, err)
	assert.Equal(t, domain.VerdictMissing, verdicts(r)[domain.SupportTrafficReport])
	assert.NotContains(t, verdicts(r), domain.SupportPlatePhoto, "an optional one is only listed when asked for")
}

func TestAReturnOnlyOfFactsIsEnough(t *testing.T) {
	r, err := domain.ReturnFiling(colision(domain.StatusActive), juan, domain.ReturnRequest{
		ReasonCode: domain.ReasonInconsistentFacts, Message: "La fecha no coincide con el informe.",
		FactsToCorrect: []domain.Fact{domain.FactOccurredAt}}, revisadoEn)

	require.NoError(t, err)
	assert.Equal(t, []domain.Fact{domain.FactOccurredAt}, r.FactsToCorrect)
	for _, a := range r.Supports {
		assert.Equal(t, domain.VerdictValid, a.Verdict, a.RequirementCode)
	}
}

func TestAReturnIsRefusedWhenItDoesNotSayWhatToDo(t *testing.T) {
	with := func(change func(*domain.ReturnRequest)) domain.ReturnRequest {
		in := devolucion()
		change(&in)
		return in
	}
	for name, tc := range map[string]struct {
		in   domain.ReturnRequest
		want error
	}{
		"reason of no list": {with(func(in *domain.ReturnRequest) { in.ReasonCode = "porque sí" }), domain.ErrReasonCodeInvalid},
		"no message":        {with(func(in *domain.ReturnRequest) { in.Message = " \t" }), domain.ErrMessageRequired},
		"long message":      {with(func(in *domain.ReturnRequest) { in.Message = strings.Repeat("ñ", 1001) }), domain.ErrMessageTooLong},
		"long note": {with(func(in *domain.ReturnRequest) { in.Supports[0].Note = strings.Repeat("ñ", 301) }),
			domain.ErrVerdictNoteTooLong},
		"everything valid": {with(func(in *domain.ReturnRequest) {
			in.Supports = []domain.SupportVerdict{{RequirementCode: domain.SupportTrafficReport, Verdict: domain.VerdictValid}}
		}), domain.ErrNothingRequested},
		"support of no type": {with(func(in *domain.ReturnRequest) { in.Supports[0].RequirementCode = domain.SupportPoliceReport }),
			domain.ErrSupportNotApplicable},
		"repeated support": {with(func(in *domain.ReturnRequest) { in.Supports[1] = in.Supports[0] }), domain.ErrSupportNotApplicable},
		"unknown fact":     {with(func(in *domain.ReturnRequest) { in.FactsToCorrect = []domain.Fact{"policy_id"} }), domain.ErrSupportNotApplicable},
		"repeated fact": {with(func(in *domain.ReturnRequest) {
			in.FactsToCorrect = []domain.Fact{domain.FactPlace, domain.FactPlace}
		}), domain.ErrSupportNotApplicable},
		"attached marked missing": {with(func(in *domain.ReturnRequest) { in.Supports[0].Verdict = domain.VerdictMissing }),
			domain.ErrVerdictNotApplicable},
		"not attached marked illegible": {with(func(in *domain.ReturnRequest) { in.Supports[1].Verdict = domain.VerdictIllegible }),
			domain.ErrVerdictNotApplicable},
	} {
		_, err := domain.ReturnFiling(colision(domain.StatusActive), juan, tc.in, revisadoEn)
		assert.ErrorIs(t, err, tc.want, name)
	}
	_, err := domain.ReturnFiling(colision(domain.StatusActive), juan,
		with(func(in *domain.ReturnRequest) { in.Message = strings.Repeat("ñ", 1000) }), revisadoEn)
	assert.NoError(t, err, "1000 characters is the limit, whatever their bytes")
}

func TestTheReasonsAndTheFactsOfTheLists(t *testing.T) {
	for _, r := range []domain.ReasonCode{domain.ReasonIllegibleOrIncompleteSupports, domain.ReasonInconsistentFacts,
		domain.ReasonMissingInformation, domain.ReasonOther} {
		assert.True(t, r.Valid(), r)
	}
	assert.False(t, domain.ReasonCode("").Valid())
	for _, f := range []domain.Fact{domain.FactOccurredAt, domain.FactPlace, domain.FactDescription, domain.FactEstimatedAmount} {
		assert.True(t, f.Valid(), f)
	}
	for _, f := range []domain.Fact{"", "policy_id", "incident_type", "evidence"} {
		assert.False(t, f.Valid(), f)
	}
}
