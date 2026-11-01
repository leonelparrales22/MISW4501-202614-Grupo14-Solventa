package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// What the confirmation and the receipt show of a claim (HU-W2.1-01
// criterion 4).

func webClaim(t *testing.T) domain.ClaimRecord {
	t.Helper()
	u := newUploads()
	r := validWebFiling(u)
	r.Supports[0].SupportIDs = u.add(insured, domain.FormatJPEG, 6)
	c, err := validateWeb(r, u)
	require.NoError(t, err)
	c.FilingNumber = "SIN-2026-01712"
	c.FiledAt = time.Date(2026, 9, 15, 0, 5, 0, 0, time.UTC)
	p := autoPolicy()
	c.Policy = &p
	return c
}

func TestTheSummaryCountsTheRequiredSupportsAndTheFiles(t *testing.T) {
	s := webClaim(t).SupportSummary()

	assert.Equal(t, 3, s.RequiredTotal)
	assert.Equal(t, 3, s.RequiredAttached)
	assert.Equal(t, 8, s.Files, "3 of 3 required, 8 files")
	require.Len(t, s.Items, 3)
	assert.Equal(t, "vehicle_photos", s.Items[0].RequirementCode)
	assert.Equal(t, "Fotos del vehículo", s.Items[0].Label)
	assert.Len(t, s.Items[0].Files, 6)
}

func TestAnOptionalSupportLeftOutIsNotInTheSummary(t *testing.T) {
	u := newUploads()
	r := validWebFiling(u)
	r.IncidentType = "glass"
	r.Supports = []domain.SupportGroup{{RequirementCode: "glass_photos", SupportIDs: u.add(insured, domain.FormatPNG, 2)}}
	c, err := validateWeb(r, u)
	require.NoError(t, err)

	s := c.SupportSummary()

	assert.Equal(t, 1, s.RequiredTotal)
	assert.Equal(t, 1, s.RequiredAttached)
	assert.Len(t, s.Items, 1)
}

func TestAMobileClaimHasNoSupportSummary(t *testing.T) {
	c := domain.ClaimRecord{Claim: domain.Claim{IncidentType: domain.IncidentCollision}, Channel: domain.ChannelMobile}

	assert.Equal(t, domain.SupportSummary{}, c.SupportSummary())
	assert.Equal(t, "Colisión con daños al vehículo", c.IncidentLabel(), "the labels come from the current catalog")
}

func TestAnUnknownTypeIsLabeledByItsCode(t *testing.T) {
	c := domain.ClaimRecord{Claim: domain.Claim{IncidentType: "flight_delay"}}
	assert.Equal(t, "flight_delay", c.IncidentLabel())
}

func TestTheReceiptHasWhatTheInsuredQuotes(t *testing.T) {
	c := webClaim(t)

	r := c.Receipt()

	assert.Equal(t, "SIN-2026-01712", r.FilingNumber)
	assert.Equal(t, domain.StatusActive, r.Status)
	assert.Equal(t, c.FiledAt, r.FiledAt)
	assert.Equal(t, "AU-2025-118204", r.PolicyID)
	assert.Equal(t, "Autos", r.Product)
	assert.Equal(t, "KJT-482", r.InsuredObject)
	assert.Equal(t, "Colisión con daños al vehículo", r.IncidentLabel)
	assert.Equal(t, c.OccurredAt, r.OccurredAt)
	assert.Equal(t, c.Place, r.Place)
	assert.Equal(t, 8, r.Supports.Files)

	c.Policy = nil
	assert.Empty(t, c.Receipt().Product, "a mobile claim has no copy of the policy")
}

func TestColombiaIsFiveHoursBehindUTC(t *testing.T) {
	at := time.Date(2026, 10, 23, 5, 0, 0, 0, time.UTC).In(domain.ColombiaTime)
	assert.Equal(t, 0, at.Hour())
	assert.Equal(t, 23, at.Day())
}
