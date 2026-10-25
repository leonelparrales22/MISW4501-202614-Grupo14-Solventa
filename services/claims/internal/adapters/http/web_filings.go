package httpadapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// WebFilingService is the use case behind the web filing (HU-W2.1-01).
type WebFilingService interface {
	Options(ctx context.Context, subject string) (filing.FilingOptions, error)
	File(ctx context.Context, subject, key string, r domain.WebFilingRequest) (domain.ClaimRecord, bool, error)
}

// SupportUploader is the use case behind the upload of a support.
type SupportUploader interface {
	Upload(ctx context.Context, subject, fileName string, body io.Reader) (domain.SupportFile, error)
}

// ClaimReader is the use case behind the reads of the insured's claims.
type ClaimReader interface {
	List(ctx context.Context, subject string, q filing.ClaimQuery) (filing.ClaimPage, error)
	Get(ctx context.Context, id string) (domain.ClaimRecord, error)
	Receipt(ctx context.Context, id string) (domain.ClaimRecord, []byte, error)
}

const (
	// maxUploadBytes bounds the multipart body: one file of 10 MiB plus its
	// envelope.
	maxUploadBytes = domain.MaxSupportBytes + 64<<10
	// uploadReadTimeout replaces the ReadTimeout of the server on the upload
	// route only: 10 MiB on a slow connection takes longer than 10 s.
	//
	// Provisional (HU-W2.1-01 §9): with signed upload URLs the file no longer
	// goes through the service.
	uploadReadTimeout = 60 * time.Second
)

