package httpadapter

import (
	"net/http"
	"strconv"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Headers that tell where a claim read came from (HA-L02), for monitoring.
const (
	headerStale      = "X-Stale"
	headerDataSource = "X-Data-Source"
)

// freshnessBody is what every claim read of the insured carries (HA-L02),
// embedded as a pointer so the bodies that are not a read leave it out:
// Stale is true when the database did not answer in time and the read came
// from the read cache, and AsOf is when the data was read from the database,
// which the app shows as "Actualizado hoy a las 14:30" or "Caché 14:30".
type freshnessBody struct {
	Stale bool      `json:"stale"`
	AsOf  time.Time `json:"as_of"`
}

// trackFreshness returns the request whose reads report where they came from.
func trackFreshness(req *http.Request) (*http.Request, *ports.Freshness) {
	ctx, f := ports.TrackFreshness(req.Context())
	return req.WithContext(ctx), f
}

// freshnessOf sets the headers of the read and returns its body.
func freshnessOf(w http.ResponseWriter, f *ports.Freshness) *freshnessBody {
	stale, asOf := f.Stale()
	source := "database"
	if stale {
		source = "cache"
	} else {
		asOf = time.Now()
	}
	w.Header().Set(headerStale, strconv.FormatBool(stale))
	w.Header().Set(headerDataSource, source)
	return &freshnessBody{Stale: stale, AsOf: asOf.UTC()}
}
