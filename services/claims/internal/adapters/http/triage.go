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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/triage"
)

// TriageReader is what the routes of the triage read (HU-W2.2-01).
type TriageReader interface {
	View(ctx context.Context, claimID string) (triage.View, error)
	Summaries(ctx context.Context, ids []string) ([]triage.Summary, error)
	Policy() domain.TriagePolicy
}

// maxTriageBatch is the most claims a batch read asks for: a page of the
// lists of claims.
const maxTriageBatch = 50

// WithTriage mounts the reads of the priority of the claims (HU-W2.2-01): one
// claim, for operations and for the insured who filed it, a page of claims and
// the policy in force, for operations only.
func WithTriage(svc TriageReader, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier))
			r.With(security.RequireRole(security.RoleOperations, security.RoleInsured)).
				Get("/v1/claims/{claim_id}/triage", claimTriage(svc))
			r.With(security.RequireRole(security.RoleOperations)).Get("/v1/claims/triage", triageBatch(svc))
			r.With(security.RequireRole(security.RoleOperations)).Get("/v1/claims/triage-policy", triagePolicy(svc))
		})
	}
}

type triageTargetBody struct {
	Kind                   string `json:"kind"`
	BusinessDays           *int   `json:"business_days"`
	ResolutionBusinessDays int    `json:"resolution_business_days"`
}

func targetBody(t domain.TriageTarget) triageTargetBody {
	out := triageTargetBody{Kind: string(t.Kind), ResolutionBusinessDays: t.ResolutionBusinessDays}
	if t.Kind != domain.TargetImmediate {
		days := t.BusinessDays
		out.BusinessDays = &days
	}
	return out
}

type triageAssessmentBody struct {
	Level                int              `json:"level"`
	Severity             string           `json:"severity"`
	AmountBand           string           `json:"amount_band"`
	EstimatedAmountMinor *int64           `json:"estimated_amount_minor"`
	Currency             string           `json:"currency"`
	IncidentType         string           `json:"incident_type"`
	Coverage             string           `json:"coverage"`
	PolicyVersion        string           `json:"policy_version"`
	Target               triageTargetBody `json:"target"`
	AssessedAt           time.Time        `json:"assessed_at"`
}

type triageHistoryBody struct {
	Level                int       `json:"level"`
	PreviousLevel        *int      `json:"previous_level"`
	Severity             string    `json:"severity"`
	AmountBand           string    `json:"amount_band"`
	EstimatedAmountMinor *int64    `json:"estimated_amount_minor"`
	IncidentType         string    `json:"incident_type"`
	Causes               []string  `json:"causes"`
	Actor                string    `json:"actor"`
	PolicyVersion        string    `json:"policy_version"`
	AssessedAt           time.Time `json:"assessed_at"`
}

// claimTriageBody is the view of operations.
type claimTriageBody struct {
	View    string                `json:"view"`
	ClaimID string                `json:"claim_id"`
	State   string                `json:"state"`
	Current *triageAssessmentBody `json:"current"`
	History []triageHistoryBody   `json:"history"`
}

// insuredTriageBody is the view of the insured who filed the claim: the level
// and the resolution time, nothing of how it was calculated.
type insuredTriageBody struct {
	View                            string     `json:"view"`
	ClaimID                         string     `json:"claim_id"`
	State                           string     `json:"state"`
	Level                           *int       `json:"level"`
	EstimatedResolutionBusinessDays *int       `json:"estimated_resolution_business_days"`
	AssessedAt                      *time.Time `json:"assessed_at"`
}

func assessmentBody(r domain.TriageRecord) *triageAssessmentBody {
	a := r.Assessment
	return &triageAssessmentBody{
		Level: int(a.Level), Severity: string(a.Severity), AmountBand: string(a.AmountBand),
		EstimatedAmountMinor: a.Input.EstimatedAmountMinor, Currency: domain.TriageCurrency,
		IncidentType: string(a.Input.IncidentType), Coverage: a.Input.Coverage, PolicyVersion: a.Input.PolicyVersion,
		Target: targetBody(a.Target), AssessedAt: r.AssessedAt.UTC(),
	}
}

func historyBody(r domain.TriageRecord) triageHistoryBody {
	a := r.Assessment
	out := triageHistoryBody{
		Level: int(a.Level), Severity: string(a.Severity), AmountBand: string(a.AmountBand),
		EstimatedAmountMinor: a.Input.EstimatedAmountMinor, IncidentType: string(a.Input.IncidentType),
		Causes: make([]string, 0, len(r.Causes)), Actor: r.Actor, PolicyVersion: a.Input.PolicyVersion,
		AssessedAt: r.AssessedAt.UTC(),
	}
	for _, c := range r.Causes {
		out.Causes = append(out.Causes, string(c))
	}
	if r.PreviousLevel != nil {
		previous := int(*r.PreviousLevel)
		out.PreviousLevel = &previous
	}
	return out
}

