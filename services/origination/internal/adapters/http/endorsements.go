package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/endorsements"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// EndorsementReviewService is the use case of the operations analyst
// (HU-W1.4-02).
type EndorsementReviewService interface {
	Inbox(ctx context.Context, f domain.EndorsementInboxFilter) (domain.EndorsementInboxPage, error)
	Detail(ctx context.Context, requestID string) (endorsements.Detail, error)
	Review(ctx context.Context, requestID string) (domain.ChangeRequest, error)
	Apply(ctx context.Context, requestID string) (endorsements.ApplyResult, error)
	Reject(ctx context.Context, requestID, observations string) (domain.ChangeRequest, error)
	Return(ctx context.Context, requestID, observations string) (domain.ChangeRequest, error)
	History(ctx context.Context, policyID string) (domain.Policy, []domain.EndorsementHistoryEntry, error)
}

// WithEndorsementReview mounts the review of the change requests by the
// operations analyst (HU-W1.4-02, screens O2, O3, O3b and O4): the inbox, the
// detail, taking a request, applying, rejecting and returning it, and the
// history of the endorsements of a policy. They are human routes for the
// operations role only. The fixed segment "endorsement-requests" never covers
// the routes of a policy, which always have a segment after the id. Register
// it after WithObservability.
func WithEndorsementReview(svc EndorsementReviewService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleOperations))
			r.Get("/v1/policies/endorsement-requests", listEndorsementRequests(svc))
			r.Get("/v1/policies/endorsement-requests/{request_id}", getEndorsementRequest(svc))
			r.Post("/v1/policies/endorsement-requests/{request_id}/review", reviewEndorsementRequest(svc))
			r.Post("/v1/policies/endorsement-requests/{request_id}/application", applyEndorsementRequest(svc))
			r.Post("/v1/policies/endorsement-requests/{request_id}/rejection", closeEndorsementRequest(svc.Reject))
			r.Post("/v1/policies/endorsement-requests/{request_id}/return", closeEndorsementRequest(svc.Return))
			r.Get("/v1/policies/{policy_id}/endorsements", getEndorsementHistory(svc))
		})
	}
}

type holderBody struct {
	FullName   string `json:"full_name"`
	DocumentID string `json:"document_id"`
}

// endorsementRequestBody is a request as the analyst reads it: never the
// subject nor the e-mail of the insured (the contact data travel only in the
// changes of a change of personal data).
type endorsementRequestBody struct {
	RequestID       string    `json:"request_id"`
	PolicyID        string    `json:"policy_id"`
	ChangeType      string    `json:"change_type"`
	Channel         string    `json:"channel"`
	FiledAt         time.Time `json:"filed_at"`
	Status          string    `json:"status"`
	StatusChangedAt time.Time `json:"status_changed_at"`
	// Returned tells a not applied request returned to the insured.
	Returned       bool    `json:"returned"`
	DecisionReason *string `json:"decision_reason"`
	RequesterName  *string `json:"requester_name"`
}

func toEndorsementRequestBody(r domain.ChangeRequest) endorsementRequestBody {
	return endorsementRequestBody{RequestID: r.ID, PolicyID: r.PolicyID, ChangeType: string(r.Type), Channel: string(r.Channel),
		FiledAt: r.FiledAt.UTC(), Status: string(r.Status), StatusChangedAt: r.StatusChangedAt.UTC(), Returned: r.Returned(),
		DecisionReason: optionalString(r.DecisionReason), RequesterName: optionalString(r.RequestedBy.Name)}
}

type inboxItemBody struct {
	endorsementRequestBody
	Holder holderBody `json:"holder"`
}

type pendingIndicatorBody struct {
	Count   int `json:"count"`
	Overdue int `json:"overdue"`
}

type appliedTodayBody struct {
	Count                  int      `json:"count"`
	MeanTimeToApplySeconds *float64 `json:"mean_time_to_apply_seconds"`
}

type rejectedRecentlyBody struct {
	Count     int     `json:"count"`
	TopReason *string `json:"top_reason"`
}

type inboxIndicatorsBody struct {
	Pending          pendingIndicatorBody `json:"pending"`
	AppliedToday     appliedTodayBody     `json:"applied_today"`
	RejectedLast7Day rejectedRecentlyBody `json:"rejected_last_7_days"`
}

type inboxResponse struct {
	Items      []inboxItemBody     `json:"items"`
	Page       int                 `json:"page"`
	PageSize   int                 `json:"page_size"`
	TotalItems int                 `json:"total_items"`
	TotalPages int                 `json:"total_pages"`
	Indicators inboxIndicatorsBody `json:"indicators"`
}

