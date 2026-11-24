package httpadapter

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assessors"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Assigner is what the routes of the assignment of the provider use
// (HU-W2.3-01).
type Assigner interface {
	View(ctx context.Context, claimID string) (assessors.View, error)
	Summaries(ctx context.Context, ids []string) ([]assessors.Summary, error)
	RecordLocation(ctx context.Context, claimID string, in domain.LocationRequest, by domain.Analyst) (assessors.View, error)
	Search(ctx context.Context, claimID string, fromRadiusKm *float64, by domain.Analyst) (assessors.View, error)
	Assign(ctx context.Context, claimID, providerID string, searchSeq int, by domain.Analyst) (assessors.View, error)
	Policy() domain.AssignmentPolicy
}

// maxAssignmentBatch is the most claims a batch read asks for: a page of the
// lists of claims.
const maxAssignmentBatch = 50

// WithAssignment mounts the assignment of the nearest provider to a claim
// (HU-W2.3-01): its state, the location of the incident, the search, the
// confirmation, a page of claims and the policy. retryEvery is how often a
// claim pending assignment is searched again, which the policy tells the
// front. Only operations (assumption S14).
func WithAssignment(svc Assigner, retryEvery time.Duration, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleOperations))
			r.Get("/v1/claims/assignments", assignmentBatch(svc))
			r.Get("/v1/claims/assignment-policy", assignmentPolicy(svc, retryEvery))
			r.Get("/v1/claims/{claim_id}/assignment", claimAssignment(svc))
			r.Post("/v1/claims/{claim_id}/assignment", assignProvider(svc))
			r.Post("/v1/claims/{claim_id}/incident-location", recordIncidentLocation(svc))
			r.Post("/v1/claims/{claim_id}/provider-searches", searchProvider(svc))
		})
	}
}

type actorBody struct {
	Email string  `json:"email"`
	Name  *string `json:"name"`
}

func actorOf(a domain.Analyst) actorBody { return actorBody{Email: a.Email, Name: nullable(a.Name)} }

func optionalActor(a *domain.Analyst) *actorBody {
	if a == nil {
		return nil
	}
	body := actorOf(*a)
	return &body
}

type incidentLocationBody struct {
	Source     string     `json:"source"`
	Latitude   float64    `json:"latitude"`
	Longitude  float64    `json:"longitude"`
	AccuracyM  *float64   `json:"accuracy_m"`
	Address    *string    `json:"address"`
	RecordedBy *actorBody `json:"recorded_by"`
	RecordedAt time.Time  `json:"recorded_at"`
}

