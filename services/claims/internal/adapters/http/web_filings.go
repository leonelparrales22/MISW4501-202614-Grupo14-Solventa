package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
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

// SupportUploader is the use case behind the supports: the declaration of a
// file, which returns where to upload it, and the confirmation of the upload.
type SupportUploader interface {
	Declare(ctx context.Context, subject string, r domain.SupportUploadRequest) (filing.SupportUpload, error)
	Confirm(ctx context.Context, subject, id string) (domain.SupportFile, error)
}

// ClaimReader is the use case behind the reads of the insured's claims.
type ClaimReader interface {
	List(ctx context.Context, subject string, q filing.ClaimQuery) (filing.ClaimPage, error)
	Get(ctx context.Context, id string) (domain.ClaimRecord, error)
	Receipt(ctx context.Context, id string) (domain.ClaimRecord, []byte, error)
	EvidenceLinks(ctx context.Context, c domain.ClaimRecord) []filing.EvidenceLink
}

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
// options, the declaration and confirmation of a support, and the filing. Only an insured files,
// always on their own behalf.
func WithWebFilings(svc WebFilingService, supports SupportUploader, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleInsured))
			r.Get("/v1/claims/filing-options", filingOptions(svc))
			r.Post("/v1/claims/supports", declareSupport(supports))
			r.Post("/v1/claims/supports/{support_id}/confirmation", confirmSupport(supports))
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

// --- Supports ---------------------------------------------------------------

type declareSupportRequest struct {
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	ByteSize    int64  `json:"byte_size"`
	SHA256      string `json:"sha256"`
}

type declareSupportResponse struct {
	SupportID   string `json:"support_id"`
	FileName    string `json:"file_name"`
	ContentType string `json:"content_type"`
	ByteSize    int64  `json:"byte_size"`
	SHA256      string `json:"sha256"`
	UploadURL   string `json:"upload_url"`
	// UploadHeaders are the headers the PUT has to carry, exactly these: the
	// signature covers them.
	UploadHeaders map[string]string `json:"upload_headers"`
	ExpiresAt     time.Time         `json:"expires_at"`
}

type confirmedSupportResponse struct {
	SupportID   string    `json:"support_id"`
	FileName    string    `json:"file_name"`
	ContentType string    `json:"content_type"`
	ByteSize    int64     `json:"byte_size"`
	SHA256      string    `json:"sha256"`
	ConfirmedAt time.Time `json:"confirmed_at"`
}

func declareSupport(svc SupportUploader) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		var in declareSupportRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, maxBodyBytes)).Decode(&in); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the request body is not valid JSON")
			return
		}

		claims, _ := security.ClaimsFromContext(req.Context())
		up, err := svc.Declare(req.Context(), claims.Subject, domain.SupportUploadRequest{
			FileName: in.FileName, ContentType: in.ContentType, ByteSize: in.ByteSize, SHA256: in.SHA256,
		})
		var invalid *domain.ValidationError
		switch {
		case err == nil:
		case errors.As(err, &invalid):
			writeValidationError(w, invalid)
			return
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
			slog.ErrorContext(req.Context(), "could not declare the support", "error", err)
			writeError(w, http.StatusServiceUnavailable, "supports_unavailable", "the upload could not be prepared; retry")
			return
		}
		f := up.File
		writeJSON(w, http.StatusCreated, declareSupportResponse{
			SupportID: f.ID, FileName: f.FileName, ContentType: f.ContentType(), ByteSize: f.ByteSize, SHA256: f.SHA256,
			UploadURL: up.URL, UploadHeaders: up.Headers, ExpiresAt: up.ExpiresAt.UTC(),
		})
	}
}