func toInboxResponse(p domain.EndorsementInboxPage) inboxResponse {
	ind := p.Indicators
	out := inboxResponse{Items: make([]inboxItemBody, 0, len(p.Items)), Page: p.Page, PageSize: p.PageSize, TotalItems: p.Total,
		TotalPages: p.TotalPages(), Indicators: inboxIndicatorsBody{
			Pending:          pendingIndicatorBody{Count: ind.Pending, Overdue: ind.PendingOverdue},
			AppliedToday:     appliedTodayBody{Count: ind.AppliedToday},
			RejectedLast7Day: rejectedRecentlyBody{Count: ind.RejectedRecently, TopReason: optionalString(ind.TopReason)},
		}}
	if ind.MeanTimeToApply != nil {
		s := ind.MeanTimeToApply.Seconds()
		out.Indicators.AppliedToday.MeanTimeToApplySeconds = &s
	}
	for _, it := range p.Items {
		out.Items = append(out.Items, inboxItemBody{endorsementRequestBody: toEndorsementRequestBody(it.Request),
			Holder: holderBody{FullName: it.HolderName, DocumentID: it.HolderDocument}})
	}
	return out
}

// inboxFilterOf reads the query of the inbox and names every parameter it
// cannot read.
func inboxFilterOf(req *http.Request) (domain.EndorsementInboxFilter, []domain.FieldError) {
	v := req.URL.Query()
	var (
		f      domain.EndorsementInboxFilter
		fields []domain.FieldError
	)
	bad := func(field, code string) { fields = append(fields, domain.FieldError{Field: field, Code: code}) }
	if raw := strings.TrimSpace(v.Get("change_type")); raw != "" {
		t, ok := domain.ParseChangeType(raw)
		if !ok {
			bad("change_type", domain.CodeInvalidFormat)
		}
		f.ChangeType = t
	}
	status, err := domain.ParseInboxStatus(v.Get("status"))
	if err != nil {
		bad("status", domain.CodeInvalidFormat)
	}
	f.Status = status
	if raw := strings.TrimSpace(v.Get("filed_from")); raw != "" {
		day, err := domain.ParseCivilDay(raw)
		if err != nil {
			bad("filed_from", domain.CodeInvalidFormat)
		}
		f.FiledFrom = day
	}
	for _, n := range []struct {
		name string
		max  int
		dest *int
	}{{"page", 1<<31 - 1, &f.Page}, {"page_size", domain.MaxInboxPageSize, &f.PageSize}} {
		raw := strings.TrimSpace(v.Get(n.name))
		if raw == "" {
			continue
		}
		value, err := strconv.Atoi(raw)
		switch {
		case err != nil:
			bad(n.name, domain.CodeInvalidFormat)
		case value < 1 || value > n.max:
			bad(n.name, domain.CodeOutOfRange)
		default:
			*n.dest = value
		}
	}
	return f, fields
}

func listEndorsementRequests(svc EndorsementReviewService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		f, fields := inboxFilterOf(req)
		if len(fields) > 0 {
			writeQueryValidationError(w, fields)
			return
		}
		page, err := svc.Inbox(req.Context(), f)
		if err != nil {
			respondEndorsementError(w, req, err)
			return
		}
		writeJSON(w, http.StatusOK, toInboxResponse(page))
	}
}

type validationBody struct {
	Code    string            `json:"code"`
	Outcome string            `json:"outcome"`
	Params  map[string]string `json:"params"`
}

func toValidationBodies(v domain.Validations) []validationBody {
	out := make([]validationBody, 0, len(v))
	for _, r := range v {
		params := r.Params
		if params == nil {
			params = map[string]string{}
		}
		out = append(out, validationBody{Code: string(r.Code), Outcome: string(r.Outcome), Params: params})
	}
	return out
}

func codesOf(codes []domain.ValidationCode) []string {
	out := make([]string, 0, len(codes))
	for _, c := range codes {
		out = append(out, string(c))
	}
	return out
}

type premiumChangeBody struct {
	Previous      float64  `json:"previous"`
	New           *float64 `json:"new"`
	Currency      string   `json:"currency"`
	RatingVersion *string  `json:"rating_version"`
}

type endorsementRefBody struct {
	EndorsementID string    `json:"endorsement_id"`
	ChangeType    string    `json:"change_type"`
	AppliedAt     time.Time `json:"applied_at"`
}

type policyEndorsementsBody struct {
	Count int                 `json:"count"`
	Last  *endorsementRefBody `json:"last"`
}

