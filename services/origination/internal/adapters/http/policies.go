package httpadapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

// RenewalService is the use case behind the policy and renewal routes
// (HU-W1.5-02).
type RenewalService interface {
	ListPolicies(ctx context.Context) ([]renewal.PolicyView, error)
	Conditions(ctx context.Context, policyID string) (renewal.Conditions, error)
	Accept(ctx context.Context, policyID string) (renewal.Accepted, error)
	Status(ctx context.Context, policyID string) (domain.Renewal, domain.Charge, error)
}

// WithPolicies mounts the policies of the insured and their online renewal
// (HU-W1.5-02, screens A1, A4 and A4b). They are human routes: only the insured
// who owns the policy reads or renews it. Register it after WithObservability.
func WithPolicies(svc RenewalService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleInsured))
			r.Get("/v1/policies", listPolicies(svc))
			r.Get("/v1/policies/{policy_id}/renewal", getRenewalConditions(svc))
			r.Post("/v1/policies/{policy_id}/renewal", acceptRenewal(svc))
			r.Get("/v1/policies/{policy_id}/renewal/status", getRenewalStatus(svc))
		})
	}
}

type policyCoverageBody struct {
	Code          string  `json:"code"`
	InsuredAmount float64 `json:"insured_amount"`
}

func toCoverageBodies(cs []domain.Coverage) []policyCoverageBody {
	out := make([]policyCoverageBody, 0, len(cs))
	for _, c := range cs {
		out = append(out, policyCoverageBody{Code: c.Code, InsuredAmount: c.InsuredAmount})
	}
	return out
}

type policyBody struct {
	PolicyID      string               `json:"policy_id"`
	Holder        string               `json:"holder_name"`
	Premium       float64              `json:"premium"`
	Currency      string               `json:"currency"`
	InsuredAmount float64              `json:"insured_amount"`
	Coverages     []policyCoverageBody `json:"coverages"`
	CoverageStart time.Time            `json:"coverage_start"`
	CoverageEnd   time.Time            `json:"coverage_end"`
	RenewalOf     *string              `json:"renewal_of"`
}

func toPolicyBody(p domain.Policy) policyBody {
	return policyBody{PolicyID: p.ID, Holder: p.Holder.FullName, Premium: p.Premium, Currency: p.Currency,
		InsuredAmount: p.InsuredAmount, Coverages: toCoverageBodies(p.Coverages),
		CoverageStart: p.CoverageStart.UTC(), CoverageEnd: p.CoverageEnd.UTC(), RenewalOf: optionalString(p.RenewalOf)}
}

type renewalSummaryBody struct {
	RenewalID   string     `json:"renewal_id"`
	Status      string     `json:"status"`
	Reason      *string    `json:"reason"`
	NewPolicyID *string    `json:"new_policy_id"`
	ChargeID    *string    `json:"charge_id"`
	AcceptedAt  *time.Time `json:"accepted_at"`
	RetryUntil  *time.Time `json:"retry_until"`
	ClosedAt    *time.Time `json:"closed_at"`
}

func toRenewalSummary(r domain.Renewal) renewalSummaryBody {
	return renewalSummaryBody{RenewalID: r.ID, Status: string(r.Status), Reason: optionalString(string(r.Reason)),
		NewPolicyID: optionalString(r.NewPolicyID), ChargeID: optionalString(r.ChargeID),
		AcceptedAt: optionalTime(r.AcceptedAt), RetryUntil: optionalTime(r.RetryUntil), ClosedAt: optionalTime(r.ClosedAt)}
}

type windowBody struct {
	OpensAt  time.Time `json:"opens_at"`
	ClosesAt time.Time `json:"closes_at"`
}

type policyListItemBody struct {
	policyBody
	RenewalWindow windowBody          `json:"renewal_window"`
	CanRenew      bool                `json:"can_renew"`
	Renewal       *renewalSummaryBody `json:"renewal"`
}

type policyListResponse struct {
	Items []policyListItemBody `json:"items"`
}

