// Package httpadapter exposes the notification center over HTTP (chi):
// the notifications of the insured, reading them and registering the devices
// the pushes go to (HU-M3.1-01).
package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/ports"
)

const (
	maxBodyBytes = 1 << 13
	maxIDLength  = 128
)

// CenterService is the use case behind the human routes.
type CenterService interface {
	List(ctx context.Context, limit int) ([]domain.Notification, int, error)
	Get(ctx context.Context, id string) (domain.Notification, error)
	MarkRead(ctx context.Context, id string) error
	MarkAllRead(ctx context.Context) error
	RegisterDevice(ctx context.Context, d domain.Device) (domain.Device, error)
}

// Option mounts optional routes on the service router.
type Option func(chi.Router)

// WithObservability adds the correlation id, the server span, the request
// metrics and the access log to every route. Pass it before any other option.
func WithObservability(service string) Option {
	return func(r chi.Router) {
		r.Use(observability.Middleware(service, func(req *http.Request) string {
			if rc := chi.RouteContext(req.Context()); rc != nil {
				return rc.RoutePattern()
			}
			return ""
		}))
	}
}

// NewRouter builds the service router: GET /health plus whatever the options
// mount.
func NewRouter(opts ...Option) http.Handler {
	r := chi.NewRouter()
	for _, opt := range opts {
		opt(r)
	}
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return r
}

// WithCenter mounts the notification center of the insured (screen M15 and
// M10b of the mobile app) and the registration of their devices. Every route
// needs the ID token of the insured; nobody sees another insured's
// notifications.
func WithCenter(svc CenterService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleInsured))
			r.Get("/v1/notifications", listNotifications(svc))
			r.Post("/v1/notifications/read-all", markAllRead(svc))
			r.Get("/v1/notifications/{notification_id}", getNotification(svc))
			r.Post("/v1/notifications/{notification_id}/read", markRead(svc))
			r.Put("/v1/devices/{installation_id}", registerDevice(svc))
		})
	}
}

type triggerBody struct {
	Parameter      string     `json:"parameter"`
	EventReference string     `json:"event_reference"`
	Threshold      float64    `json:"threshold"`
	Observed       float64    `json:"observed"`
	Unit           string     `json:"unit"`
	ObservedAt     *time.Time `json:"observed_at"`
}

type paymentMethodBody struct {
	Brand string `json:"brand"`
	Last4 string `json:"last4"`
}

type payoutBody struct {
	PayoutID       string            `json:"payout_id"`
	ClaimID        string            `json:"claim_id"`
	PolicyID       string            `json:"policy_id"`
	Amount         float64           `json:"amount"`
	Currency       string            `json:"currency"`
	Trigger        triggerBody       `json:"trigger"`
	PaymentMethod  paymentMethodBody `json:"payment_method"`
	IdempotencyKey string            `json:"idempotency_key"`
	PaidAt         time.Time         `json:"paid_at"`
}

type notificationBody struct {
	NotificationID string      `json:"notification_id"`
	Kind           string      `json:"kind"`
	OccurredAt     time.Time   `json:"occurred_at"`
	CreatedAt      time.Time   `json:"created_at"`
	ReadAt         *time.Time  `json:"read_at"`
	Payout         *payoutBody `json:"payout"`
}

func toBody(n domain.Notification) notificationBody {
	out := notificationBody{NotificationID: n.ID, Kind: n.Kind, OccurredAt: n.OccurredAt.UTC(), CreatedAt: n.CreatedAt.UTC(),
		ReadAt: n.ReadAt}
	if p := n.Payout; p != nil {
		t := p.Trigger
		var observedAt *time.Time
		if !t.ObservedAt.IsZero() {
			at := t.ObservedAt.UTC()
			observedAt = &at
		}
		out.Payout = &payoutBody{
			PayoutID: p.PayoutID, ClaimID: p.ClaimID, PolicyID: p.PolicyID, Amount: float64(p.AmountMinor) / 100,
			Currency: p.Currency, IdempotencyKey: p.IdempotencyKey, PaidAt: p.PaidAt.UTC(),
			Trigger: triggerBody{Parameter: t.Parameter, EventReference: t.EventReference, Threshold: t.Threshold,
				Observed: t.Observed, Unit: t.Unit, ObservedAt: observedAt},
			PaymentMethod: paymentMethodBody{Brand: p.Method.Brand, Last4: p.Method.Last4},
		}
	}
	return out
}

