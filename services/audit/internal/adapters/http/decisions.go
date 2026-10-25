package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

// maxDecisionBodyBytes bounds a decision record (up to 20 sources and 50 rules).
const maxDecisionBodyBytes = 1 << 16

// DecisionRecorder is the use case behind the internal route origination calls
// to record a decision (HU-W4.1-01).
type DecisionRecorder interface {
	Record(ctx context.Context, r domain.DecisionRecord) (domain.DecisionRecord, bool, error)
}

// DecisionReader is the use case behind the analyst's lookup.
type DecisionReader interface {
	Get(ctx context.Context, offerID string) (domain.DecisionRecord, error)
}

// WithDecisionRecords mounts the route origination uses to record an
// underwriting decision. Like the consent verification it is a call between
// services without a Cognito token, so it must be reachable only from inside
// the network, never published through the API Gateway.
func WithDecisionRecords(svc DecisionRecorder) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore)
			r.Post("/v1/audit/decisions", recordDecision(svc))
		})
	}
}

// WithDecisionReads mounts the lookup, a human route: the Cognito JWT is
// verified with pkg/security and only operations analysts read it.
func WithDecisionReads(svc DecisionReader, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleOperations))
			r.Get("/v1/audit/decisions/{offer_id}", getDecision(svc))
		})
	}
}

type decisionSourceBody struct {
	Source    string `json:"source"`
	Status    string `json:"status"`
	LatencyMS int64  `json:"latency_ms"`
}

type decisionRuleBody struct {
	Order int     `json:"order"`
	Code  string  `json:"code"`
	Value float64 `json:"value"`
}

type decisionProfileBody struct {
	Score    float64 `json:"score"`
	Category string  `json:"category"`
	Partial  bool    `json:"partial"`
}

// decisionBody is both the request origination sends and the record the
// analyst reads, so what is stored is exactly what is shown. recorded_at is
// set by the server and ignored in the request.
type decisionBody struct {
	OfferID        string               `json:"offer_id"`
	CustomerID     string               `json:"customer_id"`
	PartnerID      string               `json:"partner_id"`
	ApplicationID  string               `json:"application_id"`
	Channel        string               `json:"channel"`
	RequestedAt    time.Time            `json:"requested_at"`
	RaterVersion   string               `json:"rater_version"`
	Sources        []decisionSourceBody `json:"sources"`
	Profile        decisionProfileBody  `json:"profile"`
	Rules          []decisionRuleBody   `json:"rules"`
	Premium        float64              `json:"premium"`
	Currency       string               `json:"currency"`
	Decision       string               `json:"decision"`
	DecisionReason string               `json:"decision_reason"`
	RecordedAt     *time.Time           `json:"recorded_at,omitempty"`
}

func (b decisionBody) record() domain.DecisionRecord {
	r := domain.DecisionRecord{
		OfferID: b.OfferID, CustomerID: b.CustomerID, PartnerID: b.PartnerID, ApplicationID: b.ApplicationID,
		Channel: b.Channel, RequestedAt: b.RequestedAt, RaterVersion: b.RaterVersion,
		ProfileScore: b.Profile.Score, ProfileCategory: b.Profile.Category, ProfilePartial: b.Profile.Partial,
		Premium: b.Premium, Currency: b.Currency, Decision: domain.Decision(b.Decision), DecisionReason: b.DecisionReason,
	}
	for _, s := range b.Sources {
		r.Sources = append(r.Sources, domain.SourceConsulted{Source: s.Source, Status: s.Status, LatencyMS: s.LatencyMS})
	}
	for _, rule := range b.Rules {
		r.Rules = append(r.Rules, domain.RuleApplied{Order: rule.Order, Code: rule.Code, Value: rule.Value})
	}
	return r
}

func bodyOf(r domain.DecisionRecord) decisionBody {
	recorded := r.RecordedAt
	b := decisionBody{
		OfferID: r.OfferID, CustomerID: r.CustomerID, PartnerID: r.PartnerID, ApplicationID: r.ApplicationID,
		Channel: r.Channel, RequestedAt: r.RequestedAt, RaterVersion: r.RaterVersion,
		Sources: []decisionSourceBody{}, Rules: []decisionRuleBody{},
		Profile: decisionProfileBody{Score: r.ProfileScore, Category: r.ProfileCategory, Partial: r.ProfilePartial},
		Premium: r.Premium, Currency: r.Currency, Decision: string(r.Decision), DecisionReason: r.DecisionReason,
		RecordedAt: &recorded,
	}
	for _, s := range r.Sources {
		b.Sources = append(b.Sources, decisionSourceBody{Source: s.Source, Status: s.Status, LatencyMS: s.LatencyMS})
	}
	for _, rule := range r.Rules {
		b.Rules = append(b.Rules, decisionRuleBody{Order: rule.Order, Code: rule.Code, Value: rule.Value})
	}
	return b
}

func recordDecision(svc DecisionRecorder) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in decisionBody
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxDecisionBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}
		stored, created, err := svc.Record(req.Context(), in.record())
		var verr *domain.ValidationError
		switch {
		case errors.As(err, &verr):
			writeValidation(w, verr)
		case err != nil:
			slog.ErrorContext(req.Context(), "could not record the decision", "error", err)
			writeError(w, http.StatusServiceUnavailable, "service_unavailable", "the decision could not be recorded; try again")
		default:
			status := http.StatusOK
			if created {
				status = http.StatusCreated
			}
			writeJSON(w, status, bodyOf(stored))
		}
	}
}

func getDecision(svc DecisionReader) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id := chi.URLParam(req, "offer_id")
		if id == "" || len(id) > domain.MaxIdentifierLength {
			writeError(w, http.StatusNotFound, "decision_not_found", "no audit record was found for that identifier")
			return
		}
		r, err := svc.Get(req.Context(), id)
		switch {
		case errors.Is(err, domain.ErrDecisionNotFound):
			writeError(w, http.StatusNotFound, "decision_not_found", "no audit record was found for that identifier")
		case err != nil:
			slog.ErrorContext(req.Context(), "could not read the decision", "error", err)
			writeError(w, http.StatusServiceUnavailable, "service_unavailable", "the audit record could not be read; try again")
		default:
			writeJSON(w, http.StatusOK, bodyOf(r))
		}
	}
}
