package domain

import "math"

// Coordinates is a point on the Earth in decimal degrees (WGS 84), as the
// phone reports it and the map of the portal places it.
type Coordinates struct {
	Latitude  float64
	Longitude float64
}

// Valid reports whether the point is a real place: finite and in range.
func (c Coordinates) Valid() bool {
	return validLatitude(c.Latitude) && validLongitude(c.Longitude)
}

func validLatitude(v float64) bool  { return !math.IsNaN(v) && v >= -90 && v <= 90 }
func validLongitude(v float64) bool { return !math.IsNaN(v) && v >= -180 && v <= 180 }

// EarthRadiusKm is the mean radius of the Earth (IUGG).
const EarthRadiusKm = 6371.0088

// Distance is the straight-line (great-circle) distance between two points,
// in km, by the haversine formula (HU-W2.3-01, decision D7). The assignment
// compares it unrounded; RoundKm is only for showing it.
func Distance(a, b Coordinates) float64 {
	lat1, lat2 := radians(a.Latitude), radians(b.Latitude)
	dLat, dLon := lat2-lat1, radians(b.Longitude-a.Longitude)
	h := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1)*math.Cos(lat2)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * EarthRadiusKm * math.Asin(math.Min(1, math.Sqrt(h)))
}

func radians(degrees float64) float64 { return degrees * math.Pi / 180 }

// RoundKm rounds a distance to 0.1 km, as the analyst reads it.
func RoundKm(km float64) float64 { return math.Round(km*10) / 10 }
