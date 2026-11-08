package assessors

import (
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// lineLabels are the names of the lines the provider reads.
var lineLabels = map[domain.PolicyLine]string{domain.LineAuto: "Autos", domain.LineHome: "Hogar"}

// ProviderNotice is the e-mail that gives the assigned provider what it needs
// to go to the incident (criterion 5, assumption S11): the filing number, the
// claim type, the coordinates with their precision, the address and a link to
// the map. It carries nothing of the insured.
func ProviderNotice(c domain.ClaimRecord, a domain.Assignment) (subject, body string) {
	line := string(c.IncidentType)
	if option, ok := domain.CatalogFor(c.CatalogVersion).Incident(c.IncidentType); ok {
		line = lineLabels[option.Line]
	}
	at := a.Location.Point
	precision := ""
	if a.Location.AccuracyM != nil {
		precision = fmt.Sprintf(" (precisión %.0f m)", *a.Location.AccuracyM)
	}
	address := a.Location.Address
	if address == "" {
		address = "(no registrada)"
	}
	subject = "Asignación de siniestro " + c.FilingNumber
	body = fmt.Sprintf("Solventa le asignó la atención en sitio del siniestro %s.\n\n"+
		"Tipo: %s · %s\nUbicación: %.6f, %.6f%s\nDirección: %s\n"+
		"Mapa: https://www.google.com/maps/search/?api=1&query=%.6f,%.6f\n",
		c.FilingNumber, line, c.IncidentLabel(), at.Latitude, at.Longitude, precision, address,
		at.Latitude, at.Longitude)
	return subject, body
}
