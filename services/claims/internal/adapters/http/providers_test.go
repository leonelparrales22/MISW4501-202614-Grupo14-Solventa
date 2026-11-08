package httpadapter_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The routes of the registry of providers (HU-W2.3-01, criterion 2).

type fakeRegistry struct {
	provider domain.Provider
	list     []domain.Provider
	err      error
	calls    int

	gotID, gotBy    string
	gotFilter       ports.ProviderFilter
	gotProfile      domain.ProviderProfile
	gotAvailability domain.AvailabilityRequest
}

func (f *fakeRegistry) List(_ context.Context, filter ports.ProviderFilter) ([]domain.Provider, error) {
	f.calls++
	f.gotFilter = filter
	return f.list, f.err
}

func (f *fakeRegistry) Get(_ context.Context, id string) (domain.Provider, error) {
	f.calls++
	f.gotID = id
	return f.provider, f.err
}

func (f *fakeRegistry) Create(_ context.Context, in domain.ProviderProfile, by string) (domain.Provider, error) {
	f.calls++
	f.gotProfile, f.gotBy = in, by
	return f.provider, f.err
}

func (f *fakeRegistry) UpdateProfile(_ context.Context, id string, in domain.ProviderProfile, by string) (domain.Provider, error) {
	f.calls++
	f.gotID, f.gotProfile, f.gotBy = id, in, by
	return f.provider, f.err
}

func (f *fakeRegistry) SetAvailability(_ context.Context, id string, in domain.AvailabilityRequest,
	by string) (domain.Provider, error) {
	f.calls++
	f.gotID, f.gotAvailability, f.gotBy = id, in, by
	return f.provider, f.err
}

func registryRouter(f *fakeRegistry) http.Handler {
	return httpadapter.NewRouter(httpadapter.WithProviders(f, analysts))
}

func hogarNorte() domain.Provider {
	phone, claim := "+576015550102", claimID
	until := time.Date(2026, 9, 22, 0, 0, 0, 0, domain.ColombiaTime)
	return domain.Provider{ID: "prv-servicios-hogar-norte", Name: "Servicios Hogar Norte",
		Specialties: []domain.Specialty{domain.SpecialtyPlumbing, domain.SpecialtyDampProofing},
		Location:    domain.Coordinates{Latitude: 4.694, Longitude: -74.0364}, Address: "Calle 116 # 9-72, Bogotá",
		ContactEmail: "despachos@hogarnorte.example.com", ContactPhone: &phone,
		Availability: domain.AvailabilityBusy, UnavailableUntil: &until, BusyClaimID: &claim,
		UpdatedBy: domain.ReferenceDataAuthor, UpdatedAt: time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)}
}

const hogarNorteJSON = `{"id": "prv-servicios-hogar-norte", "name": "Servicios Hogar Norte",
	"specialties": ["plumbing", "damp_proofing"], "latitude": 4.694, "longitude": -74.0364,
	"address": "Calle 116 # 9-72, Bogotá", "contact_email": "despachos@hogarnorte.example.com",
	"contact_phone": "+576015550102", "availability": "busy", "unavailable_until": "2026-09-22",
	"busy_claim_id": "` + claimID + `", "updated_by": "reference-data", "updated_at": "2026-11-07T00:00:00Z"}`

const profileJSON = `{"name": "Servicios Hogar Norte", "specialties": ["plumbing", "damp_proofing"],
	"latitude": 4.694, "longitude": -74.0364, "address": "Calle 116 # 9-72, Bogotá",
	"contact_email": "despachos@hogarnorte.example.com", "contact_phone": "+576015550102"}`

