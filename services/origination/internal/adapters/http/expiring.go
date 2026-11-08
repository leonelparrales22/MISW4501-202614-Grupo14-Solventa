package httpadapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// ExpiringPoliciesService is the use case behind the list of the policies
// about to expire (HU-W1.5-01).
type ExpiringPoliciesService interface {
	List(ctx context.Context, q domain.ExpiringQuery) (domain.ExpiringPage, error)
}

// WithExpiringPolicies mounts GET /v1/policies/expiring: the policies about to
// expire that the advisor renews from (HU-W1.5-01, screen S6). It is a human
// route for the advisor only. The fixed segment "expiring" never covers the
// routes of a policy (/v1/policies/{policy_id}/…): those always have a
// segment after the id. Register it after WithObservability.
func WithExpiringPolicies(svc ExpiringPoliciesService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleAdvisor))
			r.Get("/v1/policies/expiring", listExpiringPolicies(svc))
		})
	}
}

// expiringPolicyBody is one row of the list. The holder never carries the
// e-mail (the advisor does not need it on this screen) and its document type
// is null: the policy does not keep it. status may gain values (HU-W1.6-02
// will add the policies cancelled for non-payment), so a client shows an
// unknown one neutrally.
type expiringPolicyBody struct {
	PolicyID      string              `json:"policy_id"`
	Line          string              `json:"line"`
	Product       *string             `json:"product"`
	Holder        policyHolderBody    `json:"holder"`
	Premium       float64             `json:"premium"`
	Currency      string              `json:"currency"`
	CoverageEnd   time.Time           `json:"coverage_end"`
	Status        string              `json:"status"`
	CanRenew      bool                `json:"can_renew"`
	RenewalWindow windowBody          `json:"renewal_window"`
	Renewal       *renewalSummaryBody `json:"renewal"`
}

type expiringPoliciesResponse struct {
	Items []expiringPolicyBody `json:"items"`
	// ExpiresFrom and ExpiresTo are the range that was applied (civil days
	// of Colombia), the default one when the query had none.
	ExpiresFrom string `json:"expires_from"`
	ExpiresTo   string `json:"expires_to"`
	WindowDays  int    `json:"window_days"`
	// DueWithinWindow counts the policies with an open period that end
	// within the renewal window, whatever the filter.
	DueWithinWindow int `json:"due_within_window"`
	Total           int `json:"total"`
	Page            int `json:"page"`
	PageSize        int `json:"page_size"`
}

func toExpiringPolicyBody(e domain.ExpiringPolicy) expiringPolicyBody {
	p := e.Policy
	out := expiringPolicyBody{
		PolicyID: p.ID, Line: string(domain.LineLife), Product: nil,
		Holder:  policyHolderBody{FullName: p.Holder.FullName, DocumentID: optionalString(p.Holder.DocumentID)},
		Premium: p.Premium, Currency: p.Currency, CoverageEnd: p.CoverageEnd.UTC(),
		Status: string(e.Status), CanRenew: e.CanRenew,
		RenewalWindow: windowBody{OpensAt: e.WindowOpensAt.UTC(), ClosesAt: e.WindowClosesAt.UTC()},
	}
	if e.Renewal != nil {
		s := toRenewalSummary(*e.Renewal)
		out.Renewal = &s
	}
	return out
}

// expiringQueryOf reads the query of the list. It reports the first
// parameter it cannot read; the rules of the range are the use case's.
func expiringQueryOf(req *http.Request) (domain.ExpiringQuery, string) {
	v := req.URL.Query()
	var q domain.ExpiringQuery
	for _, d := range []struct {
		name string
		dest *time.Time
	}{{"expires_from", &q.From}, {"expires_to", &q.To}} {
		if raw := strings.TrimSpace(v.Get(d.name)); raw != "" {
			day, err := domain.ParseCivilDay(raw)
			if err != nil {
				return domain.ExpiringQuery{}, d.name + " must be a date (AAAA-MM-DD)"
			}
			*d.dest = day
		}
	}
	status, err := domain.ParseExpiringStatus(v.Get("status"))
	if err != nil {
		return domain.ExpiringQuery{}, "status must be expiring or renewal_in_progress"
	}
	q.Status = status
	for _, n := range []struct {
		name string
		dest *int
	}{{"page", &q.Page}, {"page_size", &q.PageSize}} {
		if raw := strings.TrimSpace(v.Get(n.name)); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 1 {
				return domain.ExpiringQuery{}, n.name + " must be a positive integer"
			}
			*n.dest = value
		}
	}
	return q, ""
}

func listExpiringPolicies(svc ExpiringPoliciesService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q, msg := expiringQueryOf(req)
		if msg != "" {
			writeError(w, http.StatusBadRequest, "invalid_request", msg)
			return
		}
		page, err := svc.List(req.Context(), q)
		switch {
		case err == nil:
		case errors.Is(err, domain.ErrExpiringRangeInverted):
			writeError(w, http.StatusBadRequest, "out_of_range", "expires_to must not be before expires_from")
			return
		case errors.Is(err, domain.ErrInvalidExpiringQuery):
			writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
			return
		case isAuthError(err):
			writeAuthError(w, err)
			return
		default:
			slog.ErrorContext(req.Context(), "could not list the policies about to expire", "error", err)
			writeError(w, http.StatusServiceUnavailable, "renewal_unavailable", "the policies could not be listed; retry later")
			return
		}
		out := expiringPoliciesResponse{
			Items:       make([]expiringPolicyBody, 0, len(page.Items)),
			ExpiresFrom: page.From.Format(domain.CivilDayLayout), ExpiresTo: page.To.Format(domain.CivilDayLayout),
			WindowDays: page.WindowDays, DueWithinWindow: page.DueWithinWindow, Total: page.Total,
			Page: page.Page, PageSize: page.PageSize,
		}
		for _, it := range page.Items {
			out.Items = append(out.Items, toExpiringPolicyBody(it))
		}
		writeJSON(w, http.StatusOK, out)
	}
}
