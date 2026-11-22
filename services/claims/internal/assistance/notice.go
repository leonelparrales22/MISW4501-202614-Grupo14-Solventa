package assistance

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// needLabels are the names of the needs the provider reads.
var needLabels = map[domain.AssistanceNeed]string{
	domain.NeedTowing:            "Grúa",
	domain.NeedBattery:           "Batería",
	domain.NeedFlatTire:          "Cambio de llanta",
	domain.NeedFuel:              "Combustible",
	domain.NeedVehicleLockout:    "Llaves dentro del vehículo",
	domain.NeedPlumbingEmergency: "Emergencia de plomería",
}

// lineLabels are the names of the lines the provider reads.
var lineLabels = map[domain.PolicyLine]string{domain.LineAuto: "Autos", domain.LineHome: "Hogar",
	domain.LineTravel: "Viaje"}

func labelOf[K comparable](labels map[K]string, k K, fallback string) string {
	if l, ok := labels[k]; ok {
		return l
	}
	return fallback
}

// DispatchNotice is the e-mail that gives the dispatched provider what it
// needs to reach the insured (criterion 3, assumption S13): the number of the
// request, the need, the line and the insured object, the location with its
// accuracy or the address given by voice, a link to the map and the UTC time
// of the request. It carries no name, document nor phone of the insured.
func DispatchNotice(r domain.AssistanceRequest, d domain.AssistanceDispatch) (subject, body string) {
	need := labelOf(needLabels, d.Need, string(d.Need))
	line := labelOf(lineLabels, r.Policy.Line, string(r.Policy.Line))
	if object := strings.TrimSpace(r.Policy.InsuredObject); object != "" {
		line += " · " + object
	}

	at := d.Location.Point
	lat, lon := strconv.FormatFloat(at.Latitude, 'f', -1, 64), strconv.FormatFloat(at.Longitude, 'f', -1, 64)
	origin := "indicada por voz"
	if d.Location.Source == domain.SharedFromApp {
		origin = "compartida desde el teléfono"
		if d.Location.AccuracyM != nil {
			origin = fmt.Sprintf("precisión %.0f m, %s", *d.Location.AccuracyM, origin)
		}
	}
	address := d.Location.Address
	if address == "" && d.Location.Place != nil {
		address = *d.Location.Place
	}
	if address == "" {
		address = "(no registrada)"
	}

	subject = "Asistencia " + r.Number + " · " + need
	body = fmt.Sprintf("Solventa le asigna la asistencia %s.\n\n"+
		"Necesidad: %s\nLínea: %s\nUbicación: %s, %s (%s)\nDirección: %s\n"+
		"Mapa: https://www.google.com/maps/search/?api=1&query=%s,%s\nSolicitada: %s UTC\n",
		r.Number, need, line, lat, lon, origin, address, lat, lon, r.RequestedAt.UTC().Format("2006-01-02 15:04"))
	return subject, body
}