type searchLocationBody struct {
	Source    string  `json:"source"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type searchAttemptBody struct {
	RadiusKm      float64   `json:"radius_km"`
	EligibleCount int       `json:"eligible_count"`
	AttemptedAt   time.Time `json:"attempted_at"`
}

type candidateBody struct {
	ProviderID       string   `json:"provider_id"`
	Name             string   `json:"name"`
	Specialties      []string `json:"specialties"`
	DistanceKm       float64  `json:"distance_km"`
	Availability     string   `json:"availability"`
	UnavailableUntil *string  `json:"unavailable_until"`
	Eligibility      string   `json:"eligibility"`
	Suggested        bool     `json:"suggested"`
}

type providerSearchBody struct {
	Seq           int                 `json:"seq"`
	Trigger       string              `json:"trigger"`
	SearchedBy    *actorBody          `json:"searched_by"`
	Specialty     string              `json:"specialty"`
	Location      searchLocationBody  `json:"location"`
	FromRadiusKm  float64             `json:"from_radius_km"`
	Outcome       string              `json:"outcome"`
	RadiusKm      float64             `json:"radius_km"`
	Reason        *string             `json:"reason"`
	PolicyVersion string              `json:"policy_version"`
	SearchedAt    time.Time           `json:"searched_at"`
	Attempts      []searchAttemptBody `json:"attempts"`
	Candidates    []candidateBody     `json:"candidates"`
}

type assignedProviderBody struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	ContactEmail string  `json:"contact_email"`
	ContactPhone *string `json:"contact_phone"`
}

type sharedLocationBody struct {
	Source    string   `json:"source"`
	Latitude  float64  `json:"latitude"`
	Longitude float64  `json:"longitude"`
	AccuracyM *float64 `json:"accuracy_m"`
	Address   *string  `json:"address"`
}

type noticeBody struct {
	Status   string     `json:"status"`
	Attempts int        `json:"attempts"`
	SentAt   *time.Time `json:"sent_at"`
}

type assignmentBody struct {
	Provider       assignedProviderBody `json:"provider"`
	AssignedBy     actorBody            `json:"assigned_by"`
	AssignedAt     time.Time            `json:"assigned_at"`
	DistanceKm     float64              `json:"distance_km"`
	SearchSeq      int                  `json:"search_seq"`
	LocationShared sharedLocationBody   `json:"location_shared"`
	PolicyVersion  string               `json:"policy_version"`
	Notice         noticeBody           `json:"notice"`
}

type claimAssignmentBody struct {
	ClaimID       string                `json:"claim_id"`
	FilingNumber  string                `json:"filing_number"`
	ClaimStatus   string                `json:"claim_status"`
	IncidentType  string                `json:"incident_type"`
	Line          string                `json:"line"`
	State         string                `json:"state"`
	Specialty     *string               `json:"specialty"`
	PolicyVersion string                `json:"policy_version"`
	Location      *incidentLocationBody `json:"location"`
	LastSearch    *providerSearchBody   `json:"last_search"`
	Assignment    *assignmentBody       `json:"assignment"`
}

// day is the date of t in Colombia, or null.
func day(t *time.Time) *string {
	if t == nil {
		return nil
	}
	d := t.In(domain.ColombiaTime).Format(time.DateOnly)
	return &d
}

func specialtyNames(specialties []domain.Specialty) []string {
	out := make([]string, 0, len(specialties))
	for _, s := range specialties {
		out = append(out, string(s))
	}
	return out
}

func searchBodyOf(r domain.ProviderSearch) *providerSearchBody {
	out := &providerSearchBody{Seq: r.Seq, Trigger: string(r.Trigger), SearchedBy: optionalActor(r.SearchedBy),
		Specialty: string(r.Specialty), Location: searchLocationBody{Source: string(r.Location.Source),
			Latitude: r.Location.Point.Latitude, Longitude: r.Location.Point.Longitude},
		FromRadiusKm: r.Result.FromRadiusKm, Outcome: string(r.Result.Outcome), RadiusKm: r.Result.RadiusKm,
		Reason: nullable(r.Result.Reason), PolicyVersion: r.PolicyVersion, SearchedAt: r.SearchedAt.UTC(),
		Attempts:   make([]searchAttemptBody, 0, len(r.Result.Attempts)),
		Candidates: make([]candidateBody, 0, len(r.Result.Candidates))}
	for _, a := range r.Result.Attempts {
		out.Attempts = append(out.Attempts, searchAttemptBody{RadiusKm: a.RadiusKm, EligibleCount: a.EligibleCount,
			AttemptedAt: a.AttemptedAt.UTC()})
	}
	for _, c := range r.Result.Candidates {
		out.Candidates = append(out.Candidates, candidateBody{ProviderID: c.ProviderID, Name: c.Name,
			Specialties: specialtyNames(c.Specialties), DistanceKm: domain.RoundKm(c.DistanceKm),
			Availability: string(c.Availability), UnavailableUntil: day(c.UnavailableUntil),
			Eligibility: string(c.Eligibility), Suggested: c.Suggested})
	}
	return out
}

func assignmentBodyOf(a domain.Assignment, n *ports.ProviderNotice) *assignmentBody {
	out := &assignmentBody{
		Provider: assignedProviderBody{ID: a.ProviderID, Name: a.ProviderName, ContactEmail: a.ContactEmail,
			ContactPhone: a.ContactPhone},
		AssignedBy: actorOf(a.AssignedBy), AssignedAt: a.AssignedAt.UTC(), DistanceKm: domain.RoundKm(a.DistanceKm),
		SearchSeq: a.SearchSeq, PolicyVersion: a.PolicyVersion,
		LocationShared: sharedLocationBody{Source: string(a.Location.Source), Latitude: a.Location.Point.Latitude,
			Longitude: a.Location.Point.Longitude, AccuracyM: a.Location.AccuracyM, Address: nullable(a.Location.Address)},
		Notice: noticeBody{Status: string(ports.NoticePending)},
	}
	if n != nil {
		out.Notice = noticeBody{Status: string(n.Status), Attempts: n.Attempts, SentAt: optionalTime(n.SentAt)}
	}
	return out
}

func claimAssignmentBodyOf(v assessors.View) claimAssignmentBody {
	out := claimAssignmentBody{ClaimID: v.Claim.ID, FilingNumber: v.Claim.FilingNumber,
		ClaimStatus: string(v.Claim.Status), IncidentType: string(v.Claim.IncidentType), Line: string(v.Line),
		State: string(v.State), Specialty: nullable(string(v.Specialty)), PolicyVersion: v.PolicyVersion}
	if l := v.Location; l != nil {
		out.Location = &incidentLocationBody{Source: string(l.Source), Latitude: l.Point.Latitude,
			Longitude: l.Point.Longitude, AccuracyM: l.AccuracyM, Address: nullable(l.Address),
			RecordedBy: optionalActor(l.RecordedBy), RecordedAt: l.RecordedAt.UTC()}
	}
	if v.LastSearch != nil {
		out.LastSearch = searchBodyOf(*v.LastSearch)
	}
	if v.Assignment != nil {
		out.Assignment = assignmentBodyOf(*v.Assignment, v.Notice)
	}
	return out
}

// analystFrom is the analyst who acts, from the token: the e-mail the history
// keeps and the name the screens show.
func analystFrom(req *http.Request) domain.Analyst {
	c, _ := security.ClaimsFromContext(req.Context())
	return domain.Analyst{Email: analystOf(req), Name: c.Name}
}

func claimAssignment(svc Assigner) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := claimIDOf(w, req)
		if !ok {
			return
		}
		v, err := svc.View(req.Context(), id)
		if writeAssignmentError(w, req, id, err) {
			return
		}
		writeJSON(w, http.StatusOK, claimAssignmentBodyOf(v))
	}
}

type incidentLocationRequest struct {
	Address   string   `json:"address"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}

