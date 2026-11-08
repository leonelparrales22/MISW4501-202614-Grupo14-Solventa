package httpadapter_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// The manual security review of HU-W2.3-01 (OBJ 10), kept as tests: what
// cannot be an id never reaches the service, and neither does a number JSON
// cannot carry.

func TestAPathThatIsNotAnIdNeverReachesTheService(t *testing.T) {
	long := "prv-" + strings.Repeat("a", 41)
	for path, method := range map[string]string{
		"/v1/claims/../assignment":                                  http.MethodGet,
		"/v1/claims/..%2F..%2Fproviders/assignment":                 http.MethodGet,
		"/v1/claims/" + claimID + "0/assignment":                    http.MethodGet,
		"/v1/claims/" + strings.ToUpper(claimID) + "/assignment":    http.MethodGet,
		"/v1/claims/" + claimID + "%00/incident-location":           http.MethodPost,
		"/v1/claims/" + strings.Repeat("a", 10<<10) + "/assignment": http.MethodPost,
		"/v1/claims/providers/..%2Fprv-servicios-hogar-norte":       http.MethodGet,
		"/v1/claims/providers/" + long:                              http.MethodGet,
		"/v1/claims/providers/" + long + "/profile":                 http.MethodPost,
		"/v1/claims/providers/prv-abc%27--/availability":            http.MethodPost,
	} {
		a, r := &fakeAssigner{view: proposed()}, &fakeRegistry{provider: hogarNorte()}
		status, _, _ := send(assignmentAndRegistry(a, r), method, path, "operations", `{}`)
		assert.Equal(t, http.StatusNotFound, status, "%.80s", path)
		assert.Zero(t, a.calls+r.calls, "%.80s reached the service", path)
	}
}

func TestABatchOfTenKilobytesIsRefusedWithoutReadingAClaim(t *testing.T) {
	for _, path := range []string{assignmentBatch(strings.Repeat("a", 10<<10)),
		assignmentBatch(claimID, strings.Repeat("0", 10<<10))} {
		a := &fakeAssigner{}
		status, out, _ := send(assignmentAndRegistry(a, &fakeRegistry{}), http.MethodGet, path, "operations", "")
		assert.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, "invalid_request", out["error"])
		assert.Zero(t, a.calls)
	}
}

func TestACoordinateThatIsNotAFiniteNumberIsRefused(t *testing.T) {
	for _, coordinate := range []string{"NaN", "Infinity", "-Infinity", "1e400", `"4.6771"`} {
		location := `{"address": "Carrera 15 # 93-40", "latitude": ` + coordinate + `, "longitude": -74.0483}`
		profile := `{"name": "Servicios Hogar Norte", "specialties": ["plumbing"], "latitude": ` + coordinate +
			`, "longitude": -74.0364, "address": "Calle 116 # 9-72", "contact_email": "a@example.com"}`
		for path, body := range map[string]string{
			"/v1/claims/" + claimID + "/incident-location":           location,
			"/v1/claims/providers":                                   profile,
			"/v1/claims/providers/prv-servicios-hogar-norte/profile": profile,
		} {
			a, r := &fakeAssigner{view: proposed()}, &fakeRegistry{provider: hogarNorte()}
			status, out, _ := send(assignmentAndRegistry(a, r), http.MethodPost, path, "operations", body)
			assert.Equal(t, http.StatusBadRequest, status, "%s with %s", path, coordinate)
			assert.Equal(t, "invalid_request", out["error"], "%s with %s", path, coordinate)
			assert.Zero(t, a.calls+r.calls, "%s with %s reached the service", path, coordinate)
		}
	}
}

func TestABodyOverTheLimitIsRefused(t *testing.T) {
	padding := strings.Repeat(" ", 1<<14)
	for path, body := range map[string]string{
		"/v1/claims/" + claimID + "/incident-location": `{"address": "Carrera 15 # 93-40",` + padding +
			`"latitude": 4.6771, "longitude": -74.0483}`,
		"/v1/claims/" + claimID + "/assignment": `{"provider_id": "prv-abc",` + padding + `"search_seq": 1}`,
		"/v1/claims/providers":                  `{"name": "` + strings.Repeat("a", 1<<14) + `"}`,
	} {
		a, r := &fakeAssigner{view: proposed()}, &fakeRegistry{provider: hogarNorte()}
		status, _, _ := send(assignmentAndRegistry(a, r), http.MethodPost, path, "operations", body)
		assert.Equal(t, http.StatusBadRequest, status, path)
		assert.Zero(t, a.calls+r.calls, path)
	}
}
