package domain_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// HU-W2.1-04: the insured corrects a returned filing and sends it again
// (screen A9).

var (
	radicadoEn  = time.Date(2026, 9, 14, 23, 55, 0, 0, time.UTC)
	reenviadoEn = time.Date(2026, 9, 16, 15, 0, 0, 0, time.UTC)
)

func supportID(n int) string { return fmt.Sprintf("sup-%032x", n) }

func file(n int, owner string, format domain.FileFormat) domain.SupportFile {
	return domain.SupportFile{ID: supportID(n), OwnerSubject: owner, FileName: fmt.Sprintf("f%d", n), Format: format,
		ConfirmedAt: radicadoEn}
}

// devuelta is the collision of A9, returned: six photos (1-6), the document of
// the driver (7) and an illegible traffic report (8); the photograph of the
// plate is missing.
func devuelta() (domain.ClaimRecord, domain.Review) {
	var supports []domain.AttachedSupport
	add := func(code string, n int, format domain.FileFormat) {
		supports = append(supports, domain.AttachedSupport{RequirementCode: code, Position: len(supports),
			File: file(n, "sub-carolina", format)})
	}
	for n := 1; n <= 6; n++ {
		add(domain.SupportVehiclePhotos, n, domain.FormatJPEG)
	}
	add(domain.SupportDriverDocument, 7, domain.FormatPDF)
	add(domain.SupportTrafficReport, 8, domain.FormatPDF)
	amount := int64(480_000_000)
	c := domain.ClaimRecord{
		Claim: domain.Claim{ID: "cl-1", FilingNumber: "SIN-2026-01877", Subject: "sub-carolina",
			IncidentType: domain.IncidentCollision, Status: domain.StatusReturned,
			OccurredAt: radicadoEn.Add(-15 * time.Minute), Place: "Calle 80 con carrera 30, Bogotá",
			Description: "Me chocaron por detrás en el semáforo.", FiledAt: radicadoEn, EstimatedAmountMinor: &amount},
		Channel: domain.ChannelWeb, CatalogVersion: domain.CatalogVersionV1, Supports: supports,
		Policy: &domain.Policy{ID: "AU-2025-118204", ValidFrom: radicadoEn.AddDate(-1, 0, 0), ValidTo: radicadoEn.AddDate(0, 1, 0)},
	}
	r, err := domain.ReturnFiling(withStatus(c, domain.StatusActive), juan, devolucion(), revisadoEn)
	if err != nil {
		panic(err)
	}
	r.Seq = 1
	return c, r
}

func withStatus(c domain.ClaimRecord, s domain.Status) domain.ClaimRecord {
	c.Status = s
	return c
}

// corrected is the correction of A9: a new traffic report (9) and the
// photograph of the plate (10).
func corrected() (domain.ResubmissionRequest, map[string]domain.SupportFile) {
	return domain.ResubmissionRequest{ReviewSeq: 1, Supports: []domain.SupportGroup{
			{RequirementCode: domain.SupportTrafficReport, SupportIDs: []string{supportID(9)}},
			{RequirementCode: domain.SupportPlatePhoto, SupportIDs: []string{supportID(10)}},
		}}, map[string]domain.SupportFile{
			supportID(9):  file(9, "sub-carolina", domain.FormatPDF),
			supportID(10): file(10, "sub-carolina", domain.FormatJPEG),
		}
}

func idsOf(supports []domain.AttachedSupport, code string) []string {
	var out []string
	for _, a := range supports {
		if a.RequirementCode == code {
			out = append(out, a.File.ID)
		}
	}
	return out
}

