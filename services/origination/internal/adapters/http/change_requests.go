package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/endorsements"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ChangeRequestService is the use case behind the change requests of the
// insured (HU-W1.4-01).
type ChangeRequestService interface {
	CurrentTerms(ctx context.Context, policyID string) (endorsements.Current, error)
	File(ctx context.Context, policyID string, in domain.ChangeInput) (domain.ChangeRequest, error)
	List(ctx context.Context, f domain.ChangeRequestFilter) (domain.ChangeRequestPage, error)
	Get(ctx context.Context, requestID string) (domain.ChangeRequest, error)
}

// WithChangeRequests mounts the change requests of the insured (HU-W1.4-01,
// screens A3, A3b and A11): the terms a policy can change, filing a request,
// the list of the insured's requests and one request. They are human routes
// for the insured who owns the policy. The fixed segment "change-requests"
// never covers the routes of a policy (/v1/policies/{policy_id}/…), which
// always have a segment after the id. Register it after WithObservability.
func WithChangeRequests(svc ChangeRequestService, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleInsured))
			r.Get("/v1/policies/{policy_id}/current-terms", getCurrentTerms(svc))
			r.Post("/v1/policies/{policy_id}/change-requests", fileChangeRequest(svc))
			r.Get("/v1/policies/change-requests", listChangeRequests(svc))
			r.Get("/v1/policies/change-requests/{request_id}", getChangeRequest(svc))
		})
	}
}

type beneficiaryBody struct {
	FullName       string  `json:"full_name"`
	Relationship   string  `json:"relationship"`
	DocumentType   string  `json:"document_type"`
	DocumentNumber string  `json:"document_number"`
	SharePercent   float64 `json:"share_percent"`
}

func toBeneficiaryBody(b domain.Beneficiary) beneficiaryBody {
	return beneficiaryBody{FullName: b.FullName, Relationship: string(b.Relationship), DocumentType: string(b.DocumentType),
		DocumentNumber: b.DocumentNumber, SharePercent: b.SharePercent()}
}

func toBeneficiaryBodies(bs []domain.Beneficiary) []beneficiaryBody {
	out := make([]beneficiaryBody, 0, len(bs))
	for _, b := range bs {
		out = append(out, toBeneficiaryBody(b))
	}
	return out
}

// onerousBody is the lender of the credit. partner_id and name are null when
// they are not known: the client shows a generic label.
type onerousBody struct {
	PartnerID *string `json:"partner_id"`
	Name      *string `json:"name"`
	Basis     string  `json:"basis"`
}

func toOnerousBody(o *domain.OnerousBeneficiary) *onerousBody {
	if o == nil {
		return nil
	}
	return &onerousBody{PartnerID: optionalString(o.PartnerID), Name: optionalString(o.Name), Basis: o.Basis}
}

// personalDataBody is the contact data of the policy; a null member is not
// registered (in the terms) or does not change (in a requested change).
type personalDataBody struct {
	Address *string `json:"address"`
	Email   *string `json:"email"`
	Phone   *string `json:"phone"`
}

func toPersonalDataBody(c domain.ContactData) *personalDataBody {
	return &personalDataBody{Address: c.Address, Email: c.Email, Phone: c.Phone}
}

type policyTermsBody struct {
	InsuredAmount      float64           `json:"insured_amount"`
	Currency           string            `json:"currency"`
	Beneficiaries      []beneficiaryBody `json:"beneficiaries"`
	OnerousBeneficiary *onerousBody      `json:"onerous_beneficiary"`
	PersonalData       *personalDataBody `json:"personal_data"`
}

type termsPolicyBody struct {
	PolicyID      string    `json:"policy_id"`
	Line          string    `json:"line"`
	HolderName    string    `json:"holder_name"`
	CoverageStart time.Time `json:"coverage_start"`
	CoverageEnd   time.Time `json:"coverage_end"`
	InForce       bool      `json:"in_force"`
}

type openRequestBody struct {
	RequestID  string    `json:"request_id"`
	ChangeType string    `json:"change_type"`
	Status     string    `json:"status"`
	FiledAt    time.Time `json:"filed_at"`
}

