package httpadapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assessors"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Interventions is what the routes of the follow-up of the intervention and
// the closing of the case use (HU-W2.3-02).
type Interventions interface {
	View(ctx context.Context, claimID string) (assessors.InterventionView, error)
	RecordStep(ctx context.Context, claimID string, in domain.StepRequest, by domain.Analyst) (assessors.InterventionView, error)
	Close(ctx context.Context, claimID string, in domain.CloseRequest, by domain.Analyst) (assessors.InterventionView, error)
}

// WithIntervention mounts the follow-up of the intervention of the provider
// and the closing of the case (HU-W2.3-02). Only operations, as the
// assignment it continues.
func WithIntervention(svc Interventions, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleOperations))
			r.Get("/v1/claims/{claim_id}/intervention", claimIntervention(svc))
			r.Post("/v1/claims/{claim_id}/intervention/steps", recordInterventionStep(svc))
			r.Post("/v1/claims/{claim_id}/closure", closeCase(svc))
		})
	}
}

type providerReportBody struct {
	Cause           string `json:"cause"`
	Damage          string `json:"damage"`
	RepairCostMinor int64  `json:"repair_cost_minor"`
	RepairDays      int    `json:"repair_days"`
	Photos          int    `json:"photos"`
}

type interventionStepBody struct {
	Step       string              `json:"step"`
	OccurredAt time.Time           `json:"occurred_at"`
	Note       *string             `json:"note"`
	Report     *providerReportBody `json:"report"`
	RecordedBy actorBody           `json:"recorded_by"`
	RecordedAt time.Time           `json:"recorded_at"`
}

type interventionProviderBody struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	ContactEmail string  `json:"contact_email"`
	ContactPhone *string `json:"contact_phone"`
	DistanceKm   float64 `json:"distance_km"`
}

type interventionAssignmentBody struct {
	Provider   interventionProviderBody `json:"provider"`
	AssignedBy actorBody                `json:"assigned_by"`
	AssignedAt time.Time                `json:"assigned_at"`
}

type caseClosureBody struct {
	Result     string    `json:"result"`
	ClosedAt   time.Time `json:"closed_at"`
	Notes      *string   `json:"notes"`
	ClosedBy   actorBody `json:"closed_by"`
	RecordedAt time.Time `json:"recorded_at"`
}

type interventionBody struct {
	ClaimID      string                      `json:"claim_id"`
	FilingNumber string                      `json:"filing_number"`
	ClaimStatus  string                      `json:"claim_status"`
	IncidentType string                      `json:"incident_type"`
	PolicyID     string                      `json:"policy_id"`
	Place        string                      `json:"place"`
	State        string                      `json:"state"`
	Assignment   *interventionAssignmentBody `json:"assignment"`
	Steps        []interventionStepBody      `json:"steps"`
	NextStep     *string                     `json:"next_step"`
	Report       *providerReportBody         `json:"report"`
	Closure      *caseClosureBody            `json:"closure"`
	Closable     bool                        `json:"closable"`
	Missing      []string                    `json:"missing"`
}

func reportBodyOf(r *domain.ProviderReport) *providerReportBody {
	if r == nil {
		return nil
	}
	return &providerReportBody{Cause: r.Cause, Damage: r.Damage, RepairCostMinor: r.RepairCostMinor,
		RepairDays: r.RepairDays, Photos: r.Photos}
}

func stepNames(steps []domain.InterventionStep) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, string(s))
	}
	return out
}

func interventionBodyOf(v assessors.InterventionView) interventionBody {
	out := interventionBody{ClaimID: v.Claim.ID, FilingNumber: v.Claim.FilingNumber,
		ClaimStatus: string(v.Claim.Status), IncidentType: string(v.Claim.IncidentType), PolicyID: v.Claim.PolicyID,
		Place: v.Claim.Place, State: string(v.State), Steps: make([]interventionStepBody, 0, len(v.Steps)),
		NextStep: nullable(string(v.Next)), Report: reportBodyOf(v.Report), Closable: v.Closable(),
		Missing: stepNames(v.Missing)}
	if a := v.Assignment; a != nil {
		out.Assignment = &interventionAssignmentBody{
			Provider: interventionProviderBody{ID: a.ProviderID, Name: a.ProviderName, ContactEmail: a.ContactEmail,
				ContactPhone: a.ContactPhone, DistanceKm: domain.RoundKm(a.DistanceKm)},
			AssignedBy: actorOf(a.AssignedBy), AssignedAt: a.AssignedAt.UTC()}
	}
	for _, s := range v.Steps {
		out.Steps = append(out.Steps, interventionStepBody{Step: string(s.Step), OccurredAt: s.OccurredAt.UTC(),
			Note: nullable(s.Note), Report: reportBodyOf(s.Report), RecordedBy: actorOf(s.RecordedBy),
			RecordedAt: s.RecordedAt.UTC()})
	}
	if c := v.Closure; c != nil {
		out.Closure = &caseClosureBody{Result: string(c.Result), ClosedAt: c.ClosedAt.UTC(), Notes: nullable(c.Notes),
			ClosedBy: actorOf(c.ClosedBy), RecordedAt: c.RecordedAt.UTC()}
	}
	return out
}

