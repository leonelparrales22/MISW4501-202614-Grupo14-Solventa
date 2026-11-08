package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

// AssistedRenewalService is the use case behind the assisted renewal routes
// (HU-W1.5-03).
type AssistedRenewalService interface {
	View(ctx context.Context, policyID string) (renewal.AssistedView, error)
	Requote(ctx context.Context, policyID string, in renewal.RequoteInput) (renewal.RequoteResult, error)
	RequestConsentCode(ctx context.Context, policyID, requoteID string) (renewal.ConsentCodeSent, error)
	Formalize(ctx context.Context, policyID string, in renewal.FormalizeInput) (renewal.AssistedView, bool, error)
}

// WithAssistedRenewals mounts the assisted renewal of a policy about to expire
// (HU-W1.5-03, screens S7 and S7b): the advisor reads where it stands,
// requotes it, sends the insured a confirmation code and formalizes it. They
// are human routes for the advisor only, and no answer is cached. Register it
// after WithObservability.
func WithAssistedRenewals(svc AssistedRenewalService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleAdvisor))
			r.Get("/v1/policies/{policy_id}/assisted-renewal", getAssistedRenewal(svc))
			r.Post("/v1/policies/{policy_id}/assisted-renewal/requotes", requoteRenewal(svc))
			r.Post("/v1/policies/{policy_id}/assisted-renewal/consent", requestRenewalConsentCode(svc))
			r.Post("/v1/policies/{policy_id}/assisted-renewal/formalization", formalizeRenewal(svc))
		})
	}
}

type assistedPolicyBody struct {
	PolicyID      string               `json:"policy_id"`
	Line          string               `json:"line"`
	Holder        policyHolderBody     `json:"holder"`
	Premium       float64              `json:"premium"`
	Currency      string               `json:"currency"`
	InsuredAmount float64              `json:"insured_amount"`
	Coverages     []policyCoverageBody `json:"coverages"`
	CoverageStart time.Time            `json:"coverage_start"`
	CoverageEnd   time.Time            `json:"coverage_end"`
	RenewalOf     *string              `json:"renewal_of"`
}

type assistedWindowBody struct {
	OpensAt  time.Time `json:"opens_at"`
	ClosesAt time.Time `json:"closes_at"`
	Open     bool      `json:"open"`
}

type appliedRuleBody struct {
	Code  string  `json:"code"`
	Value float64 `json:"value"`
}

type requoteSourceBody struct {
	Source    string `json:"source"`
	Status    string `json:"status"`
	LatencyMs int64  `json:"latency_ms"`
}

// requoteBody is a stored requote. The score, the category and the rules of
// the underwriting never travel: applied_rules are the factors of the rating
// engine.
type requoteBody struct {
	RequoteID         string              `json:"requote_id"`
	CreatedAt         time.Time           `json:"created_at"`
	ExpiresAt         time.Time           `json:"expires_at"`
	Status            string              `json:"status"`
	Current           renewalTermsBody    `json:"current"`
	Renewal           renewalTermsBody    `json:"renewal"`
	PremiumChangeRate float64             `json:"premium_change_rate"`
	DiscountRate      float64             `json:"discount_rate"`
	Requoted          bool                `json:"requoted"`
	RatingVersion     *string             `json:"rating_version"`
	AppliedRules      []appliedRuleBody   `json:"applied_rules"`
	Mode              *string             `json:"mode"`
	Source            *string             `json:"source"`
	ProfileDate       *time.Time          `json:"profile_date"`
	UnavailableSource *string             `json:"unavailable_source"`
	Notice            *string             `json:"notice"`
	Sources           []requoteSourceBody `json:"sources"`
	ConsentBasis      *string             `json:"consent_basis"`
	ConsentAffirmedAt *time.Time          `json:"consent_affirmed_at"`
}

type assistedRenewalSummaryBody struct {
	RenewalID   string     `json:"renewal_id"`
	Status      string     `json:"status"`
	Reason      *string    `json:"reason"`
	Channel     string     `json:"channel"`
	NewPolicyID *string    `json:"new_policy_id"`
	ChargeID    *string    `json:"charge_id"`
	AcceptedAt  *time.Time `json:"accepted_at"`
	RetryUntil  *time.Time `json:"retry_until"`
	ClosedAt    *time.Time `json:"closed_at"`
}