func listPolicies(svc RenewalService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		views, err := svc.ListPolicies(req.Context())
		if err != nil {
			respondRenewalError(w, req, err)
			return
		}
		out := policyListResponse{Items: make([]policyListItemBody, 0, len(views))}
		for _, v := range views {
			item := policyListItemBody{policyBody: toPolicyBody(v.Policy), CanRenew: v.CanRenew,
				RenewalWindow: windowBody{OpensAt: v.WindowOpensAt.UTC(), ClosesAt: v.WindowClosesAt.UTC()}}
			if v.Renewal != nil {
				s := toRenewalSummary(*v.Renewal)
				item.Renewal = &s
			}
			out.Items = append(out.Items, item)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

type renewalTermsBody struct {
	Premium       float64              `json:"premium"`
	Currency      string               `json:"currency"`
	InsuredAmount float64              `json:"insured_amount"`
	Coverages     []policyCoverageBody `json:"coverages"`
	CoverageStart time.Time            `json:"coverage_start"`
	CoverageEnd   time.Time            `json:"coverage_end"`
}

func toRenewalTermsBody(t domain.RenewalTerms) renewalTermsBody {
	return renewalTermsBody{Premium: t.Premium, Currency: t.Currency, InsuredAmount: t.InsuredAmount,
		Coverages: toCoverageBodies(t.Coverages), CoverageStart: t.CoverageStart.UTC(), CoverageEnd: t.CoverageEnd.UTC()}
}

type renewalConditionsResponse struct {
	Policy        policyBody       `json:"policy"`
	Current       renewalTermsBody `json:"current"`
	Renewal       renewalTermsBody `json:"renewal"`
	RenewalWindow windowBody       `json:"renewal_window"`
	// Requoted is false when the conditions repeat the current ones because
	// the engine could not price the renewal.
	Requoted bool `json:"requoted"`
	// DiscountRate is the good-experience discount applied, 0 when none.
	DiscountRate  float64            `json:"discount_rate"`
	PaymentMethod *paymentMethodBody `json:"payment_method"`
}

func getRenewalConditions(svc RenewalService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "policy_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "policy_id (max 128 chars) is required")
			return
		}
		c, err := svc.Conditions(req.Context(), id)
		if err != nil {
			respondRenewalError(w, req, err)
			return
		}
		writeJSON(w, http.StatusOK, renewalConditionsResponse{
			Policy: toPolicyBody(c.Policy), Current: toRenewalTermsBody(c.Current), Renewal: toRenewalTermsBody(c.Renewal),
			RenewalWindow: windowBody{OpensAt: c.WindowOpensAt.UTC(), ClosesAt: c.WindowClosesAt.UTC()},
			Requoted:      c.Requoted, DiscountRate: c.DiscountRate, PaymentMethod: methodOf(c.PaymentMethod),
		})
	}
}

func methodOf(m *domain.PaymentMethod) *paymentMethodBody {
	if m == nil {
		return nil
	}
	return toPaymentMethodBody(*m)
}

type renewalResponse struct {
	Renewal renewalSummaryBody `json:"renewal"`
	Terms   renewalTermsBody   `json:"terms"`
	// DiscountRate is the discount applied to the accepted premium.
	DiscountRate float64 `json:"discount_rate"`
	// Charge is the premium charge of the renewal. It is the same record as
	// GET /v1/acceptances/{charge_id}/charge, where the payment method of a
	// payment that was declined can be replaced before retry_until.
	Charge *chargeResponse `json:"charge"`
}

func renewalResponseOf(r domain.Renewal, c domain.Charge, chargeFound bool) renewalResponse {
	out := renewalResponse{
		Renewal: toRenewalSummary(r), DiscountRate: r.DiscountRate,
		Terms: renewalTermsBody{Premium: r.Premium, Currency: r.Currency, InsuredAmount: r.Terms.InsuredAmount,
			Coverages: toCoverageBodies(r.Terms.Coverages), CoverageStart: r.PeriodEnd.UTC(),
			CoverageEnd: r.PeriodEnd.AddDate(0, r.Terms.TermMonths, 0).UTC()},
	}
	if chargeFound {
		c := toChargeResponse(c)
		out.Charge = &c
	}
	return out
}

func acceptRenewal(svc RenewalService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "policy_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "policy_id (max 128 chars) is required")
			return
		}
		a, err := svc.Accept(req.Context(), id)
		if err != nil {
			respondRenewalError(w, req, err)
			return
		}
		status := http.StatusOK
		if a.Created {
			status = http.StatusCreated
		}
		writeJSON(w, status, renewalResponseOf(a.Renewal, a.Charge, a.Charge.AcceptanceID != ""))
	}
}

func getRenewalStatus(svc RenewalService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "policy_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "policy_id (max 128 chars) is required")
			return
		}
		r, c, err := svc.Status(req.Context(), id)
		if err != nil {
			respondRenewalError(w, req, err)
			return
		}
		writeJSON(w, http.StatusOK, renewalResponseOf(r, c, c.AcceptanceID != ""))
	}
}

func respondRenewalError(w http.ResponseWriter, req *http.Request, err error) {
	switch {
	case errors.Is(err, ports.ErrPolicyNotFound):
		writeError(w, http.StatusNotFound, "policy_not_found", "the policy does not exist")
	case errors.Is(err, ports.ErrRenewalNotFound):
		writeError(w, http.StatusNotFound, "renewal_not_found", "the policy has no renewal for its current period")
	case errors.Is(err, domain.ErrRenewalNotAvailable):
		writeError(w, http.StatusConflict, "renewal_not_available", "the policy is not inside its renewal window")
	case errors.Is(err, domain.ErrRenewalClosed):
		writeError(w, http.StatusConflict, "renewal_closed", "the renewal of this period already has an outcome")
	case isAuthError(err):
		writeAuthError(w, err)
	default:
		slog.ErrorContext(req.Context(), "could not read or renew the policy", "error", err)
		writeError(w, http.StatusServiceUnavailable, "renewal_unavailable", "the renewal could not be processed; retry later")
	}
}
