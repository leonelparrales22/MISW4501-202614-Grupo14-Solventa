package httpadapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
)

// Performance is the use case behind the technical performance of the
// partner API (HU-W3.2-03).
type Performance interface {
	Performance(ctx context.Context, period domain.Period) (domain.Performance, error)
}

// performanceTimeout bounds the reads of CloudWatch of one request.
const performanceTimeout = 8 * time.Second

// WithPerformance mounts GET /v1/partner-performance?period=today|7d|30d:
// the current state of the partner API, its availability and p95 latency in
// the period and its incidents (HU-W3.2-03, screen B3). It is a human route
// for the users of the partner portal only. The SLO is the one of the partner
// API, which every partner shares, so the answer is the same for all of them.
func WithPerformance(svc Performance, verifier security.Verifier, clock func() time.Time) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RolePartner))
			r.Get("/v1/partner-performance", performance(svc, clock))
		})
	}
}

type availabilityBody struct {
	Percent          float64 `json:"percent"`
	ObjectivePercent float64 `json:"objective_percent"`
	Requests         int64   `json:"requests"`
	ServerErrors     int64   `json:"server_errors"`
}

type latencyBody struct {
	P95Ms    *float64 `json:"p95_ms"`
	BudgetMs float64  `json:"budget_ms"`
}

type incidentBody struct {
	ID              string     `json:"id"`
	Kind            string     `json:"kind"`
	Route           string     `json:"route"`
	StartedAt       time.Time  `json:"started_at"`
	EndedAt         *time.Time `json:"ended_at"`
	DurationSeconds int64      `json:"duration_seconds"`
	Resolved        bool       `json:"resolved"`
}

type performanceBody struct {
	Period       string           `json:"period"`
	From         time.Time        `json:"from"`
	To           time.Time        `json:"to"`
	Status       string           `json:"status"`
	Availability availabilityBody `json:"availability"`
	Latency      latencyBody      `json:"latency"`
	Incidents    []incidentBody   `json:"incidents"`
}

func performance(svc Performance, clock func() time.Time) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		period, err := domain.ParsePeriod(req.URL.Query().Get("period"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "period must be today, 7d or 30d")
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), performanceTimeout)
		defer cancel()
		p, err := svc.Performance(ctx, period)
		if err != nil {
			if !errors.Is(err, domain.ErrTelemetryUnavailable) {
				slog.ErrorContext(req.Context(), "could not read the performance of the partner API", "error", err)
			} else {
				slog.WarnContext(req.Context(), "the telemetry of the partner API is not available", "error", err)
			}
			writeError(w, http.StatusServiceUnavailable, "telemetry_unavailable",
				"the performance of the API could not be read now; try again")
			return
		}
		now := clock()
		out := performanceBody{
			Period: string(p.Period), From: p.From.UTC(), To: p.To.UTC(), Status: string(p.Status),
			Availability: availabilityBody{Percent: p.AvailabilityPercent(), ObjectivePercent: p.ObjectivePercent,
				Requests: p.Requests, ServerErrors: p.ServerErrors},
			Latency:   latencyBody{P95Ms: p.P95Ms, BudgetMs: p.BudgetMs},
			Incidents: make([]incidentBody, 0, len(p.Incidents)),
		}
		for _, i := range p.Incidents {
			out.Incidents = append(out.Incidents, incidentBody{
				ID: i.ID, Kind: string(i.Kind), Route: i.Route, StartedAt: i.StartedAt.UTC(), EndedAt: utc(i.EndedAt),
				DurationSeconds: int64(i.Duration(now).Seconds()), Resolved: i.Resolved(),
			})
		}
		writeJSON(w, http.StatusOK, out)
	}
}