type endorsementPolicyBody struct {
	PolicyID           string                 `json:"policy_id"`
	Line               string                 `json:"line"`
	Holder             holderBody             `json:"holder"`
	InForce            bool                   `json:"in_force"`
	CoverageStart      time.Time              `json:"coverage_start"`
	CoverageEnd        time.Time              `json:"coverage_end"`
	InsuredAmount      float64                `json:"insured_amount"`
	Premium            float64                `json:"premium"`
	Currency           string                 `json:"currency"`
	OnerousBeneficiary *onerousBody           `json:"onerous_beneficiary"`
	Endorsements       policyEndorsementsBody `json:"endorsements"`
}

type actorBody struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

func toActorBody(a domain.Actor) actorBody {
	return actorBody{Name: a.DisplayName(), Role: string(security.RoleOperations)}
}

type decisionInfoBody struct {
	Outcome       string     `json:"outcome"`
	By            *actorBody `json:"by"`
	At            time.Time  `json:"at"`
	Observations  *string    `json:"observations"`
	EndorsementID *string    `json:"endorsement_id"`
	Failed        []string   `json:"failed"`
}

type endorsementDetailResponse struct {
	Request           endorsementRequestBody `json:"request"`
	Changes           []fieldChangeBody      `json:"changes"`
	Premium           *premiumChangeBody     `json:"premium"`
	Policy            endorsementPolicyBody  `json:"policy"`
	Validations       []validationBody       `json:"validations"`
	ValidationsSource string                 `json:"validations_source"`
	Decision          *decisionInfoBody      `json:"decision"`
}

func changesOf(r domain.ChangeRequest) []fieldChangeBody {
	out := []fieldChangeBody{}
	for _, c := range r.Changes() {
		out = append(out, fieldChangeBody{Field: c.Field, Previous: changeValue(c.Previous), Requested: changeValue(c.Requested)})
	}
	return out
}

// decisionOf is what was decided on the request, and by whom: the endorsement,
// the attempt not applied (and its return) or the rejection.
func decisionOf(r domain.ChangeRequest, rec domain.RequestRecord) *decisionInfoBody {
	failed := []string{}
	if a := rec.LastAttempt; a != nil && a.Outcome == domain.OutcomeNotApplied {
		failed = codesOf(a.Validations.Of(domain.ValidationFailed))
	}
	switch {
	case r.Status == domain.ChangeRequestApplied && rec.Endorsement != nil:
		e := rec.Endorsement
		by := toActorBody(e.By)
		return &decisionInfoBody{Outcome: string(domain.OutcomeApplied), By: &by, At: e.At, EndorsementID: optionalString(e.ID),
			Failed: failed}
	case r.Status == domain.ChangeRequestRejected:
		out := &decisionInfoBody{Outcome: string(domain.ChangeRequestRejected), At: r.StatusChangedAt,
			Observations: optionalString(r.DecisionReason), Failed: failed}
		if a := rec.Last(domain.ActionRejection); a != nil {
			by := toActorBody(a.By)
			out.By, out.At = &by, a.At
		}
		return out
	case r.Returned():
		out := &decisionInfoBody{Outcome: string(domain.NoticeReturned), At: r.StatusChangedAt,
			Observations: optionalString(r.DecisionReason), Failed: failed}
		if a := rec.Last(domain.ActionReturn); a != nil {
			by := toActorBody(a.By)
			out.By, out.At = &by, a.At
		}
		return out
	case r.Status == domain.ChangeRequestNotApplied && rec.LastAttempt != nil:
		by := toActorBody(rec.LastAttempt.By)
		return &decisionInfoBody{Outcome: string(domain.OutcomeNotApplied), By: &by, At: rec.LastAttempt.At, Failed: failed}
	}
	return nil
}

func toEndorsementDetailResponse(d endorsements.Detail) endorsementDetailResponse {
	p := d.Policy
	out := endorsementDetailResponse{
		Request: toEndorsementRequestBody(d.Request), Changes: changesOf(d.Request),
		Policy: endorsementPolicyBody{PolicyID: p.ID, Line: string(domain.LineLife),
			Holder:  holderBody{FullName: p.Holder.FullName, DocumentID: p.Holder.DocumentID},
			InForce: d.InForce, CoverageStart: p.CoverageStart.UTC(), CoverageEnd: p.CoverageEnd.UTC(),
			InsuredAmount: d.Terms.InsuredAmount, Premium: p.Premium, Currency: p.Currency,
			OnerousBeneficiary: toOnerousBody(d.Terms.Onerous),
			Endorsements:       policyEndorsementsBody{Count: d.Endorsements.Count}},
		Validations: toValidationBodies(d.Validations), ValidationsSource: string(d.Source),
		Decision: decisionOf(d.Request, d.Record),
	}
	if last := d.Endorsements.Last; last != nil {
		out.Policy.Endorsements.Last = &endorsementRefBody{EndorsementID: last.ID, ChangeType: string(last.Type), AppliedAt: last.At.UTC()}
	}
	if pc := d.Premium; pc != nil {
		body := &premiumChangeBody{Previous: pc.Previous, Currency: pc.Currency, RatingVersion: optionalString(pc.Version)}
		if pc.Priced {
			n := pc.New
			body.New = &n
		}
		out.Premium = body
	}
	return out
}

