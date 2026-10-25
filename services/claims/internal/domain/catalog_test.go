package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// The catalog of claim types and supports of the web filing (HU-W2.1-01,
// decision D4).

func TestTheCatalogV1HasTheTypesOfTheMockup(t *testing.T) {
	c := domain.CatalogV1()
	assert.Equal(t, "claim-catalog-v1", c.Version)

	lines := map[domain.IncidentType]domain.PolicyLine{}
	for _, o := range c.Incidents {
		lines[o.Type] = o.Line
	}
	assert.Equal(t, map[domain.IncidentType]domain.PolicyLine{
		domain.IncidentCollision:        domain.LineAuto,
		domain.IncidentTheft:            domain.LineAuto,
		domain.IncidentThirdPartyDamage: domain.LineAuto,
		domain.IncidentGlass:            domain.LineAuto,
		domain.IncidentOther:            domain.LineAuto,
		domain.IncidentWaterDamage:      domain.LineHome,
	}, lines)
}

func TestTheRequirementsOfEachType(t *testing.T) {
	type req struct {
		required bool
		min, max int
		formats  int
	}
	want := map[domain.IncidentType]map[string]req{
		domain.IncidentCollision: {
			"vehicle_photos": {true, 4, 10, 2}, "driver_document": {true, 1, 2, 3}, "traffic_report": {true, 1, 3, 3},
		},
		domain.IncidentTheft: {
			"police_report": {true, 1, 3, 3}, "driver_document": {true, 1, 2, 3}, "vehicle_photos": {false, 0, 10, 2},
		},
		domain.IncidentThirdPartyDamage: {
			"vehicle_photos": {true, 4, 10, 2}, "driver_document": {true, 1, 2, 3}, "traffic_report": {true, 1, 3, 3},
		},
		domain.IncidentGlass:       {"glass_photos": {true, 2, 6, 2}, "driver_document": {false, 0, 2, 3}},
		domain.IncidentOther:       {"supporting_document": {true, 1, 5, 3}},
		domain.IncidentWaterDamage: {"damage_photos": {true, 2, 10, 2}, "repair_quote": {true, 1, 3, 3}},
	}
	c := domain.CatalogV1()
	for incident, reqs := range want {
		option, ok := c.Incident(incident)
		require.True(t, ok, incident)
		assert.NotEmpty(t, option.Label)
		got := map[string]req{}
		for _, r := range option.Requirements {
			assert.NotEmpty(t, r.Label)
			got[r.Code] = req{r.Required, r.MinFiles, r.MaxFiles, len(r.Formats)}
		}
		assert.Equal(t, reqs, got, incident)
	}
}

func TestPhotosTakeOnlyImagesAndDocumentsTakePDFToo(t *testing.T) {
	collision, _ := domain.CatalogV1().Incident(domain.IncidentCollision)
	photos, _ := collision.Requirement(domain.SupportVehiclePhotos)
	report, _ := collision.Requirement(domain.SupportTrafficReport)

	assert.True(t, photos.Accepts(domain.FormatJPEG))
	assert.True(t, photos.Accepts(domain.FormatPNG))
	assert.False(t, photos.Accepts(domain.FormatPDF))
	for _, f := range []domain.FileFormat{domain.FormatPDF, domain.FormatJPEG, domain.FormatPNG} {
		assert.True(t, report.Accepts(f), f)
	}

	_, ok := collision.Requirement(domain.SupportPoliceReport)
	assert.False(t, ok, "the police report does not apply to a collision")
}

func TestAnUnknownTypeIsNotInTheCatalog(t *testing.T) {
	_, ok := domain.CatalogV1().Incident("flight_delay")
	assert.False(t, ok)
}

func TestEveryCallReturnsAFreshCatalog(t *testing.T) {
	a := domain.CatalogV1()
	a.Incidents[0].Requirements[0].MinFiles = 99
	a.Incidents[0].Requirements[0].Formats[0] = domain.FormatPDF

	b := domain.CatalogV1()
	assert.Equal(t, 4, b.Incidents[0].Requirements[0].MinFiles)
	assert.Equal(t, domain.FormatJPEG, b.Incidents[0].Requirements[0].Formats[0])
}

func TestTheOptionsOfAPolicyAreTheCoveredTypesOfItsLine(t *testing.T) {
	c := domain.CatalogV1()

	var types []domain.IncidentType
	for _, o := range c.OptionsFor(autoPolicy()) {
		types = append(types, o.Type)
	}
	assert.Equal(t, []domain.IncidentType{
		domain.IncidentCollision, domain.IncidentTheft, domain.IncidentThirdPartyDamage, domain.IncidentOther,
	}, types, "in the order of the catalog, without glass")

	travel := domain.Policy{Line: domain.LineTravel, CoveredIncidents: []domain.IncidentType{"flight_delay"}}
	assert.Empty(t, c.OptionsFor(travel))
}

func TestTheContentTypeOfEachFormat(t *testing.T) {
	for format, contentType := range map[domain.FileFormat]string{
		domain.FormatJPEG: "image/jpeg", domain.FormatPNG: "image/png", domain.FormatPDF: "application/pdf",
	} {
		assert.Equal(t, contentType, format.ContentType())
		back, ok := domain.FormatOf(contentType)
		assert.True(t, ok)
		assert.Equal(t, format, back)
	}
	assert.Empty(t, domain.FileFormat("gif").ContentType())
	_, ok := domain.FormatOf("image/gif")
	assert.False(t, ok)
}