type renewalAcceptanceBody struct {
	Method     string    `json:"method"`
	AcceptedAt time.Time `json:"accepted_at"`
	Reference  *string   `json:"reference"`
}

type renewalFormalizationBody struct {
	AdvisorID    string                `json:"advisor_id"`
	AdvisorEmail string                `json:"advisor_email"`
	AdvisorName  *string               `json:"advisor_name"`
	FormalizedAt time.Time             `json:"formalized_at"`
	RequoteID    string                `json:"requote_id"`
	Acceptance   renewalAcceptanceBody `json:"acceptance"`
}

// renewalChargeBody is where the charge of the renewal stands (decision
// 01·D-3 a+); its retry deadline is renewal.retry_until.
type renewalChargeBody struct {
	Status        string     `json:"status"`
	FailureReason *string    `json:"failure_reason"`
	DeclineCode   *string    `json:"decline_code"`
	LastFailureAt *time.Time `json:"last_failure_at"`
	Attempts      int        `json:"attempts"`
	PolicyID      *string    `json:"policy_id"`
}

type assistedRenewalResponse struct {
	Policy        assistedPolicyBody          `json:"policy"`
	RenewalWindow assistedWindowBody          `json:"renewal_window"`
	Current       renewalTermsBody            `json:"current"`
	PaymentMethod *paymentMethodBody          `json:"payment_method"`
	Requote       *requoteBody                `json:"requote"`
	Renewal       *assistedRenewalSummaryBody `json:"renewal"`
	Formalization *renewalFormalizationBody   `json:"formalization"`
	Charge        *renewalChargeBody          `json:"charge"`
	ConsentMode   string                      `json:"consent_mode"`
	CanRequote    bool                        `json:"can_requote"`
	CanFormalize  bool                        `json:"can_formalize"`
	BlockedReason *string                     `json:"blocked_reason"`
}

type renewalConsentCodeRequest struct {
	RequoteID string `json:"requote_id"`
}

type renewalConsentCodeBody struct {
	RequoteID string    `json:"requote_id"`
	SentTo    string    `json:"sent_to"`
	ExpiresAt time.Time `json:"expires_at"`
}

type requoteRequest struct {
	// ConsentAffirmed is the advisor's checkbox for a policy born in the
	// assisted channel (decision D-13 c); it is ignored for the others.
	ConsentAffirmed *bool `json:"consent_affirmed"`
}

type renewalFormalizeRequest struct {
	RequoteID        string            `json:"requote_id"`
	Acceptance       acceptanceRequest `json:"acceptance"`
	ConfirmationCode string            `json:"confirmation_code"`
}

func currentTermsOf(p domain.Policy) renewalTermsBody {
	return renewalTermsBody{Premium: p.Premium, Currency: p.Currency, InsuredAmount: p.InsuredAmount,
		Coverages: toCoverageBodies(p.Coverages), CoverageStart: p.CoverageStart.UTC(), CoverageEnd: p.CoverageEnd.UTC()}
}

func toRequoteBody(p domain.Policy, r domain.RenewalRequote, status domain.RequoteStatus) requoteBody {
	out := requoteBody{
		RequoteID: r.ID, CreatedAt: r.CreatedAt.UTC(), ExpiresAt: r.ExpiresAt.UTC(), Status: string(status),
		Current: currentTermsOf(p),
		Renewal: renewalTermsBody{Premium: r.Premium, Currency: r.Currency, InsuredAmount: r.Terms.InsuredAmount,
			Coverages: toCoverageBodies(r.Terms.Coverages), CoverageStart: r.PeriodEnd.UTC(),
			CoverageEnd: r.PeriodEnd.AddDate(0, r.Terms.TermMonths, 0).UTC()},
		PremiumChangeRate: domain.PremiumChangeRate(p.Premium, r.Premium), DiscountRate: r.DiscountRate, Requoted: r.Requoted,
		RatingVersion: optionalString(r.RatingVersion), AppliedRules: make([]appliedRuleBody, 0, len(r.AppliedRules)),
		Mode: optionalString(string(r.Mode)), Source: optionalString(string(r.ProfileSource)),
		ProfileDate: optionalTime(r.ProfileDate), UnavailableSource: optionalString(r.UnavailableSource),
		Notice: optionalString(string(r.ProfileNotice)), Sources: make([]requoteSourceBody, 0, len(r.Sources)),
		ConsentBasis: optionalString(string(r.ConsentBasis)), ConsentAffirmedAt: optionalTime(r.ConsentAffirmedAt),
	}
	for _, rule := range r.AppliedRules {
		out.AppliedRules = append(out.AppliedRules, appliedRuleBody{Code: rule.Code, Value: rule.Value})
	}
	for _, s := range r.Sources {
		out.Sources = append(out.Sources, requoteSourceBody{Source: s.Source, Status: string(s.Status), LatencyMs: s.Latency.Milliseconds()})
	}
	return out
}