func requestIDOf(w http.ResponseWriter, req *http.Request) (string, bool) {
	id, ok := changePathID(req, "request_id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_request", "request_id (max 128 chars, no control characters) is required")
	}
	return id, ok
}

func getEndorsementRequest(svc EndorsementReviewService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := requestIDOf(w, req)
		if !ok {
			return
		}
		d, err := svc.Detail(req.Context(), id)
		if err != nil {
			respondEndorsementError(w, req, err)
			return
		}
		writeJSON(w, http.StatusOK, toEndorsementDetailResponse(d))
	}
}

type endorsementRequestResponse struct {
	Request endorsementRequestBody `json:"request"`
}

func reviewEndorsementRequest(svc EndorsementReviewService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := requestIDOf(w, req)
		if !ok {
			return
		}
		r, err := svc.Review(req.Context(), id)
		if err != nil {
			respondEndorsementError(w, req, err)
			return
		}
		writeJSON(w, http.StatusOK, endorsementRequestResponse{Request: toEndorsementRequestBody(r)})
	}
}

type appliedEndorsementBody struct {
	EndorsementID string    `json:"endorsement_id"`
	Version       int       `json:"version"`
	AppliedAt     time.Time `json:"applied_at"`
	By            actorBody `json:"by"`
}

type endorsementAttemptBody struct {
	At     time.Time `json:"at"`
	By     actorBody `json:"by"`
	Failed []string  `json:"failed"`
}

type applicationResponse struct {
	Outcome     string                  `json:"outcome"`
	Request     endorsementRequestBody  `json:"request"`
	Endorsement *appliedEndorsementBody `json:"endorsement"`
	Attempt     *endorsementAttemptBody `json:"attempt"`
	Validations []validationBody        `json:"validations"`
}

func applyEndorsementRequest(svc EndorsementReviewService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := requestIDOf(w, req)
		if !ok {
			return
		}
		res, err := svc.Apply(req.Context(), id)
		if err != nil {
			respondEndorsementError(w, req, err)
			return
		}
		out := applicationResponse{Outcome: string(res.Outcome), Request: toEndorsementRequestBody(res.Request),
			Validations: toValidationBodies(res.Validations)}
		if e := res.Endorsement; e != nil {
			out.Endorsement = &appliedEndorsementBody{EndorsementID: e.ID, Version: e.Version, AppliedAt: e.At.UTC(), By: toActorBody(e.By)}
		}
		if a := res.Attempt; a != nil {
			out.Attempt = &endorsementAttemptBody{At: a.At.UTC(), By: toActorBody(a.By), Failed: codesOf(a.Validations.Of(domain.ValidationFailed))}
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// observationsInput is the body of a rejection or a return.
type observationsInput struct {
	Observations string `json:"observations"`
}

func closeEndorsementRequest(decide func(context.Context, string, string) (domain.ChangeRequest, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := requestIDOf(w, req)
		if !ok {
			return
		}
		var in observationsInput
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the body must be {\"observations\": \"…\"}")
			return
		}
		r, err := decide(req.Context(), id, in.Observations)
		if err != nil {
			respondEndorsementError(w, req, err)
			return
		}
		writeJSON(w, http.StatusOK, endorsementRequestResponse{Request: toEndorsementRequestBody(r)})
	}
}

type historyEntryBody struct {
	Outcome       string            `json:"outcome"`
	At            time.Time         `json:"at"`
	EndorsementID *string           `json:"endorsement_id"`
	RequestID     string            `json:"request_id"`
	ChangeType    string            `json:"change_type"`
	Changes       []fieldChangeBody `json:"changes"`
	Premium       *struct {
		Previous float64 `json:"previous"`
		New      float64 `json:"new"`
		Currency string  `json:"currency"`
	} `json:"premium"`
	Failed []string  `json:"failed"`
	By     actorBody `json:"by"`
}

type historyResponse struct {
	PolicyID string             `json:"policy_id"`
	Holder   holderBody         `json:"holder"`
	Entries  []historyEntryBody `json:"entries"`
}

func toHistoryResponse(p domain.Policy, entries []domain.EndorsementHistoryEntry) historyResponse {
	out := historyResponse{PolicyID: p.ID, Holder: holderBody{FullName: p.Holder.FullName, DocumentID: p.Holder.DocumentID},
		Entries: make([]historyEntryBody, 0, len(entries))}
	for _, e := range entries {
		body := historyEntryBody{Outcome: string(e.Outcome), At: e.At.UTC(), EndorsementID: optionalString(e.EndorsementID),
			RequestID: e.Request.ID, ChangeType: string(e.Request.Type), Changes: changesOf(e.Request),
			Failed: codesOf(e.Failed), By: toActorBody(e.By)}
		if e.Outcome == domain.OutcomeApplied && e.Request.Type == domain.ChangeInsuredAmount {
			body.Premium = &struct {
				Previous float64 `json:"previous"`
				New      float64 `json:"new"`
				Currency string  `json:"currency"`
			}{Previous: e.PreviousPremium, New: e.NewPremium, Currency: e.Currency}
		}
		out.Entries = append(out.Entries, body)
	}
	return out
}

func getEndorsementHistory(svc EndorsementReviewService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := changePathID(req, "policy_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "policy_id (max 128 chars, no control characters) is required")
			return
		}
		p, entries, err := svc.History(req.Context(), id)
		if err != nil {
			respondEndorsementError(w, req, err)
			return
		}
		writeJSON(w, http.StatusOK, toHistoryResponse(p, entries))
	}
}