type limitsBody struct {
	InsuredAmountMin float64  `json:"insured_amount_min"`
	InsuredAmountMax float64  `json:"insured_amount_max"`
	MaxBeneficiaries int      `json:"max_beneficiaries"`
	Relationships    []string `json:"relationships"`
	DocumentTypes    []string `json:"document_types"`
}

type currentTermsResponse struct {
	Policy       termsPolicyBody   `json:"policy"`
	Terms        policyTermsBody   `json:"terms"`
	OpenRequests []openRequestBody `json:"open_requests"`
	Limits       limitsBody        `json:"limits"`
}

func changeLimits() limitsBody {
	out := limitsBody{InsuredAmountMin: domain.MinInsuredAmount, InsuredAmountMax: domain.MaxInsuredAmount,
		MaxBeneficiaries: domain.MaxBeneficiaries}
	for _, r := range domain.Relationships {
		out.Relationships = append(out.Relationships, string(r))
	}
	for _, d := range domain.DocumentTypes {
		out.DocumentTypes = append(out.DocumentTypes, string(d))
	}
	return out
}

func toCurrentTermsResponse(c endorsements.Current) currentTermsResponse {
	p := c.Policy
	out := currentTermsResponse{
		Policy: termsPolicyBody{PolicyID: p.ID, Line: string(domain.LineLife), HolderName: p.Holder.FullName,
			CoverageStart: p.CoverageStart.UTC(), CoverageEnd: p.CoverageEnd.UTC(), InForce: c.InForce},
		Terms: policyTermsBody{InsuredAmount: c.Terms.InsuredAmount, Currency: c.Terms.Currency,
			Beneficiaries: toBeneficiaryBodies(c.Terms.Beneficiaries), OnerousBeneficiary: toOnerousBody(c.Terms.Onerous),
			PersonalData: toPersonalDataBody(c.Terms.Contact)},
		OpenRequests: make([]openRequestBody, 0, len(c.Open)),
		Limits:       changeLimits(),
	}
	for _, r := range c.Open {
		out.OpenRequests = append(out.OpenRequests, openRequestBody{RequestID: r.ID, ChangeType: string(r.Type),
			Status: string(r.Status), FiledAt: r.FiledAt.UTC()})
	}
	return out
}

func getCurrentTerms(svc ChangeRequestService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := changePathID(req, "policy_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "policy_id (max 128 chars, no control characters) is required")
			return
		}
		c, err := svc.CurrentTerms(req.Context(), id)
		if err != nil {
			respondChangeRequestError(w, req, err)
			return
		}
		writeJSON(w, http.StatusOK, toCurrentTermsResponse(c))
	}
}

// changePathID reads an id of the path: present, at most 128 characters and
// without control characters (a NUL would reach the database as an error).
func changePathID(req *http.Request, name string) (string, bool) {
	id, ok := pathID(req, name)
	return id, ok && !domain.HasControl(id)
}

// changeRequestInput is the body of POST /v1/policies/{policy_id}/change-requests:
// the type, the channel and only the member of the type.
type changeRequestInput struct {
	ChangeType    string                  `json:"change_type"`
	Channel       string                  `json:"channel"`
	Beneficiaries *[]beneficiaryInputBody `json:"beneficiaries"`
	InsuredAmount *float64                `json:"insured_amount"`
	PersonalData  *personalDataBody       `json:"personal_data"`
}

type beneficiaryInputBody struct {
	FullName       string   `json:"full_name"`
	Relationship   string   `json:"relationship"`
	DocumentType   string   `json:"document_type"`
	DocumentNumber string   `json:"document_number"`
	SharePercent   *float64 `json:"share_percent"`
}

// onlyItsMember tells whether the body carries no member of another type
// (the oneOf of the contract).
func (in changeRequestInput) onlyItsMember() bool {
	members := map[domain.ChangeType]bool{
		domain.ChangeBeneficiaries: in.Beneficiaries != nil,
		domain.ChangeInsuredAmount: in.InsuredAmount != nil,
		domain.ChangePersonalData:  in.PersonalData != nil,
	}
	t, known := domain.ParseChangeType(in.ChangeType)
	for typ, present := range members {
		if present && known && typ != t {
			return false
		}
	}
	return true
}