func toAssistedRenewalResponse(v renewal.AssistedView) assistedRenewalResponse {
	p := v.Policy
	out := assistedRenewalResponse{
		Policy: assistedPolicyBody{PolicyID: p.ID, Line: string(domain.LineLife),
			Holder:  policyHolderBody{FullName: p.Holder.FullName, DocumentID: optionalString(p.Holder.DocumentID)},
			Premium: p.Premium, Currency: p.Currency, InsuredAmount: p.InsuredAmount, Coverages: toCoverageBodies(p.Coverages),
			CoverageStart: p.CoverageStart.UTC(), CoverageEnd: p.CoverageEnd.UTC(), RenewalOf: optionalString(p.RenewalOf)},
		RenewalWindow: assistedWindowBody{OpensAt: v.WindowOpensAt.UTC(), ClosesAt: v.WindowClosesAt.UTC(), Open: v.WindowOpen},
		Current:       currentTermsOf(p), PaymentMethod: methodOf(v.PaymentMethod),
		ConsentMode: string(v.ConsentMode), CanRequote: v.CanRequote, CanFormalize: v.CanFormalize,
		BlockedReason: optionalString(string(v.BlockedReason)),
	}
	if v.Requote != nil {
		body := toRequoteBody(p, *v.Requote, v.RequoteStatus)
		out.Requote = &body
	}
	if r := v.Renewal; r != nil {
		out.Renewal = &assistedRenewalSummaryBody{RenewalID: r.ID, Status: string(r.Status), Reason: optionalString(string(r.Reason)),
			Channel: string(v.Channel), NewPolicyID: optionalString(r.NewPolicyID), ChargeID: optionalString(r.ChargeID),
			AcceptedAt: optionalTime(r.AcceptedAt), RetryUntil: optionalTime(r.RetryUntil), ClosedAt: optionalTime(r.ClosedAt)}
	}
	if f := v.Formalization; f != nil {
		out.Formalization = &renewalFormalizationBody{AdvisorID: f.AdvisorID, AdvisorEmail: f.AdvisorEmail,
			AdvisorName: optionalString(f.AdvisorName), FormalizedAt: f.FormalizedAt.UTC(), RequoteID: f.RequoteID,
			Acceptance: renewalAcceptanceBody{Method: string(f.Acceptance.Method), AcceptedAt: f.Acceptance.AcceptedAt.UTC(),
				Reference: optionalString(f.Acceptance.Reference)}}
	}
	if c := v.Charge; c != nil {
		out.Charge = &renewalChargeBody{Status: string(c.Status), FailureReason: optionalString(string(c.FailureReason)),
			DeclineCode: optionalString(c.DeclineCode), LastFailureAt: optionalTime(c.LastFailureAt), Attempts: c.Attempts,
			PolicyID: optionalString(c.PolicyID)}
	}
	return out
}

func getAssistedRenewal(svc AssistedRenewalService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "policy_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "policy_id (max 128 chars) is required")
			return
		}
		v, err := svc.View(req.Context(), id)
		if err != nil {
			respondAssistedRenewalError(w, req, err)
			return
		}
		writeJSON(w, http.StatusOK, toAssistedRenewalResponse(v))
	}
}

// decodeOptional reads a JSON body that may be empty.
func decodeOptional(w http.ResponseWriter, req *http.Request, into any) bool {
	err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes)).Decode(into)
	if err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
		return false
	}
	return true
}

func requoteRenewal(svc AssistedRenewalService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "policy_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "policy_id (max 128 chars) is required")
			return
		}
		var in requoteRequest
		if !decodeOptional(w, req, &in) {
			return
		}
		res, err := svc.Requote(req.Context(), id, renewal.RequoteInput{ConsentAffirmed: in.ConsentAffirmed != nil && *in.ConsentAffirmed})
		if err != nil {
			respondAssistedRenewalError(w, req, err)
			return
		}
		writeJSON(w, http.StatusCreated, toRequoteBody(res.Policy, res.Requote, domain.RequoteValid))
	}
}