func claimIntervention(svc Interventions) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := claimIDOf(w, req)
		if !ok {
			return
		}
		v, err := svc.View(req.Context(), id)
		if writeInterventionError(w, req, id, err) {
			return
		}
		writeJSON(w, http.StatusOK, interventionBodyOf(v))
	}
}

type reportRequest struct {
	Cause           string `json:"cause"`
	Damage          string `json:"damage"`
	RepairCostMinor *int64 `json:"repair_cost_minor"`
	RepairDays      *int   `json:"repair_days"`
	Photos          *int   `json:"photos"`
}

type stepRequest struct {
	Step       string         `json:"step"`
	OccurredAt *time.Time     `json:"occurred_at"`
	Note       string         `json:"note"`
	Report     *reportRequest `json:"report"`
}

func recordInterventionStep(svc Interventions) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := claimIDOf(w, req)
		if !ok {
			return
		}
		var body stepRequest
		if !decodeStrict(w, req, &body) {
			return
		}
		in := domain.StepRequest{Step: domain.InterventionStep(body.Step), OccurredAt: body.OccurredAt,
			Note: body.Note}
		if r := body.Report; r != nil {
			in.Report = &domain.ReportRequest{Cause: r.Cause, Damage: r.Damage, RepairCostMinor: r.RepairCostMinor,
				RepairDays: r.RepairDays, Photos: r.Photos}
		}
		v, err := svc.RecordStep(req.Context(), id, in, analystFrom(req))
		if writeInterventionError(w, req, id, err) {
			return
		}
		writeJSON(w, http.StatusCreated, interventionBodyOf(v))
	}
}

type caseCloseRequest struct {
	Result   string     `json:"result"`
	ClosedAt *time.Time `json:"closed_at"`
	Notes    string     `json:"notes"`
}

func closeCase(svc Interventions) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := claimIDOf(w, req)
		if !ok {
			return
		}
		var body caseCloseRequest
		if !decodeStrict(w, req, &body) {
			return
		}
		v, err := svc.Close(req.Context(), id, domain.CloseRequest{Result: domain.ClosingResult(body.Result),
			ClosedAt: body.ClosedAt, Notes: body.Notes}, analystFrom(req))
		if writeInterventionError(w, req, id, err) {
			return
		}
		writeJSON(w, http.StatusCreated, interventionBodyOf(v))
	}
}

// notFinishedResponse is the closing refused because the intervention is not
// finished, with the steps still missing (criterion 4).
type notFinishedResponse struct {
	Error   string   `json:"error"`
	Message string   `json:"message"`
	Missing []string `json:"missing"`
}

// interventionErrors are the errors of the intervention the analyst can act
// on, with the status and the code of the contract.
var interventionErrors = []struct {
	err     error
	status  int
	code    string
	message string
}{
	{ports.ErrClaimNotFound, http.StatusNotFound, "claim_not_found", "there is no claim with that id"},
	{domain.ErrNotAssigned, http.StatusConflict, "not_assigned", "the claim has no provider assigned"},
	{domain.ErrCaseClosed, http.StatusConflict, "case_closed", "the case is closed already"},
	{domain.ErrStepOutOfOrder, http.StatusConflict, "step_out_of_order",
		"the step is not the next one of the intervention"},
}

// writeInterventionError answers the error of the intervention and tells
// whether there was one.
func writeInterventionError(w http.ResponseWriter, req *http.Request, claimID string, err error) bool {
	if err == nil {
		return false
	}
	var invalid *domain.ValidationError
	if errors.As(err, &invalid) {
		writeValidationError(w, invalid)
		return true
	}
	if errors.Is(err, domain.ErrInterventionNotFinished) {
		missing := []string{string(domain.StepRepairExecuted)}
		var notFinished *domain.NotFinishedError
		if errors.As(err, &notFinished) {
			missing = stepNames(notFinished.Missing)
		}
		writeJSON(w, http.StatusConflict, notFinishedResponse{Error: "intervention_not_finished",
			Message: "the provider has not reported the intervention finished", Missing: missing})
		return true
	}
	for _, e := range interventionErrors {
		if errors.Is(err, e.err) {
			writeError(w, e.status, e.code, e.message)
			return true
		}
	}
	slog.ErrorContext(req.Context(), "could not serve the intervention of the provider", "claim_id", claimID,
		"error", err)
	writeError(w, http.StatusServiceUnavailable, "claims_unavailable", "the intervention could not be served; retry")
	return true
}