func (in changeRequestInput) toDomain() domain.ChangeInput {
	out := domain.ChangeInput{Type: in.ChangeType, Channel: in.Channel, InsuredAmount: in.InsuredAmount}
	if in.Beneficiaries != nil {
		out.Beneficiaries = make([]domain.BeneficiaryInput, 0, len(*in.Beneficiaries))
		for _, b := range *in.Beneficiaries {
			out.Beneficiaries = append(out.Beneficiaries, domain.BeneficiaryInput{FullName: b.FullName, Relationship: b.Relationship,
				DocumentType: b.DocumentType, DocumentNumber: b.DocumentNumber, SharePercent: b.SharePercent})
		}
	}
	if in.PersonalData != nil {
		out.PersonalData = &domain.ContactInput{Address: in.PersonalData.Address, Email: in.PersonalData.Email,
			Phone: in.PersonalData.Phone}
	}
	return out
}

func fileChangeRequest(svc ChangeRequestService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := changePathID(req, "policy_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "policy_id (max 128 chars, no control characters) is required")
			return
		}
		var in changeRequestInput
		dec := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes))
		// The onerous beneficiary, or anything else the contract does not
		// name, never travels in a request (additionalProperties: false).
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not a valid change request")
			return
		}
		if !in.onlyItsMember() {
			writeError(w, http.StatusBadRequest, "invalid_request", "the body carries only the member of its change_type")
			return
		}
		r, err := svc.File(req.Context(), id, in.toDomain())
		if err != nil {
			respondChangeRequestError(w, req, err)
			return
		}
		w.Header().Set("Location", "/v1/policies/change-requests/"+url.PathEscape(r.ID))
		writeJSON(w, http.StatusCreated, toChangeRequestBody(r))
	}
}

// changeContentBody is one side of a change: only the members of its type.
type changeContentBody struct {
	Beneficiaries      *[]beneficiaryBody `json:"beneficiaries,omitempty"`
	OnerousBeneficiary *onerousBody       `json:"onerous_beneficiary,omitempty"`
	InsuredAmount      *float64           `json:"insured_amount,omitempty"`
	Currency           *string            `json:"currency,omitempty"`
	PersonalData       *personalDataBody  `json:"personal_data,omitempty"`
}

func toChangeContentBody(t domain.ChangeType, c domain.ChangeContent) changeContentBody {
	switch t {
	case domain.ChangeBeneficiaries:
		list := toBeneficiaryBodies(c.Beneficiaries)
		return changeContentBody{Beneficiaries: &list, OnerousBeneficiary: toOnerousBody(c.Onerous)}
	case domain.ChangeInsuredAmount:
		amount, currency := c.InsuredAmount, c.Currency
		return changeContentBody{InsuredAmount: &amount, Currency: &currency}
	default:
		return changeContentBody{PersonalData: toPersonalDataBody(c.Contact)}
	}
}

// fieldChangeBody is one line of the difference: previous and requested are a
// beneficiary, a number or a text, or null.
type fieldChangeBody struct {
	Field     string `json:"field"`
	Previous  any    `json:"previous"`
	Requested any    `json:"requested"`
}

func changeValue(v any) any {
	if b, ok := v.(domain.Beneficiary); ok {
		return toBeneficiaryBody(b)
	}
	return v
}

type requesterBody struct {
	Email string  `json:"email"`
	Name  *string `json:"name"`
}

// changeRequestBody is a change request. status is the stored one (five
// values); the insured sees not_applied as in_review without a
// decision_reason and as rejected with one (adjustment A-1).
type changeRequestBody struct {
	RequestID       string            `json:"request_id"`
	PolicyID        string            `json:"policy_id"`
	ChangeType      string            `json:"change_type"`
	Channel         string            `json:"channel"`
	Status          string            `json:"status"`
	StatusChangedAt time.Time         `json:"status_changed_at"`
	DecisionReason  *string           `json:"decision_reason"`
	FiledAt         time.Time         `json:"filed_at"`
	RequestedBy     requesterBody     `json:"requested_by"`
	Previous        changeContentBody `json:"previous"`
	Requested       changeContentBody `json:"requested"`
	Changes         []fieldChangeBody `json:"changes"`
}