func recordIncidentLocation(svc Assigner) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := claimIDOf(w, req)
		if !ok {
			return
		}
		var body incidentLocationRequest
		if !decodeStrict(w, req, &body) {
			return
		}
		v, err := svc.RecordLocation(req.Context(), id, domain.LocationRequest{Address: body.Address,
			Latitude: body.Latitude, Longitude: body.Longitude}, analystFrom(req))
		if writeAssignmentError(w, req, id, err) {
			return
		}
		writeJSON(w, http.StatusCreated, claimAssignmentBodyOf(v))
	}
}

type providerSearchRequest struct {
	FromRadiusKm *float64 `json:"from_radius_km"`
}

func searchProvider(svc Assigner) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := claimIDOf(w, req)
		if !ok {
			return
		}
		var body providerSearchRequest
		if !decodeOptional(w, req, &body) {
			return
		}
		v, err := svc.Search(req.Context(), id, body.FromRadiusKm, analystFrom(req))
		if writeAssignmentError(w, req, id, err) {
			return
		}
		writeJSON(w, http.StatusCreated, claimAssignmentBodyOf(v))
	}
}

// decodeOptional reads a JSON object as decodeStrict does, and accepts an
// empty body: the search without one starts from the initial radius.
func decodeOptional(w http.ResponseWriter, req *http.Request, v any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, req.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid")
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	req.Body = io.NopCloser(bytes.NewReader(raw))
	return decodeStrict(w, req, v)
}

type assignmentRequest struct {
	ProviderID *string `json:"provider_id"`
	SearchSeq  *int    `json:"search_seq"`
}

// assignmentRequestOf checks the fields of a confirmation, every one at once.
func assignmentRequestOf(in assignmentRequest) (string, int, error) {
	var fields []domain.FieldError
	switch {
	case in.ProviderID == nil:
		fields = append(fields, domain.FieldError{Field: "provider_id", Code: domain.CodeRequired})
	case !domain.ValidProviderID(*in.ProviderID):
		fields = append(fields, domain.FieldError{Field: "provider_id", Code: domain.CodeInvalidFormat})
	}
	switch {
	case in.SearchSeq == nil:
		fields = append(fields, domain.FieldError{Field: "search_seq", Code: domain.CodeRequired})
	case *in.SearchSeq < 1:
		fields = append(fields, domain.FieldError{Field: "search_seq", Code: domain.CodeOutOfRange})
	}
	if len(fields) > 0 {
		return "", 0, &domain.ValidationError{Fields: fields}
	}
	return *in.ProviderID, *in.SearchSeq, nil
}

func assignProvider(svc Assigner) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := claimIDOf(w, req)
		if !ok {
			return
		}
		var body assignmentRequest
		if !decodeStrict(w, req, &body) {
			return
		}
		providerID, seq, err := assignmentRequestOf(body)
		if err == nil {
			var v assessors.View
			v, err = svc.Assign(req.Context(), id, providerID, seq, analystFrom(req))
			if err == nil {
				writeJSON(w, http.StatusCreated, claimAssignmentBodyOf(v))
				return
			}
		}
		writeAssignmentError(w, req, id, err)
	}
}