type listResponse struct {
	Items       []notificationBody `json:"items"`
	UnreadCount int                `json:"unread_count"`
}

func listNotifications(svc CenterService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		limit := 0
		if raw := req.URL.Query().Get("limit"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 {
				writeError(w, http.StatusBadRequest, "invalid_request", "limit must be a positive integer")
				return
			}
			limit = n
		}
		items, unread, err := svc.List(req.Context(), limit)
		if err != nil {
			respondError(w, req, err)
			return
		}
		out := listResponse{Items: make([]notificationBody, 0, len(items)), UnreadCount: unread}
		for _, n := range items {
			out.Items = append(out.Items, toBody(n))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func getNotification(svc CenterService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "notification_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "notification_id (max 128 chars) is required")
			return
		}
		n, err := svc.Get(req.Context(), id)
		if err != nil {
			respondError(w, req, err)
			return
		}
		writeJSON(w, http.StatusOK, toBody(n))
	}
}

func markRead(svc CenterService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "notification_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "notification_id (max 128 chars) is required")
			return
		}
		if err := svc.MarkRead(req.Context(), id); err != nil {
			respondError(w, req, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func markAllRead(svc CenterService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if err := svc.MarkAllRead(req.Context()); err != nil {
			respondError(w, req, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

type deviceRequest struct {
	Platform    string `json:"platform"`
	Token       string `json:"token"`
	PushEnabled bool   `json:"push_enabled"`
	Locale      string `json:"locale"`
}

type deviceResponse struct {
	InstallationID string    `json:"installation_id"`
	Platform       string    `json:"platform"`
	PushEnabled    bool      `json:"push_enabled"`
	Locale         string    `json:"locale"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func registerDevice(svc CenterService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "installation_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "installation_id (max 128 chars) is required")
			return
		}
		var in deviceRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the body is not a valid device")
			return
		}
		d, err := svc.RegisterDevice(req.Context(), domain.Device{InstallationID: id, Platform: in.Platform, Token: in.Token,
			PushEnabled: in.PushEnabled, Locale: in.Locale})
		if err != nil {
			respondError(w, req, err)
			return
		}
		writeJSON(w, http.StatusOK, deviceResponse{InstallationID: d.InstallationID, Platform: d.Platform,
			PushEnabled: d.PushEnabled, Locale: d.Locale, UpdatedAt: d.UpdatedAt.UTC()})
	}
}

type errorResponse struct {
	Error   string `json:"error"`
	Message string `json:"message"`
}

func respondError(w http.ResponseWriter, req *http.Request, err error) {
	switch {
	case errors.Is(err, ports.ErrNotificationNotFound):
		writeError(w, http.StatusNotFound, "notification_not_found", "the notification does not exist")
	case errors.Is(err, domain.ErrInvalidDevice):
		writeError(w, http.StatusBadRequest, "invalid_device", err.Error())
	case errors.Is(err, security.ErrUnauthenticated):
		writeError(w, http.StatusUnauthorized, "unauthenticated", "a valid token is required")
	default:
		slog.ErrorContext(req.Context(), "could not serve the notification center", "error", err)
		writeError(w, http.StatusServiceUnavailable, "notifications_unavailable", "the notifications could not be read; retry later")
	}
}

func pathID(req *http.Request, name string) (string, bool) {
	id := chi.URLParam(req, name)
	return id, id != "" && len(id) <= maxIDLength
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: code, Message: message})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("could not write the response", "error", err)
	}
}

func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