func toChangeRequestBody(r domain.ChangeRequest) changeRequestBody {
	out := changeRequestBody{
		RequestID: r.ID, PolicyID: r.PolicyID, ChangeType: string(r.Type), Channel: string(r.Channel),
		Status: string(r.Status), StatusChangedAt: r.StatusChangedAt.UTC(), DecisionReason: optionalString(r.DecisionReason),
		FiledAt:     r.FiledAt.UTC(),
		RequestedBy: requesterBody{Email: r.RequestedBy.Email, Name: optionalString(r.RequestedBy.Name)},
		Previous:    toChangeContentBody(r.Type, r.Previous), Requested: toChangeContentBody(r.Type, r.Requested),
		Changes: []fieldChangeBody{},
	}
	for _, c := range r.Changes() {
		out.Changes = append(out.Changes, fieldChangeBody{Field: c.Field, Previous: changeValue(c.Previous), Requested: changeValue(c.Requested)})
	}
	return out
}

func getChangeRequest(svc ChangeRequestService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := changePathID(req, "request_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "request_id (max 128 chars, no control characters) is required")
			return
		}
		r, err := svc.Get(req.Context(), id)
		if err != nil {
			respondChangeRequestError(w, req, err)
			return
		}
		writeJSON(w, http.StatusOK, toChangeRequestBody(r))
	}
}

// changeRequestSummaryBody is one row of the list.
type changeRequestSummaryBody struct {
	RequestID       string    `json:"request_id"`
	PolicyID        string    `json:"policy_id"`
	ChangeType      string    `json:"change_type"`
	Channel         string    `json:"channel"`
	Status          string    `json:"status"`
	StatusChangedAt time.Time `json:"status_changed_at"`
	DecisionReason  *string   `json:"decision_reason"`
	FiledAt         time.Time `json:"filed_at"`
}

type changeRequestRefBody struct {
	RequestID       string    `json:"request_id"`
	ChangeType      string    `json:"change_type"`
	FiledAt         time.Time `json:"filed_at"`
	StatusChangedAt time.Time `json:"status_changed_at"`
}

type indicatorBody struct {
	Count  int                   `json:"count"`
	Latest *changeRequestRefBody `json:"latest"`
}

type changeRequestIndicatorsBody struct {
	Open            indicatorBody `json:"open"`
	AppliedThisYear indicatorBody `json:"applied_this_year"`
	Rejected        indicatorBody `json:"rejected"`
}

type changeRequestPageResponse struct {
	Items      []changeRequestSummaryBody  `json:"items"`
	Page       int                         `json:"page"`
	PageSize   int                         `json:"page_size"`
	TotalItems int                         `json:"total_items"`
	TotalPages int                         `json:"total_pages"`
	Indicators changeRequestIndicatorsBody `json:"indicators"`
	Policies   []string                    `json:"policies"`
}

func indicatorOf(count int, ref *domain.ChangeRequestRef) indicatorBody {
	out := indicatorBody{Count: count}
	if ref != nil {
		out.Latest = &changeRequestRefBody{RequestID: ref.ID, ChangeType: string(ref.Type), FiledAt: ref.FiledAt.UTC(),
			StatusChangedAt: ref.StatusChangedAt.UTC()}
	}
	return out
}

func toChangeRequestPageResponse(p domain.ChangeRequestPage) changeRequestPageResponse {
	out := changeRequestPageResponse{
		Items: make([]changeRequestSummaryBody, 0, len(p.Items)), Page: p.Page, PageSize: p.PageSize,
		TotalItems: p.Total, TotalPages: p.TotalPages(),
		Indicators: changeRequestIndicatorsBody{
			Open:            indicatorOf(p.Indicators.Open, p.Indicators.LatestOpen),
			AppliedThisYear: indicatorOf(p.Indicators.AppliedThisYear, p.Indicators.LastApplied),
			Rejected:        indicatorOf(p.Indicators.Rejected, p.Indicators.LastRejected),
		},
		Policies: append([]string{}, p.Policies...),
	}
	for _, r := range p.Items {
		out.Items = append(out.Items, changeRequestSummaryBody{RequestID: r.ID, PolicyID: r.PolicyID, ChangeType: string(r.Type),
			Channel: string(r.Channel), Status: string(r.Status), StatusChangedAt: r.StatusChangedAt.UTC(),
			DecisionReason: optionalString(r.DecisionReason), FiledAt: r.FiledAt.UTC()})
	}
	return out
}