type assignmentSummaryBody struct {
	ClaimID      string     `json:"claim_id"`
	State        string     `json:"state"`
	Specialty    *string    `json:"specialty"`
	ProviderName *string    `json:"provider_name"`
	DistanceKm   *float64   `json:"distance_km"`
	AssignedAt   *time.Time `json:"assigned_at"`
	Reason       *string    `json:"reason"`
}

type assignmentListBody struct {
	Items []assignmentSummaryBody `json:"items"`
}

func assignmentBatch(svc Assigner) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ids := req.URL.Query()["claim_id"]
		if len(ids) == 0 || len(ids) > maxAssignmentBatch {
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
		if writeAssignmentError(w, req, "", err) {
			return
		}
		out := assignmentListBody{Items: make([]assignmentSummaryBody, 0, len(summaries))}
		for _, s := range summaries {
			item := assignmentSummaryBody{ClaimID: s.ClaimID, State: string(s.State),
				Specialty: nullable(string(s.Specialty)), ProviderName: nullable(s.ProviderName),
				AssignedAt: optionalTime(s.AssignedAt), Reason: nullable(s.Reason)}
			if s.DistanceKm != nil {
				distance := domain.RoundKm(*s.DistanceKm)
				item.DistanceKm = &distance
			}
			out.Items = append(out.Items, item)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

type specialtyRuleBody struct {
	Line         string  `json:"line"`
	IncidentType string  `json:"incident_type"`
	Specialty    *string `json:"specialty"`
}

type assignmentPolicyBody struct {
	Version              string              `json:"version"`
	RadiiKm              []float64           `json:"radii_km"`
	InitialRadiusKm      float64             `json:"initial_radius_km"`
	MaxRadiusKm          float64             `json:"max_radius_km"`
	Specialties          []specialtyRuleBody `json:"specialties"`
	RetryIntervalSeconds int                 `json:"retry_interval_seconds"`
}

func assignmentPolicy(svc Assigner, retryEvery time.Duration) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		p := svc.Policy()
		out := assignmentPolicyBody{Version: p.Version, RadiiKm: p.RadiiKm, InitialRadiusKm: p.InitialRadiusKm(),
			MaxRadiusKm: p.MaxRadiusKm(), Specialties: make([]specialtyRuleBody, 0, len(p.Specialties)),
			RetryIntervalSeconds: int(retryEvery / time.Second)}
		for _, r := range p.Specialties {
			out.Specialties = append(out.Specialties, specialtyRuleBody{Line: string(r.Line),
				IncidentType: string(r.Incident), Specialty: nullable(string(r.Specialty))})
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// assignmentErrors are the errors of the assignment the analyst can act on,
// with the status and the code of the contract.
var assignmentErrors = []struct {
	err     error
	status  int
	code    string
	message string
}{
	{ports.ErrClaimNotFound, http.StatusNotFound, "claim_not_found", "there is no claim with that id"},
	{domain.ErrLocationFromApp, http.StatusConflict, "location_from_app", "the claim has trusted coordinates from the app"},
	{domain.ErrAssignmentExists, http.StatusConflict, "assignment_exists", "the claim already has its provider"},
	{domain.ErrClaimNotAssignable, http.StatusConflict, "claim_not_assignable",
		"the claim is not in evaluation or needs no on-site visit"},
	{domain.ErrProviderUnavailable, http.StatusConflict, "provider_unavailable", "the provider is no longer available"},
	{domain.ErrSearchOutdated, http.StatusConflict, "search_outdated", "the search is not the last one of the claim"},
	{domain.ErrLocationMissing, http.StatusUnprocessableEntity, "location_missing",
		"the claim has no location to search from"},
	{domain.ErrProviderNotEligible, http.StatusUnprocessableEntity, "provider_not_eligible",
		"the provider is not eligible in that search"},
}

// writeAssignmentError answers the error of the assignment and tells whether
// there was one. The log names the claim, never the provider's contact nor
// the coordinates.
func writeAssignmentError(w http.ResponseWriter, req *http.Request, claimID string, err error) bool {
	if err == nil {
		return false
	}
	var invalid *domain.ValidationError
	if errors.As(err, &invalid) {
		writeValidationError(w, invalid)
		return true
	}
	for _, e := range assignmentErrors {
		if errors.Is(err, e.err) {
			writeError(w, e.status, e.code, e.message)
			return true
		}
	}
	slog.ErrorContext(req.Context(), "could not serve the assignment of the provider", "claim_id", claimID, "error", err)
	writeError(w, http.StatusServiceUnavailable, "claims_unavailable", "the assignment could not be served; retry")
	return true
}