func claimTriage(svc TriageReader) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id := chi.URLParam(req, "claim_id")
		if !claimIDPattern.MatchString(id) {
			writeError(w, http.StatusNotFound, "claim_not_found", "there is no claim with that id")
			return
		}
		v, err := svc.View(req.Context(), id)
		if writeTriageError(w, req, err) {
			return
		}
		claims, _ := security.ClaimsFromContext(req.Context())
		if claims.HasAnyRole(security.RoleOperations) {
			out := claimTriageBody{View: "operations", ClaimID: v.ClaimID, State: string(v.State),
				History: make([]triageHistoryBody, 0, len(v.History))}
			if v.Current != nil {
				out.Current = assessmentBody(*v.Current)
			}
			for _, r := range v.History {
				out.History = append(out.History, historyBody(r))
			}
			writeJSON(w, http.StatusOK, out)
			return
		}
		out := insuredTriageBody{View: "insured", ClaimID: v.ClaimID, State: string(v.State)}
		if c := v.Current; c != nil {
			level, days, at := int(c.Assessment.Level), c.Assessment.Target.ResolutionBusinessDays, c.AssessedAt.UTC()
			out.Level, out.EstimatedResolutionBusinessDays, out.AssessedAt = &level, &days, &at
		}
		writeJSON(w, http.StatusOK, out)
	}
}

type triageSummaryBody struct {
	ClaimID    string            `json:"claim_id"`
	State      string            `json:"state"`
	Level      *int              `json:"level"`
	Severity   *string           `json:"severity"`
	AmountBand *string           `json:"amount_band"`
	Target     *triageTargetBody `json:"target"`
	AssessedAt *time.Time        `json:"assessed_at"`
}

type triageListBody struct {
	Items []triageSummaryBody `json:"items"`
}

func triageBatch(svc TriageReader) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ids := req.URL.Query()["claim_id"]
		if len(ids) == 0 || len(ids) > maxTriageBatch {
			writeError(w, http.StatusBadRequest, "invalid_request", "claim_id must be given between 1 and 50 times")
			return
		}
		seen := make(map[string]bool, len(ids))
		for _, id := range ids {
			if !claimIDPattern.MatchString(id) || seen[id] {
				writeError(w, http.StatusBadRequest, "invalid_request", "every claim_id must be a claim id, without repeats")
				return
			}
			seen[id] = true
		}
		summaries, err := svc.Summaries(req.Context(), ids)
		if writeTriageError(w, req, err) {
			return
		}
		out := triageListBody{Items: make([]triageSummaryBody, 0, len(summaries))}
		for _, s := range summaries {
			out.Items = append(out.Items, summaryOfTriage(s.ClaimID, s.State, s.Current))
		}
		writeJSON(w, http.StatusOK, out)
	}
}

type severityRuleBody struct {
	Line         string `json:"line"`
	IncidentType string `json:"incident_type"`
	Severity     string `json:"severity"`
}

type levelTargetBody struct {
	Level  int              `json:"level"`
	Target triageTargetBody `json:"target"`
}

type amountThresholdsBody struct {
	LowBelowMinor  int64 `json:"low_below_minor"`
	HighAboveMinor int64 `json:"high_above_minor"`
}

type triagePolicyBody struct {
	Version          string               `json:"version"`
	Currency         string               `json:"currency"`
	AmountThresholds amountThresholdsBody `json:"amount_thresholds"`
	Severity         []severityRuleBody   `json:"severity"`
	Targets          []levelTargetBody    `json:"targets"`
}

func triagePolicy(svc TriageReader) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		p := svc.Policy()
		out := triagePolicyBody{
			Version: p.Version, Currency: domain.TriageCurrency,
			AmountThresholds: amountThresholdsBody{LowBelowMinor: p.LowBelowMinor, HighAboveMinor: p.HighAboveMinor},
			Severity:         make([]severityRuleBody, 0, len(p.Severity)),
			Targets:          make([]levelTargetBody, 0, len(p.Targets)),
		}
		for _, r := range p.Severity {
			out.Severity = append(out.Severity, severityRuleBody{
				Line: string(r.Line), IncidentType: string(r.Incident), Severity: string(r.Severity)})
		}
		for _, t := range p.Targets {
			out.Targets = append(out.Targets, levelTargetBody{Level: int(t.Level), Target: targetBody(t.Target)})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// writeTriageError answers a failed read of the triage and tells whether it
// did. The log carries the error only: never the amount nor the insured.
func writeTriageError(w http.ResponseWriter, req *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ports.ErrClaimNotFound):
		writeError(w, http.StatusNotFound, "claim_not_found", "there is no claim with that id")
	case errors.Is(err, security.ErrForbidden), errors.Is(err, security.ErrUnauthenticated):
		security.WriteForbidden(w)
	default:
		slog.ErrorContext(req.Context(), "could not read the triage", "error", err)
		writeError(w, http.StatusServiceUnavailable, "claims_unavailable", "the triage could not be read; retry")
	}
	return true
}