// changeRequestFilterOf reads the query of the list and names every
// parameter it cannot read.
func changeRequestFilterOf(req *http.Request) (domain.ChangeRequestFilter, []domain.FieldError) {
	v := req.URL.Query()
	var (
		f      domain.ChangeRequestFilter
		fields []domain.FieldError
	)
	bad := func(field, code string) { fields = append(fields, domain.FieldError{Field: field, Code: code}) }
	status, err := domain.ParseInsuredStatus(v.Get("status"))
	if err != nil {
		bad("status", domain.CodeInvalidFormat)
	}
	f.Status = status
	f.PolicyID = strings.TrimSpace(v.Get("policy_id"))
	if len(f.PolicyID) > maxIDSize || domain.HasControl(f.PolicyID) {
		bad("policy_id", domain.CodeInvalidFormat)
	}
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
	}{{"page", 1<<31 - 1, &f.Page}, {"page_size", domain.MaxChangeRequestPageSize, &f.PageSize}} {
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

func listChangeRequests(svc ChangeRequestService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		f, fields := changeRequestFilterOf(req)
		if len(fields) > 0 {
			writeQueryValidationError(w, fields)
			return
		}
		page, err := svc.List(req.Context(), f)
		if err != nil {
			respondChangeRequestError(w, req, err)
			return
		}
		writeJSON(w, http.StatusOK, toChangeRequestPageResponse(page))
	}
}

// writeQueryValidationError answers 400 validation_failed with every query
// parameter that cannot be read.
func writeQueryValidationError(w http.ResponseWriter, fields []domain.FieldError) {
	out := validationErrorResponse{Error: "validation_failed", Message: "one or more query parameters are invalid",
		Fields: make([]fieldErrorBody, 0, len(fields))}
	for _, f := range fields {
		out.Fields = append(out.Fields, fieldErrorBody{Field: f.Field, Code: f.Code})
	}
	writeJSON(w, http.StatusBadRequest, out)
}

// changeRequestConflict is the 409 of an open request of the same type: it
// names the open one, so the client links to it.
type changeRequestConflict struct {
	Error     string  `json:"error"`
	Message   string  `json:"message"`
	RequestID *string `json:"request_id"`
}

func respondChangeRequestError(w http.ResponseWriter, req *http.Request, err error) {
	var (
		invalid *domain.ValidationError
		open    *ports.ChangeRequestOpenError
	)
	switch {
	case errors.As(err, &invalid):
		writeValidationError(w, invalid)
	case errors.As(err, &open):
		writeJSON(w, http.StatusConflict, changeRequestConflict{Error: "change_request_open",
			Message: "the policy already has an open request of this type", RequestID: optionalString(open.RequestID)})
	case errors.Is(err, domain.ErrPolicyNotInForce):
		writeError(w, http.StatusConflict, "policy_not_in_force", "the policy is not in force")
	case errors.Is(err, ports.ErrPolicyNotFound):
		writeError(w, http.StatusNotFound, "policy_not_found", "the policy does not exist")
	case errors.Is(err, ports.ErrChangeRequestNotFound):
		writeError(w, http.StatusNotFound, "change_request_not_found", "the change request does not exist")
	case errors.Is(err, domain.ErrInvalidChangeRequestQuery):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case isAuthError(err):
		writeAuthError(w, err)
	default:
		slog.ErrorContext(req.Context(), "could not process the change request", "error", err)
		writeError(w, http.StatusServiceUnavailable, "change_request_unavailable", "the change request could not be processed; retry later")
	}
}
