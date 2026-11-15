package httpadapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/partnermetrics"
)

// PartnerMetricsService is the use case behind the summary of the dashboard
// of a commercial partner (HU-W3.2-01).
type PartnerMetricsService interface {
	Summary(ctx context.Context, partnerID string, q domain.PartnerMetricsQuery) (domain.PartnerSummary, error)
}

// WithPartnerMetrics mounts GET /v1/partner-metrics/summary: the quotes, the
// policies issued and the conversion of the partner of the caller in a range
// of civil days (HU-W3.2-01, screen B1). It is a human route for the users of
// the partner portal only. The partner is always the custom:partner_id claim
// of the verified token (HA-S01): the request cannot name another one (CA-4).
// Register it after WithObservability.
func WithPartnerMetrics(svc PartnerMetricsService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RolePartner))
			r.Get("/v1/partner-metrics/summary", partnerSummary(svc))
		})
	}
}

type partnerSummaryResponse struct {
	PartnerID       string  `json:"partner_id"`
	From            string  `json:"from"`
	To              string  `json:"to"`
	QuotesRequested int     `json:"quotes_requested"`
	PoliciesIssued  int     `json:"policies_issued"`
	ConversionRate  float64 `json:"conversion_rate"`
}

// partnerMetricsQueryOf reads from and to, both required. It reports the first
// parameter it cannot read; the rules of the range are the use case's.
func partnerMetricsQueryOf(req *http.Request) (domain.PartnerMetricsQuery, string) {
	v := req.URL.Query()
	var q domain.PartnerMetricsQuery
	for _, d := range []struct {
		name string
		dest *time.Time
	}{{"from", &q.From}, {"to", &q.To}} {
		raw := strings.TrimSpace(v.Get(d.name))
		if raw == "" {
			return domain.PartnerMetricsQuery{}, d.name + " is required (AAAA-MM-DD)"
		}
		day, err := domain.ParseCivilDay(raw)
		if err != nil {
			return domain.PartnerMetricsQuery{}, d.name + " must be a date (AAAA-MM-DD)"
		}
		*d.dest = day
	}
	return q, ""
}

func partnerSummary(svc PartnerMetricsService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q, msg := partnerMetricsQueryOf(req)
		if msg != "" {
			writeError(w, http.StatusBadRequest, "invalid_request", msg)
			return
		}
		claims, _ := security.ClaimsFromContext(req.Context())
		s, err := svc.Summary(req.Context(), claims.PartnerID, q)
		switch {
		case err == nil:
		case errors.Is(err, partnermetrics.ErrNoPartner):
			// A partner user without its partner claim sees nothing (CA-4).
			security.WriteForbidden(w)
			return
		case errors.Is(err, domain.ErrPartnerMetricsRangeInverted):
			writeError(w, http.StatusBadRequest, "out_of_range", "to must not be before from")
			return
		case errors.Is(err, domain.ErrPartnerMetricsRangeTooLong):
			writeError(w, http.StatusBadRequest, "range_too_long", "the range can cover at most 366 days")
			return
		case errors.Is(err, domain.ErrPartnerMetricsRangeMissing):
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		default:
			slog.ErrorContext(req.Context(), "could not read the summary of the partner", "error", err)
			writeError(w, http.StatusServiceUnavailable, "metrics_unavailable", "the summary could not be read; retry later")
			return
		}
		writeJSON(w, http.StatusOK, partnerSummaryResponse{
			PartnerID: s.PartnerID, From: s.From.Format(domain.CivilDayLayout), To: s.To.Format(domain.CivilDayLayout),
			QuotesRequested: s.QuotesRequested, PoliciesIssued: s.PoliciesIssued, ConversionRate: s.ConversionRate,
		})
	}
}
