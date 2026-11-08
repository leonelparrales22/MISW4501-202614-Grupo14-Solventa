package assessors_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assessors"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// The e-mail to the assigned provider (HU-W2.3-01, criterion 5): what it
// needs to go to the incident and nothing of the insured.

func TestTheNoticeCarriesTheLocationOfTheApp(t *testing.T) {
	c := domain.ClaimRecord{Claim: domain.Claim{ID: "cl-1", FilingNumber: "SIN-2026-01655",
		IncidentType: domain.IncidentWaterDamage, Subject: "sub-carolina", Place: "Carrera 15 # 93-40"},
		CatalogVersion: domain.CatalogVersionV1}
	accuracy := 8.0
	a := domain.Assignment{ClaimID: "cl-1", Location: domain.IncidentLocation{Source: domain.LocationFromApp,
		Point: incident, AccuracyM: &accuracy}}

	subject, body := assessors.ProviderNotice(c, a)
	assert.Equal(t, "Asignación de siniestro SIN-2026-01655", subject)
	assert.Equal(t, "Solventa le asignó la atención en sitio del siniestro SIN-2026-01655.\n\n"+
		"Tipo: Hogar · Daño por agua\n"+
		"Ubicación: 4.677100, -74.048300 (precisión 8 m)\n"+
		"Dirección: (no registrada)\n"+
		"Mapa: https://www.google.com/maps/search/?api=1&query=4.677100,-74.048300\n", body)
	assert.NotContains(t, body, "sub-carolina")
}

func TestTheNoticeCarriesTheAddressOfTheAnalyst(t *testing.T) {
	c := domain.ClaimRecord{Claim: domain.Claim{FilingNumber: "SIN-2026-01656", IncidentType: domain.IncidentGlass},
		CatalogVersion: domain.CatalogVersionV1}
	a := domain.Assignment{Location: domain.IncidentLocation{Source: domain.LocationFromAnalyst, Point: incident,
		Address: "Carrera 15 # 93-40, apto 402, Bogotá"}}
	_, body := assessors.ProviderNotice(c, a)
	assert.Contains(t, body, "Tipo: Autos · Cristales\n")
	assert.Contains(t, body, "Ubicación: 4.677100, -74.048300\n")
	assert.Contains(t, body, "Dirección: Carrera 15 # 93-40, apto 402, Bogotá\n")

	c.IncidentType = "fire"
	_, body = assessors.ProviderNotice(c, a)
	assert.Contains(t, body, "Tipo: fire · fire\n", "a type the catalog does not know is named by its code")
}