func TestTheRegistryIsListedWithItsFilters(t *testing.T) {
	f := &fakeRegistry{list: []domain.Provider{hogarNorte()}}
	rec := do(registryRouter(f), http.MethodGet, "/v1/claims/providers?specialty=plumbing&availability=available",
		"operations", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.JSONEq(t, `{"items": [`+hogarNorteJSON+`]}`, rec.Body.String())
	assert.Equal(t, ports.ProviderFilter{Specialty: domain.SpecialtyPlumbing, Availability: domain.AvailabilityAvailable},
		f.gotFilter)

	rec = do(registryRouter(&fakeRegistry{}), http.MethodGet, "/v1/claims/providers", "operations", nil, nil)
	assert.JSONEq(t, `{"items": []}`, rec.Body.String())

	for _, query := range []string{"?specialty=towing", "?availability=maybe"} {
		f := &fakeRegistry{}
		rec := do(registryRouter(f), http.MethodGet, "/v1/claims/providers"+query, "operations", nil, nil)
		assert.Equal(t, http.StatusBadRequest, rec.Code, query)
		assert.Zero(t, f.calls, query)
	}
	rec = do(registryRouter(&fakeRegistry{err: errors.New("down")}), http.MethodGet, "/v1/claims/providers",
		"operations", nil, nil)
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

func TestOneProviderOfTheRegistry(t *testing.T) {
	f := &fakeRegistry{provider: hogarNorte()}
	rec := do(registryRouter(f), http.MethodGet, "/v1/claims/providers/prv-servicios-hogar-norte", "operations", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, hogarNorteJSON, rec.Body.String())
	assert.Equal(t, "prv-servicios-hogar-norte", f.gotID)

	rec = do(registryRouter(&fakeRegistry{err: ports.ErrProviderNotFound}), http.MethodGet,
		"/v1/claims/providers/prv-nadie", "operations", nil, nil)
	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.Equal(t, "provider_not_found", decode(t, rec)["error"])

	for path, method := range map[string]string{"/v1/claims/providers/PRV-1": http.MethodGet,
		"/v1/claims/providers/prv-a": http.MethodGet, "/v1/claims/providers/x/profile": http.MethodPost,
		"/v1/claims/providers/prv_1/availability": http.MethodPost} {
		f := &fakeRegistry{}
		status, out, _ := send(registryRouter(f), method, path, "operations", `{}`)
		assert.Equal(t, http.StatusNotFound, status, path)
		assert.Equal(t, "provider_not_found", out["error"], path)
		assert.Zero(t, f.calls, path)
	}
}

func TestOperationsRegistersAProviderOverHTTP(t *testing.T) {
	f := &fakeRegistry{provider: hogarNorte()}
	status, out, header := send(registryRouter(f), http.MethodPost, "/v1/claims/providers", "operations", profileJSON)
	require.Equal(t, http.StatusCreated, status)
	assert.Equal(t, "no-store", header.Get("Cache-Control"))
	assert.Equal(t, "prv-servicios-hogar-norte", out["id"])
	assert.Equal(t, "andres.ruiz@solventa.example", f.gotBy)
	assert.Equal(t, "Servicios Hogar Norte", f.gotProfile.Name)
	assert.Equal(t, []domain.Specialty{domain.SpecialtyPlumbing, domain.SpecialtyDampProofing}, f.gotProfile.Specialties)
	assert.Equal(t, 4.694, *f.gotProfile.Latitude)
	assert.Equal(t, "+576015550102", *f.gotProfile.ContactPhone)

	f = &fakeRegistry{err: &domain.ValidationError{Fields: []domain.FieldError{{Field: "name", Code: domain.CodeRequired}}}}
	status, out, _ = send(registryRouter(f), http.MethodPost, "/v1/claims/providers", "operations", `{}`)
	assert.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "validation_failed", out["error"])
	assert.Nil(t, f.gotProfile.Latitude, "a missing coordinate is told from zero")

	for _, body := range []string{`{"name": 1}`, `{"id": "prv-propio"}`, `[]`, `{"specialties": "plumbing"}`} {
		f := &fakeRegistry{}
		status, _, _ := send(registryRouter(f), http.MethodPost, "/v1/claims/providers", "operations", body)
		assert.Equal(t, http.StatusBadRequest, status, body)
		assert.Zero(t, f.calls, body)
	}
}

func TestOperationsChangesAProviderOverHTTP(t *testing.T) {
	f := &fakeRegistry{provider: hogarNorte()}
	status, _, _ := send(registryRouter(f), http.MethodPost, "/v1/claims/providers/prv-servicios-hogar-norte/profile",
		"operations", profileJSON)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, "prv-servicios-hogar-norte", f.gotID)
	assert.Equal(t, "Servicios Hogar Norte", f.gotProfile.Name)
	assert.Equal(t, "andres.ruiz@solventa.example", f.gotBy)

	f = &fakeRegistry{provider: hogarNorte()}
	status, _, _ = send(registryRouter(f), http.MethodPost,
		"/v1/claims/providers/prv-servicios-hogar-norte/availability", "operations",
		`{"availability": "unavailable", "unavailable_until": "2026-09-22"}`)
	require.Equal(t, http.StatusOK, status)
	assert.Equal(t, domain.AvailabilityUnavailable, f.gotAvailability.Availability)
	assert.Equal(t, "2026-09-22", *f.gotAvailability.UnavailableUntil)

	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{ports.ErrProviderNotFound, http.StatusNotFound, "provider_not_found"},
		{&domain.ValidationError{Fields: []domain.FieldError{{Field: "availability", Code: domain.CodeInvalidFormat}}},
			http.StatusUnprocessableEntity, "validation_failed"},
		{errors.New("down"), http.StatusServiceUnavailable, "claims_unavailable"},
	} {
		for _, path := range []string{"/profile", "/availability"} {
			status, out, _ := send(registryRouter(&fakeRegistry{err: c.err}), http.MethodPost,
				"/v1/claims/providers/prv-servicios-hogar-norte"+path, "operations", `{}`)
			assert.Equal(t, c.status, status, path)
			assert.Equal(t, c.code, out["error"], path)
		}
	}
	status, _, _ = send(registryRouter(&fakeRegistry{}), http.MethodPost,
		"/v1/claims/providers/prv-servicios-hogar-norte/availability", "operations", `{"availability": "busy", "x": 1}`)
	assert.Equal(t, http.StatusBadRequest, status)
}