func confirmSupport(svc SupportUploader) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id := chi.URLParam(req, "support_id")
		if !domain.ValidSupportID(id) {
			writeError(w, http.StatusNotFound, "support_not_found", "there is no support with that id")
			return
		}
		claims, _ := security.ClaimsFromContext(req.Context())
		f, err := svc.Confirm(req.Context(), claims.Subject, id)
		switch {
		case err == nil:
		case errors.Is(err, domain.ErrSupportNotFound):
			writeError(w, http.StatusNotFound, "support_not_found", "there is no support with that id")
			return
		case errors.Is(err, domain.ErrSupportNotUploaded):
			writeError(w, http.StatusUnprocessableEntity, "support_not_uploaded",
				"the file has not reached the storage yet; upload it and confirm again")
			return
		case errors.Is(err, domain.ErrSupportMismatch):
			writeError(w, http.StatusUnprocessableEntity, "support_mismatch",
				"the uploaded file is not the one declared (size or digest)")
			return
		case errors.Is(err, domain.ErrUnsupportedFileType):
			writeError(w, http.StatusUnsupportedMediaType, "unsupported_file_type",
				"the content of the file is not the JPG, PNG or PDF that was declared")
			return
		default:
			slog.ErrorContext(req.Context(), "could not confirm the support", "error", err)
			writeError(w, http.StatusServiceUnavailable, "supports_unavailable", "the support could not be checked; retry")
			return
		}
		writeJSON(w, http.StatusOK, confirmedSupportResponse{
			SupportID: f.ID, FileName: f.FileName, ContentType: f.ContentType(), ByteSize: f.ByteSize,
			SHA256: f.SHA256, ConfirmedAt: f.ConfirmedAt.UTC(),
		})
	}
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
		writeJSON(w, status, detailBody(c, nil))
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

// evidenceViewBody is a photograph or video captured by the insured in the place
// of the incident (HU-M2.1-01). DownloadURL is signed and short-lived; it is
// null when it could not be signed.
type evidenceViewBody struct {
	Kind              string     `json:"kind"`
	ContentType       string     `json:"content_type"`
	CapturedAt        time.Time  `json:"captured_at"`
	Latitude          float64    `json:"latitude"`
	Longitude         float64    `json:"longitude"`
	AccuracyM         float64    `json:"accuracy_m"`
	LocationTrusted   bool       `json:"location_trusted"`
	ByteSize          int64      `json:"byte_size"`
	DurationS         int        `json:"duration_s"`
	DownloadURL       *string    `json:"download_url"`
	DownloadExpiresAt *time.Time `json:"download_expires_at"`
}

type claimDetailBody struct {
	claimSummaryBody
	Place       string              `json:"place"`
	Description string              `json:"description"`
	Supports    supportsSummaryBody `json:"supports"`
	Evidence    []evidenceViewBody  `json:"evidence"`
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

func evidenceBodies(links []filing.EvidenceLink) []evidenceViewBody {
	out := make([]evidenceViewBody, 0, len(links))
	for _, l := range links {
		e := l.Evidence
		body := evidenceViewBody{
			Kind: string(e.Kind), ContentType: e.Kind.ContentType(), CapturedAt: e.CapturedAt.UTC(),
			Latitude: e.Latitude, Longitude: e.Longitude, AccuracyM: e.AccuracyM, LocationTrusted: e.LocationTrusted,
			ByteSize: e.ByteSize, DurationS: e.DurationS,
		}
		if l.URL != "" {
			url, expires := l.URL, l.ExpiresAt.UTC()
			body.DownloadURL, body.DownloadExpiresAt = &url, &expires
		}
		out = append(out, body)
	}
	return out
}

// detailBody is the detail of a claim. links are the evidence of the claim,
// signed by the caller; the answer of a filing passes none because a web
// claim has no evidence.
func detailBody(c domain.ClaimRecord, links []filing.EvidenceLink) claimDetailBody {
	s := c.SupportSummary()
	out := claimDetailBody{
		claimSummaryBody: summaryBody(c), Place: c.Place, Description: c.Description,
		Supports: supportsSummaryBody{RequiredTotal: s.RequiredTotal, RequiredAttached: s.RequiredAttached,
			Files: s.Files, Items: []supportItemBody{}},
		Evidence: evidenceBodies(links),
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
			writeJSON(w, http.StatusOK, detailBody(c, svc.EvidenceLinks(req.Context(), c)))
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
