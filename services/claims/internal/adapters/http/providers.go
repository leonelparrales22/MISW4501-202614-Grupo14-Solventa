package httpadapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// ProviderRegistry is what the routes of the registry of providers use
// (HU-W2.3-01, criterion 2).
type ProviderRegistry interface {
	List(ctx context.Context, f ports.ProviderFilter) ([]domain.Provider, error)
	Get(ctx context.Context, id string) (domain.Provider, error)
	Create(ctx context.Context, in domain.ProviderProfile, by string) (domain.Provider, error)
	UpdateProfile(ctx context.Context, id string, in domain.ProviderProfile, by string) (domain.Provider, error)
	SetAvailability(ctx context.Context, id string, in domain.AvailabilityRequest, by string) (domain.Provider, error)
}

// WithProviders mounts the registry of providers that operations keeps
// (HU-W2.3-01, decision D3): GET and POST only, under /v1/claims/providers.
func WithProviders(svc ProviderRegistry, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleOperations))
			r.Get("/v1/claims/providers", listProviders(svc))
			r.Post("/v1/claims/providers", createProvider(svc))
			r.Get("/v1/claims/providers/{provider_id}", getProvider(svc))
			r.Post("/v1/claims/providers/{provider_id}/profile", updateProviderProfile(svc))
			r.Post("/v1/claims/providers/{provider_id}/availability", setProviderAvailability(svc))
		})
	}
}

type providerBody struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Specialties      []string  `json:"specialties"`
	Latitude         float64   `json:"latitude"`
	Longitude        float64   `json:"longitude"`
	Address          string    `json:"address"`
	ContactEmail     string    `json:"contact_email"`
	ContactPhone     *string   `json:"contact_phone"`
	Availability     string    `json:"availability"`
	UnavailableUntil *string   `json:"unavailable_until"`
	BusyClaimID      *string   `json:"busy_claim_id"`
	UpdatedBy        string    `json:"updated_by"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type providerListBody struct {
	Items []providerBody `json:"items"`
}

func providerBodyOf(p domain.Provider) providerBody {
	return providerBody{ID: p.ID, Name: p.Name, Specialties: specialtyNames(p.Specialties),
		Latitude: p.Location.Latitude, Longitude: p.Location.Longitude, Address: p.Address,
		ContactEmail: p.ContactEmail, ContactPhone: p.ContactPhone, Availability: string(p.Availability),
		UnavailableUntil: day(p.UnavailableUntil), BusyClaimID: p.BusyClaimID, UpdatedBy: p.UpdatedBy,
		UpdatedAt: p.UpdatedAt.UTC()}
}

func listProviders(svc ProviderRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		f := ports.ProviderFilter{Specialty: domain.Specialty(q.Get("specialty")),
			Availability: domain.Availability(q.Get("availability"))}
		if (f.Specialty != "" && !f.Specialty.Valid()) || (f.Availability != "" && !f.Availability.Valid()) {
			writeError(w, http.StatusBadRequest, "invalid_request", "specialty or availability is not one of the list")
			return
		}
		list, err := svc.List(req.Context(), f)
		if writeProviderError(w, req, "", err) {
			return
		}
		out := providerListBody{Items: make([]providerBody, 0, len(list))}
		for _, p := range list {
			out.Items = append(out.Items, providerBodyOf(p))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// providerIDOf reads the id of the path; one that cannot be an id of a
// provider is not found.
func providerIDOf(w http.ResponseWriter, req *http.Request) (string, bool) {
	id := chi.URLParam(req, "provider_id")
	if !domain.ValidProviderID(id) {
		writeError(w, http.StatusNotFound, "provider_not_found", "there is no provider with that id")
		return "", false
	}
	return id, true
}

func getProvider(svc ProviderRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := providerIDOf(w, req)
		if !ok {
			return
		}
		p, err := svc.Get(req.Context(), id)
		if writeProviderError(w, req, id, err) {
			return
		}
		writeJSON(w, http.StatusOK, providerBodyOf(p))
	}
}

type providerProfileRequest struct {
	Name         string   `json:"name"`
	Specialties  []string `json:"specialties"`
	Latitude     *float64 `json:"latitude"`
	Longitude    *float64 `json:"longitude"`
	Address      string   `json:"address"`
	ContactEmail string   `json:"contact_email"`
	ContactPhone *string  `json:"contact_phone"`
}

func (in providerProfileRequest) profile() domain.ProviderProfile {
	out := domain.ProviderProfile{Name: in.Name, Latitude: in.Latitude, Longitude: in.Longitude, Address: in.Address,
		ContactEmail: in.ContactEmail, ContactPhone: in.ContactPhone}
	for _, s := range in.Specialties {
		out.Specialties = append(out.Specialties, domain.Specialty(s))
	}
	return out
}

func createProvider(svc ProviderRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var body providerProfileRequest
		if !decodeStrict(w, req, &body) {
			return
		}
		p, err := svc.Create(req.Context(), body.profile(), analystOf(req))
		if writeProviderError(w, req, "", err) {
			return
		}
		slog.InfoContext(req.Context(), "provider registered", "provider_id", p.ID)
		writeJSON(w, http.StatusCreated, providerBodyOf(p))
	}
}

func updateProviderProfile(svc ProviderRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := providerIDOf(w, req)
		if !ok {
			return
		}
		var body providerProfileRequest
		if !decodeStrict(w, req, &body) {
			return
		}
		p, err := svc.UpdateProfile(req.Context(), id, body.profile(), analystOf(req))
		if writeProviderError(w, req, id, err) {
			return
		}
		slog.InfoContext(req.Context(), "provider profile changed", "provider_id", p.ID)
		writeJSON(w, http.StatusOK, providerBodyOf(p))
	}
}

type availabilityRequest struct {
	Availability     string  `json:"availability"`
	UnavailableUntil *string `json:"unavailable_until"`
}

func setProviderAvailability(svc ProviderRegistry) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := providerIDOf(w, req)
		if !ok {
			return
		}
		var body availabilityRequest
		if !decodeStrict(w, req, &body) {
			return
		}
		p, err := svc.SetAvailability(req.Context(), id, domain.AvailabilityRequest{
			Availability: domain.Availability(body.Availability), UnavailableUntil: body.UnavailableUntil},
			analystOf(req))
		if writeProviderError(w, req, id, err) {
			return
		}
		slog.InfoContext(req.Context(), "provider availability changed", "provider_id", p.ID,
			"availability", p.Availability)
		writeJSON(w, http.StatusOK, providerBodyOf(p))
	}
}

// writeProviderError answers the error of the registry and tells whether
// there was one. The log names the provider, never its contact.
func writeProviderError(w http.ResponseWriter, req *http.Request, providerID string, err error) bool {
	var invalid *domain.ValidationError
	switch {
	case err == nil:
		return false
	case errors.As(err, &invalid):
		writeValidationError(w, invalid)
	case errors.Is(err, ports.ErrProviderNotFound):
		writeError(w, http.StatusNotFound, "provider_not_found", "there is no provider with that id")
	default:
		slog.ErrorContext(req.Context(), "could not serve the registry of providers", "provider_id", providerID,
			"error", err)
		writeError(w, http.StatusServiceUnavailable, "claims_unavailable", "the registry could not be served; retry")
	}
	return true
}