// decidedConflict is the 409 of a request another analyst decided first: it
// carries the status as the client must show it now.
type decidedConflict struct {
	Error          string  `json:"error"`
	Message        string  `json:"message"`
	Status         string  `json:"status"`
	Returned       bool    `json:"returned"`
	DecisionReason *string `json:"decision_reason"`
}

// unverifiableResponse is the 503 of validations that could not be verified:
// nothing changed.
type unverifiableResponse struct {
	Error        string   `json:"error"`
	Message      string   `json:"message"`
	Unverifiable []string `json:"unverifiable"`
}

func respondEndorsementError(w http.ResponseWriter, req *http.Request, err error) {
	var (
		decided      *endorsements.DecidedError
		unverifiable *endorsements.UnverifiableError
	)
	switch {
	case errors.As(err, &decided):
		code, message := "request_already_decided", "the change request was already decided"
		if errors.Is(err, ports.ErrNotReturnable) {
			code, message = "not_returnable", "only a request not applied and not returned yet can be returned"
		}
		writeJSON(w, http.StatusConflict, decidedConflict{Error: code, Message: message, Status: string(decided.Request.Status),
			Returned: decided.Request.Returned(), DecisionReason: optionalString(decided.Request.DecisionReason)})
	case errors.Is(err, ports.ErrNotReturnable):
		writeError(w, http.StatusConflict, "not_returnable", "only a request not applied and not returned yet can be returned")
	case errors.Is(err, ports.ErrPolicyChanged):
		writeError(w, http.StatusConflict, "policy_changed",
			"another endorsement of the policy was applied while this one was validated; nothing changed, apply it again")
	case errors.As(err, &unverifiable):
		writeJSON(w, http.StatusServiceUnavailable, unverifiableResponse{Error: "validation_unverifiable",
			Message: "the validations could not be verified; nothing changed, retry later", Unverifiable: codesOf(unverifiable.Codes)})
	case errors.Is(err, domain.ErrEndorsementObservations):
		writeError(w, http.StatusUnprocessableEntity, "observations_required", "observations of 10 to 500 characters are required")
	case errors.Is(err, domain.ErrInvalidInboxQuery):
		writeError(w, http.StatusBadRequest, "out_of_range", err.Error())
	case errors.Is(err, ports.ErrChangeRequestNotFound):
		writeError(w, http.StatusNotFound, "change_request_not_found", "the change request does not exist")
	case errors.Is(err, ports.ErrPolicyNotFound):
		writeError(w, http.StatusNotFound, "policy_not_found", "the policy does not exist")
	case isAuthError(err):
		writeAuthError(w, err)
	default:
		slog.ErrorContext(req.Context(), "could not process the endorsement request", "error", err)
		writeError(w, http.StatusServiceUnavailable, "endorsement_unavailable", "the endorsement could not be processed; retry later")
	}
}
