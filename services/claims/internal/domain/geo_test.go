package domain_test

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// The straight-line distance of the assignment (HU-W2.3-01, decision D7).

// kmPerDegree is the length of one degree of a great circle.
const kmPerDegree = 2 * math.Pi * domain.EarthRadiusKm / 360

// incident is the place of the claim of the mockup (Bogotá, apartment 402).
var incident = domain.Coordinates{Latitude: 4.6771, Longitude: -74.0483}

// north is the point km kilometers north of the incident.
func north(km float64) domain.Coordinates {
	return domain.Coordinates{Latitude: incident.Latitude + km/kmPerDegree, Longitude: incident.Longitude}
}

func TestTheDistanceBetweenKnownPoints(t *testing.T) {
	origin := domain.Coordinates{}
	assert.Zero(t, domain.Distance(incident, incident), "the same point")
	assert.InDelta(t, kmPerDegree, domain.Distance(origin, domain.Coordinates{Longitude: 1}), 1e-9,
		"one degree along the equator")
	assert.InDelta(t, kmPerDegree, domain.Distance(origin, domain.Coordinates{Latitude: 1}), 1e-9,
		"one degree along a meridian")
	assert.InDelta(t, math.Pi*domain.EarthRadiusKm, domain.Distance(origin, domain.Coordinates{Longitude: 180}), 1e-6,
		"the antipodes")
	assert.InDelta(t, 2.3, domain.Distance(incident, north(2.3)), 1e-9)
	// Plaza de Bolívar to Parque de la 93, in Bogotá: about 9.3 km.
	bolivar := domain.Coordinates{Latitude: 4.5981, Longitude: -74.0760}
	parque93 := domain.Coordinates{Latitude: 4.6767, Longitude: -74.0484}
	assert.InDelta(t, 9.26, domain.Distance(bolivar, parque93), 0.05)
}

func TestTheDistanceIsSymmetric(t *testing.T) {
	a, b := incident, domain.Coordinates{Latitude: -33.4489, Longitude: -70.6693}
	assert.Equal(t, domain.Distance(a, b), domain.Distance(b, a))
}

func TestTheDistanceIsRoundedToATenthOfAKilometer(t *testing.T) {
	assert.Equal(t, 2.3, domain.RoundKm(2.34999))
	assert.Equal(t, 2.4, domain.RoundKm(2.35))
	assert.Equal(t, 0.0, domain.RoundKm(0.04))
	assert.Equal(t, 20.0, domain.RoundKm(20.04))
}

func TestACoordinateIsValidOnlyWithinItsRange(t *testing.T) {
	for _, c := range []domain.Coordinates{{}, {Latitude: 90, Longitude: 180}, {Latitude: -90, Longitude: -180}, incident} {
		assert.True(t, c.Valid(), c)
	}
	for _, c := range []domain.Coordinates{
		{Latitude: 90.0001}, {Latitude: -90.0001}, {Longitude: 180.0001}, {Longitude: -180.0001},
		{Latitude: math.NaN()}, {Longitude: math.Inf(1)},
	} {
		assert.False(t, c.Valid(), c)
	}
}