var (
	claimIDPattern  = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	policyIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{3,40}$`)
)

// WithClaimReads mounts the reads of the insured's own claims, from both
// channels (HU-W2.1-01): the list, the detail and the filing receipt. They
// only need the user pool.
func WithClaimReads(svc ClaimReader, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleInsured))
			r.Get("/v1/claims", listClaims(svc))
			r.Get("/v1/claims/{claim_id}", getClaim(svc))
			r.Get("/v1/claims/{claim_id}/filing-receipt", getReceipt(svc))
		})
	}
}

// WithWebFilings mounts the web filing with supports (HU-W2.1-01): the filing
// options, the upload of a support and the filing. Only an insured files,
// always on their own behalf.
func WithWebFilings(svc WebFilingService, supports SupportUploader, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleInsured))
			r.Get("/v1/claims/filing-options", filingOptions(svc))
			r.Post("/v1/claims/supports", uploadSupport(supports))
			r.Post("/v1/claims/web-filings", fileWebClaim(svc))
		})
	}
}

// --- Filing options ---------------------------------------------------------

type requirementBody struct {
	Code     string   `json:"code"`
	Label    string   `json:"label"`
	Required bool     `json:"required"`
	MinFiles int      `json:"min_files"`
	MaxFiles int      `json:"max_files"`
	Formats  []string `json:"formats"`
}

type incidentOptionBody struct {
	Code         string            `json:"code"`
	Label        string            `json:"label"`
	Requirements []requirementBody `json:"requirements"`
}

type eligiblePolicyBody struct {
	PolicyID      string               `json:"policy_id"`
	Line          string               `json:"line"`
	Product       string               `json:"product"`
	InsuredObject string               `json:"insured_object"`
	ValidFrom     time.Time            `json:"valid_from"`
	ValidTo       time.Time            `json:"valid_to"`
	IncidentTypes []incidentOptionBody `json:"incident_types"`
}

type filingOptionsResponse struct {
	CatalogVersion string               `json:"catalog_version"`
	Policies       []eligiblePolicyBody `json:"policies"`
}

func filingOptions(svc WebFilingService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		claims, _ := security.ClaimsFromContext(req.Context())
		options, err := svc.Options(req.Context(), claims.Subject)
		if err != nil {
			writePolicyCoreError(w, req, err)
			return
		}
		out := filingOptionsResponse{CatalogVersion: options.CatalogVersion, Policies: []eligiblePolicyBody{}}
		for _, p := range options.Policies {
			body := eligiblePolicyBody{
				PolicyID: p.Policy.ID, Line: string(p.Policy.Line), Product: p.Policy.Product,
				InsuredObject: p.Policy.InsuredObject, ValidFrom: p.Policy.ValidFrom.UTC(), ValidTo: p.Policy.ValidTo.UTC(),
				IncidentTypes: []incidentOptionBody{},
			}
			for _, o := range p.Incidents {
				option := incidentOptionBody{Code: string(o.Type), Label: o.Label, Requirements: []requirementBody{}}
				for _, r := range o.Requirements {
					formats := make([]string, 0, len(r.Formats))
					for _, f := range r.Formats {
						formats = append(formats, string(f))
					}
					option.Requirements = append(option.Requirements, requirementBody{
						Code: r.Code, Label: r.Label, Required: r.Required, MinFiles: r.MinFiles, MaxFiles: r.MaxFiles,
						Formats: formats,
					})
				}
				body.IncidentTypes = append(body.IncidentTypes, option)
			}
			out.Policies = append(out.Policies, body)
		}
		writeJSON(w, http.StatusOK, out)
	}
}

// --- Upload of a support ----------------------------------------------------

type supportFileResponse struct {
	SupportID   string    `json:"support_id"`
	FileName    string    `json:"file_name"`
	ContentType string    `json:"content_type"`
	ByteSize    int64     `json:"byte_size"`
	SHA256      string    `json:"sha256"`
	UploadedAt  time.Time `json:"uploaded_at"`
}

// errExtraFilePart means the body brought more than one "file" part.
var errExtraFilePart = errors.New("more than one file part")

func uploadSupport(svc SupportUploader) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		// The server's ReadTimeout is too short for 10 MiB on a slow
		// connection. A writer that cannot extend it (tests) keeps the default.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(uploadReadTimeout))
		req.Body = http.MaxBytesReader(w, req.Body, maxUploadBytes)

		name, content, err := readFilePart(req)
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge), errors.Is(err, domain.ErrFileTooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "the file is larger than 10 MiB")
			return
		case err != nil:
			writeError(w, http.StatusBadRequest, "invalid_request",
				"the body must be multipart/form-data with exactly one part named \"file\"")
			return
		}

		claims, _ := security.ClaimsFromContext(req.Context())
		f, err := svc.Upload(req.Context(), claims.Subject, name, content)
		switch {
		case err == nil:
		case errors.Is(err, domain.ErrFileTooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, "file_too_large", "the file is larger than 10 MiB")
			return
		case errors.Is(err, domain.ErrUnsupportedFileType):
			writeError(w, http.StatusUnsupportedMediaType, "unsupported_file_type", "only JPG, PNG or PDF files are accepted")
			return
		case errors.Is(err, domain.ErrEmptyFile):
			writeError(w, http.StatusUnprocessableEntity, "empty_file", "the file has no content")
			return
		case errors.Is(err, domain.ErrTooManyPendingSupports):
			writeError(w, http.StatusTooManyRequests, "too_many_pending_supports",
				"there are too many supports not attached to a claim")
			return
		default:
			slog.ErrorContext(req.Context(), "could not store the support", "error", err)
			writeError(w, http.StatusServiceUnavailable, "supports_unavailable", "the support could not be stored; retry")
			return
		}
		writeJSON(w, http.StatusCreated, supportFileResponse{
			SupportID: f.ID, FileName: f.FileName, ContentType: f.ContentType(), ByteSize: f.ByteSize,
			SHA256: f.SHA256, UploadedAt: f.UploadedAt,
		})
	}
}

// readFilePart reads the only part named "file" of a multipart body, at most
// one byte over the limit of a support. Other parts are skipped; a second
// "file" part is an error.
func readFilePart(req *http.Request) (string, io.Reader, error) {
	mr, err := req.MultipartReader()
	if err != nil {
		return "", nil, err
	}
	var (
		name    string
		content []byte
		found   bool
	)
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", nil, err
		}
		if part.FormName() != "file" {
			if _, err := io.Copy(io.Discard, part); err != nil {
				return "", nil, err
			}
			continue
		}
		if found {
			return "", nil, errExtraFilePart
		}
		found = true
		name = part.FileName()
		if content, err = readPart(part); err != nil {
			return "", nil, err
		}
	}
	if !found {
		return "", nil, errors.New("no file part")
	}
	return name, bytes.NewReader(content), nil
}

func readPart(part *multipart.Part) ([]byte, error) {
	content, err := io.ReadAll(io.LimitReader(part, domain.MaxSupportBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) > domain.MaxSupportBytes {
		return nil, domain.ErrFileTooLarge
	}
	return content, nil
}

// --- Web filing -------------------------------------------------------------

type supportGroupBody struct {
	RequirementCode string   `json:"requirement_code"`
	SupportIDs      []string `json:"support_ids"`
}

type webFilingRequest struct {
	PolicyID     string             `json:"policy_id"`
	IncidentType string             `json:"incident_type"`
	OccurredAt   string             `json:"occurred_at"`
	Place        string             `json:"place"`
	Description  string             `json:"description"`
	Supports     []supportGroupBody `json:"supports"`
}

type policyNotEligibleResponse struct {
	Error     string     `json:"error"`
	Message   string     `json:"message"`
	Reason    string     `json:"reason"`
	ValidFrom *time.Time `json:"valid_from"`
	ValidTo   *time.Time `json:"valid_to"`
}

var notEligibleMessages = map[string]string{
	filing.ReasonPolicyNotFound:          "the policy is not one of the insured's",
	filing.ReasonPolicyNotActive:         "the policy is not active",
	filing.ReasonIncidentNotCovered:      "the policy does not cover the claim type",
	filing.ReasonOccurredOutsideValidity: "the incident happened outside the validity of the policy",
}

func fileWebClaim(svc WebFilingService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		key := req.Header.Get(headerFilingKey)
		if !filingKeyPattern.MatchString(key) {
			writeError(w, http.StatusBadRequest, "invalid_request",
				"the Idempotency-Key header is required (up to 128 characters)")
			return
		}
		var in webFilingRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}
		r := domain.WebFilingRequest{
			PolicyID: in.PolicyID, IncidentType: in.IncidentType, OccurredAt: in.OccurredAt,
			Place: in.Place, Description: in.Description,
		}
		for _, g := range in.Supports {
			r.Supports = append(r.Supports, domain.SupportGroup{RequirementCode: g.RequirementCode, SupportIDs: g.SupportIDs})
		}

		claims, _ := security.ClaimsFromContext(req.Context())
		c, created, err := svc.File(req.Context(), claims.Subject, key, r)

		var (
			invalid     *domain.ValidationError
			notEligible *filing.PolicyNotEligibleError
		)
		switch {
		case err == nil:
		case errors.As(err, &invalid):
			writeValidationError(w, invalid)
			return
		case errors.As(err, &notEligible):
			out := policyNotEligibleResponse{Error: "policy_not_eligible", Reason: notEligible.Reason,
				Message: notEligibleMessages[notEligible.Reason]}
			if p := notEligible.Policy; p != nil {
				from, to := p.ValidFrom.UTC(), p.ValidTo.UTC()
				out.ValidFrom, out.ValidTo = &from, &to
			}
			writeJSON(w, http.StatusUnprocessableEntity, out)
			return
		case errors.Is(err, ports.ErrFilingKeyReused):
			writeError(w, http.StatusUnprocessableEntity, "idempotency_key_reused", "that key was used for another filing")
			return
		case errors.Is(err, ports.ErrPolicyDirectoryUnavailable):
			writePolicyCoreError(w, req, err)
			return
		default:
			slog.ErrorContext(req.Context(), "could not file the web claim", "error", err)
			writeError(w, http.StatusServiceUnavailable, "filing_unavailable",
				"the filing could not be completed; retry with the same key")
			return
		}

		status := http.StatusOK
		if created {
			status = http.StatusCreated
			slog.InfoContext(req.Context(), "web claim filed",
				"claim_id", c.ID, "filing_number", c.FilingNumber, "supports", len(c.Supports))
		}
		writeJSON(w, status, detailBody(c))
	}
}

func writePolicyCoreError(w http.ResponseWriter, req *http.Request, err error) {
	slog.WarnContext(req.Context(), "the policy core did not answer", "error", err)
	writeError(w, http.StatusServiceUnavailable, "policy_directory_unavailable",
		"the policies could not be read; retry")
}

// --- Reads ------------------------------------------------------------------

type policySummaryBody struct {
	PolicyID      string  `json:"policy_id"`
	Line          *string `json:"line"`
	Product       *string `json:"product"`
	InsuredObject *string `json:"insured_object"`
}

type attachedFileBody struct {
	SupportID   string `json:"support_id"`
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	ByteSize    int64  `json:"byte_size"`
}

type supportItemBody struct {
	RequirementCode string             `json:"requirement_code"`
	Label           string             `json:"label"`
	Files           []attachedFileBody `json:"files"`
}

type supportsSummaryBody struct {
	RequiredTotal    int               `json:"required_total"`
	RequiredAttached int               `json:"required_attached"`
	Files            int               `json:"files"`
	Items            []supportItemBody `json:"items"`
}

type claimSummaryBody struct {
	ClaimID      string            `json:"claim_id"`
	FilingNumber string            `json:"filing_number"`
	Status       string            `json:"status"`
	Channel      string            `json:"channel"`
	Policy       policySummaryBody `json:"policy"`
	IncidentType string            `json:"incident_type"`
	OccurredAt   time.Time         `json:"occurred_at"`
	FiledAt      time.Time         `json:"filed_at"`
}

type claimDetailBody struct {
	claimSummaryBody
	Place       string              `json:"place"`
	Description string              `json:"description"`
	Supports    supportsSummaryBody `json:"supports"`
}

type claimListResponse struct {
	Items         []claimSummaryBody `json:"items"`
	NextPageToken *string            `json:"next_page_token"`
}

func summaryBody(c domain.ClaimRecord) claimSummaryBody {
	out := claimSummaryBody{
		ClaimID: c.ID, FilingNumber: c.FilingNumber, Status: string(c.Status), Channel: string(c.Channel),
		Policy:       policySummaryBody{PolicyID: c.PolicyID},
		IncidentType: string(c.IncidentType), OccurredAt: c.OccurredAt.UTC(), FiledAt: c.FiledAt.UTC(),
	}
	if p := c.Policy; p != nil {
		line, product, object := string(p.Line), p.Product, p.InsuredObject
		out.Policy.Line, out.Policy.Product, out.Policy.InsuredObject = &line, &product, &object
	}
	return out
}

func detailBody(c domain.ClaimRecord) claimDetailBody {
	s := c.SupportSummary()
	out := claimDetailBody{
		claimSummaryBody: summaryBody(c), Place: c.Place, Description: c.Description,
		Supports: supportsSummaryBody{RequiredTotal: s.RequiredTotal, RequiredAttached: s.RequiredAttached,
			Files: s.Files, Items: []supportItemBody{}},
	}
	for _, item := range s.Items {
		body := supportItemBody{RequirementCode: item.RequirementCode, Label: item.Label, Files: []attachedFileBody{}}
		for _, f := range item.Files {
			body.Files = append(body.Files, attachedFileBody{
				SupportID: f.ID, FileName: f.FileName, ContentType: f.ContentType(), ByteSize: f.ByteSize})
		}
		out.Supports.Items = append(out.Supports.Items, body)
	}
	return out
}

func listClaims(svc ClaimReader) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		query := req.URL.Query()
		q := filing.ClaimQuery{
			Status: query.Get("status"), PolicyID: query.Get("policy_id"), From: query.Get("from"),
			PageToken: query.Get("page_token"),
		}
		if raw := query.Get("page_size"); raw != "" {
			size, err := strconv.Atoi(raw)
			if err != nil || size < 1 {
				writeError(w, http.StatusBadRequest, "invalid_request", "page_size must be between 1 and 50")
				return
			}
			q.PageSize = size
		}
		if len(q.PageToken) > 512 || (q.PolicyID != "" && !policyIDPattern.MatchString(q.PolicyID)) {
			writeError(w, http.StatusBadRequest, "invalid_request", "a filter or the page token is not valid")
			return
		}

		claims, _ := security.ClaimsFromContext(req.Context())
		page, err := svc.List(req.Context(), claims.Subject, q)
		switch {
		case errors.Is(err, filing.ErrInvalidQuery):
			writeError(w, http.StatusBadRequest, "invalid_request", "a filter or the page token is not valid")
			return
		case err != nil:
			slog.ErrorContext(req.Context(), "could not list the claims", "error", err)
			writeError(w, http.StatusServiceUnavailable, "claims_unavailable", "the claims could not be read; retry")
			return
		}
		out := claimListResponse{Items: make([]claimSummaryBody, 0, len(page.Claims))}
		for _, c := range page.Claims {
			out.Items = append(out.Items, summaryBody(c))
		}
		if page.NextPageToken != "" {
			out.NextPageToken = &page.NextPageToken
		}
		writeJSON(w, http.StatusOK, out)
	}
}

func getClaim(svc ClaimReader) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id := chi.URLParam(req, "claim_id")
		if !claimIDPattern.MatchString(id) {
			writeError(w, http.StatusNotFound, "claim_not_found", "there is no claim with that id")
			return
		}
		c, err := svc.Get(req.Context(), id)
		if !writeReadError(w, req, err) {
			writeJSON(w, http.StatusOK, detailBody(c))
		}
	}
}

func getReceipt(svc ClaimReader) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id := chi.URLParam(req, "claim_id")
		if !claimIDPattern.MatchString(id) {
			writeError(w, http.StatusNotFound, "claim_not_found", "there is no claim with that id")
			return
		}
		c, pdf, err := svc.Receipt(req.Context(), id)
		if writeReadError(w, req, err) {
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="radicado-`+c.FilingNumber+`.pdf"`)
		w.Header().Set("Content-Length", strconv.Itoa(len(pdf)))
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write(pdf); err != nil {
			slog.ErrorContext(req.Context(), "could not write the receipt", "error", err)
		}
	}
}

// writeReadError answers a failed read and tells whether it did.
func writeReadError(w http.ResponseWriter, req *http.Request, err error) bool {
	switch {
	case err == nil:
		return false
	case errors.Is(err, ports.ErrClaimNotFound):
		writeError(w, http.StatusNotFound, "claim_not_found", "there is no claim with that id")
	case errors.Is(err, security.ErrForbidden), errors.Is(err, security.ErrUnauthenticated):
		security.WriteForbidden(w)
	default:
		slog.ErrorContext(req.Context(), "could not read the claim", "error", err)
		writeError(w, http.StatusServiceUnavailable, "claims_unavailable", "the claim could not be read; retry")
	}
	return true
}