func TestAResubmissionReplacesTheIllegibleSupportAndKeepsTheRest(t *testing.T) {
	c, last := devuelta()
	in, uploaded := corrected()

	r, err := domain.Resubmit(c, last, "sub-carolina", in, uploaded, reenviadoEn)

	require.NoError(t, err)
	assert.Equal(t, domain.StatusActive, r.Claim.Status, "it goes back to the advisor")
	assert.Equal(t, "SIN-2026-01877", r.FilingNumber, "with the same filing number")
	assert.Equal(t, 1, r.ReviewSeq)
	assert.Equal(t, "sub-carolina", r.ResubmittedBy)
	assert.Equal(t, reenviadoEn, r.ResubmittedAt)
	assert.Equal(t, []domain.SupportChange{
		{RequirementCode: domain.SupportTrafficReport, Label: "Informe de tránsito o croquis",
			Added: []string{supportID(9)}, Withdrawn: []string{supportID(8)}},
		{RequirementCode: domain.SupportPlatePhoto, Label: "Fotografía de la placa", Added: []string{supportID(10)}},
	}, r.Supports)
	assert.Empty(t, r.Facts, "no fact changed")
	assert.Len(t, idsOf(r.Claim.Supports, domain.SupportVehiclePhotos), 6, "the photos are kept")
	assert.Equal(t, []string{supportID(9)}, idsOf(r.Claim.Supports, domain.SupportTrafficReport))
	require.Len(t, r.Added, 2)
	assert.Equal(t, 8, r.Added[0].Position, "the new supports go after the ones filed")
	assert.Equal(t, 9, r.Added[1].Position)
	assert.Equal(t, domain.StatusReturned, c.Status, "the claim it was given is not changed")
}

func TestAResubmissionSaysEverythingThatIsStillMissing(t *testing.T) {
	c, last := devuelta()

	_, err := domain.Resubmit(c, last, "sub-carolina", domain.ResubmissionRequest{ReviewSeq: 1}, nil, reenviadoEn)

	assert.Equal(t, map[string]string{
		"supports.traffic_report": domain.CodeNotReplaced,
		"supports.plate_photo":    domain.CodeMissing,
	}, fieldsOf(t, err))
}

func TestKeepingTheIllegibleFileIsNotReplacingIt(t *testing.T) {
	c, last := devuelta()
	in, uploaded := corrected()
	in.Supports[0].SupportIDs = []string{supportID(8)}

	_, err := domain.Resubmit(c, last, "sub-carolina", in, uploaded, reenviadoEn)

	assert.Equal(t, map[string]string{"supports.traffic_report": domain.CodeNotReplaced}, fieldsOf(t, err))
}

func TestPhotosCanBeAddedToTheOnesFiled(t *testing.T) {
	c, last := devuelta()
	in, uploaded := corrected()
	photos := domain.SupportGroup{RequirementCode: domain.SupportVehiclePhotos}
	for n := 1; n <= 6; n++ {
		photos.SupportIDs = append(photos.SupportIDs, supportID(n))
	}
	photos.SupportIDs = append(photos.SupportIDs, supportID(11))
	in.Supports = append(in.Supports, photos)
	uploaded[supportID(11)] = file(11, "sub-carolina", domain.FormatPNG)

	r, err := domain.Resubmit(c, last, "sub-carolina", in, uploaded, reenviadoEn)

	require.NoError(t, err)
	assert.Len(t, idsOf(r.Claim.Supports, domain.SupportVehiclePhotos), 7)
	assert.Equal(t, domain.SupportChange{RequirementCode: domain.SupportVehiclePhotos, Label: "Fotos del vehículo",
		Added: []string{supportID(11)}}, r.Supports[2])
}

func TestTheFilesOfASupportAreCheckedAsInTheFiling(t *testing.T) {
	c, last := devuelta()
	in, uploaded := corrected()
	uploaded[supportID(9)] = file(9, "sub-otro", domain.FormatPDF)       // another insured's
	uploaded[supportID(10)] = file(10, "sub-carolina", domain.FormatPDF) // the plate is a photograph
	in.Supports = append(in.Supports,
		domain.SupportGroup{RequirementCode: domain.SupportVehiclePhotos, SupportIDs: []string{supportID(1)}},
		domain.SupportGroup{RequirementCode: domain.SupportPoliceReport, SupportIDs: []string{supportID(12)}},
		domain.SupportGroup{RequirementCode: "NO VALE", SupportIDs: []string{supportID(13)}})

	_, err := domain.Resubmit(c, last, "sub-carolina", in, uploaded, reenviadoEn)

	assert.Equal(t, map[string]string{
		"supports.traffic_report": domain.CodeNotFound,
		"supports.plate_photo":    domain.CodeInvalidFormat,
		"supports.vehicle_photos": domain.CodeTooFew,
		"supports.police_report":  domain.CodeNotApplicable,
		"supports":                domain.CodeNotApplicable,
	}, fieldsOf(t, err))
}