func requestRenewalConsentCode(svc AssistedRenewalService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "policy_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "policy_id (max 128 chars) is required")
			return
		}
		var in renewalConsentCodeRequest
		if !decodeOptional(w, req, &in) {
			return
		}
		if strings.TrimSpace(in.RequoteID) == "" {
			writeValidationError(w, &domain.ValidationError{Fields: []domain.FieldError{{Field: domain.RequoteIDField, Code: domain.CodeRequired}}})
			return
		}
		sent, err := svc.RequestConsentCode(req.Context(), id, in.RequoteID)
		if err != nil {
			respondAssistedRenewalError(w, req, err)
			return
		}
		writeJSON(w, http.StatusOK, renewalConsentCodeBody{RequoteID: sent.RequoteID, SentTo: sent.SentTo, ExpiresAt: sent.ExpiresAt.UTC()})
	}
}

func formalizeRenewal(svc AssistedRenewalService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "policy_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "policy_id (max 128 chars) is required")
			return
		}
		var in renewalFormalizeRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}
		// The acceptance and the code are read as the purchase reads them
		// (HU-W1.1-03); the rest is validated by the use case.
		input, invalid := toFormalizeInput(formalizeRequest{Acceptance: in.Acceptance, ConfirmationCode: in.ConfirmationCode})
		if invalid != nil {
			if strings.TrimSpace(in.RequoteID) == "" {
				invalid.Fields = append([]domain.FieldError{{Field: domain.RequoteIDField, Code: domain.CodeRequired}}, invalid.Fields...)
			}
			writeValidationError(w, invalid)
			return
		}
		v, created, err := svc.Formalize(req.Context(), id, renewal.FormalizeInput{
			RequoteID: strings.TrimSpace(in.RequoteID), Acceptance: input.Acceptance, Code: input.Code,
		})
		if err != nil {
			respondAssistedRenewalError(w, req, err)
			return
		}
		status := http.StatusOK
		if created {
			w.Header().Set("Location", "/v1/policies/"+id+"/assisted-renewal")
			status = http.StatusCreated
		}
		writeJSON(w, status, toAssistedRenewalResponse(v))
	}
}

// respondAssistedRenewalError answers the errors of the assisted renewal: its
// own, the ones of the confirmation code (shared with HU-W1.1-03) and the ones
// of the renewal (shared with HU-W1.5-02).
func respondAssistedRenewalError(w http.ResponseWriter, req *http.Request, err error) {
	var invalid *domain.ValidationError
	switch {
	case isAuthError(err):
		writeAuthError(w, err)
	case errors.As(err, &invalid):
		writeValidationError(w, invalid)
	case errors.Is(err, ports.ErrRenewalRequoteNotFound):
		writeError(w, http.StatusNotFound, "requote_not_found", "the requote does not exist for this policy")
	case errors.Is(err, domain.ErrAssistedRenewalTaken):
		writeError(w, http.StatusConflict, "already_formalized", "the renewal of this period was already formalized by an advisor")
	case errors.Is(err, domain.ErrPaymentMethodMissing):
		writeError(w, http.StatusConflict, "payment_method_missing", "the policy has no registered payment method; the insured can renew online")
	case errors.Is(err, domain.ErrRequoteExpired):
		writeError(w, http.StatusUnprocessableEntity, "requote_expired", "the requote expired; requote the policy again")
	case errors.Is(err, domain.ErrRequoteSuperseded):
		writeError(w, http.StatusUnprocessableEntity, "requote_superseded", "a newer requote of this period replaced it")
	case errors.Is(err, profiling.ErrConsentRequired):
		writeError(w, http.StatusUnprocessableEntity, "consent_required", "the customer has no active consent; nothing was consulted")
	case errors.Is(err, ports.ErrConsentUnverifiable):
		writeError(w, http.StatusServiceUnavailable, "consent_unverifiable", "the consent could not be verified; retry later")
	default:
		if writeConsentCodeError(w, err, "requote") {
			return
		}
		respondRenewalError(w, req, err)
	}
}