func TestARequiredSupportCannotBeLeftWithoutFiles(t *testing.T) {
	c, last := devuelta()
	in, uploaded := corrected()
	in.Supports = append(in.Supports, domain.SupportGroup{RequirementCode: domain.SupportDriverDocument})

	_, err := domain.Resubmit(c, last, "sub-carolina", in, uploaded, reenviadoEn)

	assert.Equal(t, map[string]string{"supports.driver_document": domain.CodeMissing}, fieldsOf(t, err))
}

func TestTheFactsAreCorrectedWithTheRulesOfTheFiling(t *testing.T) {
	c, last := devuelta()
	in, uploaded := corrected()
	occurred := radicadoEn.Add(-2 * time.Hour)
	in.OccurredAt = text(occurred.Format(time.RFC3339))
	in.Place = text("  Calle 80 con carrera 30, frente al centro comercial, Bogotá ")
	in.Description = text(c.Description) // the same: not a change
	in.EstimatedAmountMinor = text("520000000")

	r, err := domain.Resubmit(c, last, "sub-carolina", in, uploaded, reenviadoEn)

	require.NoError(t, err)
	assert.Equal(t, occurred, r.Claim.OccurredAt)
	assert.Equal(t, "Calle 80 con carrera 30, frente al centro comercial, Bogotá", r.Claim.Place)
	assert.Equal(t, int64(520_000_000), *r.Claim.EstimatedAmountMinor)
	assert.Equal(t, []domain.FactChange{
		{Fact: domain.FactOccurredAt, Before: c.OccurredAt.Format(time.RFC3339), After: occurred.Format(time.RFC3339)},
		{Fact: domain.FactPlace, Before: "Calle 80 con carrera 30, Bogotá",
			After: "Calle 80 con carrera 30, frente al centro comercial, Bogotá"},
		{Fact: domain.FactEstimatedAmount, Before: "480000000", After: "520000000"},
	}, r.Facts)
	assert.Equal(t, int64(480_000_000), *c.EstimatedAmountMinor, "the claim it was given is not changed")
}

func TestAFactThatBreaksTheRulesOfTheFilingIsRefused(t *testing.T) {
	c, last := devuelta()
	in, uploaded := corrected()
	in.OccurredAt = text(radicadoEn.Add(time.Hour).Format(time.RFC3339)) // after it was filed
	in.Place = text("  ")
	in.Description = text("corta")
	in.EstimatedAmountMinor = text("12.5")

	_, err := domain.Resubmit(c, last, "sub-carolina", in, uploaded, reenviadoEn)

	assert.Equal(t, map[string]string{
		"occurred_at":            domain.CodeOutOfRange,
		"place":                  domain.CodeRequired,
		"description":            domain.CodeOutOfRange,
		"estimated_amount_minor": domain.CodeInvalidFormat,
	}, fieldsOf(t, err))
}

func TestTheIncidentMustFallInTheValidityOfThePolicyFiled(t *testing.T) {
	c, last := devuelta()
	in, uploaded := corrected()
	in.OccurredAt = text(c.Policy.ValidFrom.Add(-time.Hour).Format(time.RFC3339))

	_, err := domain.Resubmit(c, last, "sub-carolina", in, uploaded, reenviadoEn)

	assert.Equal(t, map[string]string{"occurred_at": domain.CodeOutOfRange}, fieldsOf(t, err))
}

func TestAnAmountAskedForMustBeDeclared(t *testing.T) {
	c, _ := devuelta()
	c.EstimatedAmountMinor = nil
	ret := domain.ReturnRequest{ReasonCode: domain.ReasonMissingInformation, Message: "Falta el monto estimado.",
		FactsToCorrect: []domain.Fact{domain.FactEstimatedAmount}}
	last, err := domain.ReturnFiling(withStatus(c, domain.StatusActive), juan, ret, revisadoEn)
	require.NoError(t, err)
	last.Seq = 1

	_, err = domain.Resubmit(c, last, "sub-carolina", domain.ResubmissionRequest{ReviewSeq: 1}, nil, reenviadoEn)
	assert.Equal(t, map[string]string{"estimated_amount_minor": domain.CodeRequired}, fieldsOf(t, err))

	r, err := domain.Resubmit(c, last, "sub-carolina",
		domain.ResubmissionRequest{ReviewSeq: 1, EstimatedAmountMinor: text("150000000")}, nil, reenviadoEn)
	require.NoError(t, err)
	assert.Equal(t, []domain.FactChange{{Fact: domain.FactEstimatedAmount, Before: "", After: "150000000"}}, r.Facts)
}

func TestOnlyAReturnedFilingAnsweringItsLastReturnIsSentAgain(t *testing.T) {
	c, last := devuelta()
	in, uploaded := corrected()

	_, err := domain.Resubmit(withStatus(c, domain.StatusActive), last, "sub-carolina", in, uploaded, reenviadoEn)
	assert.ErrorIs(t, err, domain.ErrNotReturned)

	in.ReviewSeq = 2
	_, err = domain.Resubmit(c, last, "sub-carolina", in, uploaded, reenviadoEn)
	assert.ErrorIs(t, err, domain.ErrStaleReturn)
}

func TestAFilingOfThePhoneGetsItsSupportsFromThePortal(t *testing.T) {
	phone := domain.ClaimRecord{Claim: domain.Claim{ID: "cl-2", FilingNumber: "SIN-2026-00042", Subject: "sub-carolina",
		IncidentType: domain.IncidentCollision, Status: domain.StatusReturned, OccurredAt: radicadoEn.Add(-time.Hour),
		Place: "Autopista Norte con calle 170", Description: "Choque leve al salir del parqueadero.", FiledAt: radicadoEn,
		Evidence: make([]domain.Evidence, 4)}, Channel: domain.ChannelMobile}
	ret := domain.ReturnRequest{ReasonCode: domain.ReasonIllegibleOrIncompleteSupports, Message: "Las fotos están oscuras.",
		Supports: []domain.SupportVerdict{{RequirementCode: domain.SupportVehiclePhotos, Verdict: domain.VerdictIllegible},
			{RequirementCode: domain.SupportDriverDocument, Verdict: domain.VerdictMissing}}}
	last, err := domain.ReturnFiling(withStatus(phone, domain.StatusActive), juan, ret, revisadoEn)
	require.NoError(t, err)
	last.Seq = 1
	uploaded := map[string]domain.SupportFile{supportID(20): file(20, "sub-carolina", domain.FormatPDF)}
	in := domain.ResubmissionRequest{ReviewSeq: 1, Supports: []domain.SupportGroup{
		{RequirementCode: domain.SupportDriverDocument, SupportIDs: []string{supportID(20)}}}}

	_, err = domain.Resubmit(phone, last, "sub-carolina", in, uploaded, reenviadoEn)
	assert.Equal(t, map[string]string{"supports.vehicle_photos": domain.CodeNotReplaced}, fieldsOf(t, err),
		"the photographs of the phone are replaced from the portal")

	photos := domain.SupportGroup{RequirementCode: domain.SupportVehiclePhotos}
	for n := 21; n <= 24; n++ {
		photos.SupportIDs = append(photos.SupportIDs, supportID(n))
		uploaded[supportID(n)] = file(n, "sub-carolina", domain.FormatJPEG)
	}
	in.Supports = append(in.Supports, photos)
	r, err := domain.Resubmit(phone, last, "sub-carolina", in, uploaded, reenviadoEn)

	require.NoError(t, err)
	checklist := domain.Checklist(r.Claim)
	assert.Equal(t, 4, checklist[0].Files, "the new photos replace the ones of the phone")
	assert.Equal(t, domain.SupportVehiclePhotos, checklist[0].RequirementCode)
	assert.Nil(t, r.Claim.Policy, "a filing of the phone has no policy copied")
}

func TestTheTimeOfTheIncidentIsCheckedAsInTheFiling(t *testing.T) {
	c, last := devuelta()
	c.Policy = nil // a filing of the phone: only the age of the incident counts
	for value, code := range map[string]string{
		"":                     domain.CodeRequired,
		"14/09/2026 18:40":     domain.CodeInvalidFormat,
		"2020-09-14T18:40:00Z": domain.CodeOutOfRange,
	} {
		in, uploaded := corrected()
		in.OccurredAt = text(value)

		_, err := domain.Resubmit(c, last, "sub-carolina", in, uploaded, reenviadoEn)

		assert.Equal(t, map[string]string{"occurred_at": code}, fieldsOf(t, err), value)
	}
	in, uploaded := corrected()
	in.OccurredAt = text(c.OccurredAt.Format(time.RFC3339))
	r, err := domain.Resubmit(c, last, "sub-carolina", in, uploaded, reenviadoEn)
	require.NoError(t, err)
	assert.Empty(t, r.Facts, "the same time is not a change")
}
