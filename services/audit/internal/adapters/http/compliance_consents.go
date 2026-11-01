package httpadapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

// ComplianceConsentLister is the use case behind the list of consents of the
// compliance portal (HU-W4.2-01).
type ComplianceConsentLister interface {
	List(ctx context.Context, q domain.ComplianceQuery) (domain.CompliancePage, error)
}

// WithComplianceConsents mounts the list of consents of the compliance portal
// (HU-W4.2-01), a human route: the Cognito JWT is verified with pkg/security
// and only the operations role reaches it.
//
//	GET /v1/audit/consents  the page of the list with its filters, totals, indicators and entities
//
// The same path keeps its POST, the grant of the partner channel of
// WithConsents (API key at the API Gateway, no token): chi keeps one handler,
// and its own guards, per method (decision D-1 a). The query string is never
// logged: it can carry a full document.
func WithComplianceConsents(svc ComplianceConsentLister, verifier security.Verifier) Option {
	return func(r chi.Router) {
		r.Group(func(r chi.Router) {
			r.Use(noStore, security.Authenticate(verifier), security.RequireRole(security.RoleOperations))
			r.Get("/v1/audit/consents", listComplianceConsents(svc))
		})
	}
}

type complianceSummaryRevocationBody struct {
	EffectiveAt  time.Time `json:"effective_at"`
	Channel      string    `json:"channel"`
	NoticeStatus string    `json:"notice_status"`
}

// complianceSummaryBody is one row of the list: who (name and masked
// document, decision D-4 a), when it was granted and until when, its status
// derived now, how it was revoked if it was, how it was granted and what it
// authorizes.
type complianceSummaryBody struct {
	ConsentID    string                           `json:"consent_id"`
	Holder       complianceHolderBody             `json:"holder"`
	GrantedAt    time.Time                        `json:"granted_at"`
	ExpiresAt    time.Time                        `json:"expires_at"`
	EndedAt      *time.Time                       `json:"ended_at"`
	Status       string                           `json:"status"`
	Revocation   *complianceSummaryRevocationBody `json:"revocation"`
	GrantChannel string                           `json:"grant_channel"`
	Scope        []sourceTermBody                 `json:"scope"`
	Entities     []entityBody                     `json:"entities"`
}

type revokedCountBody struct {
	Total            int `json:"total"`
	MobileApp        int `json:"mobile_app"`
	CompliancePortal int `json:"compliance_portal"`
}

type complianceIndicatorsBody struct {
	Active                   int              `json:"active"`
	Expired                  int              `json:"expired"`
	RevokedLast30Days        revokedCountBody `json:"revoked_last_30_days"`
	RevocationsOutOfDeadline int              `json:"revocations_out_of_deadline"`
	AsOf                     time.Time        `json:"as_of"`
}

type complianceConsentPageBody struct {
	Items      []complianceSummaryBody  `json:"items"`
	Page       int                      `json:"page"`
	PageSize   int                      `json:"page_size"`
	TotalItems int                      `json:"total_items"`
	TotalPages int                      `json:"total_pages"`
	Indicators complianceIndicatorsBody `json:"indicators"`
	Entities   []entityBody             `json:"entities"`
}

func complianceSummaryFrom(v domain.ConsentView) complianceSummaryBody {
	c := v.Consent
	out := complianceSummaryBody{
		ConsentID: c.ID,
		Holder:    complianceHolderBody{FullName: c.Holder.FullName, DocumentMasked: domain.MaskDocument(c.Holder.DocumentID)},
		GrantedAt: c.GrantedAt.UTC(), ExpiresAt: c.ExpiresAt().UTC(), EndedAt: optionalTime(v.EndedAt),
		Status: string(v.Status), GrantChannel: string(c.Channel), Scope: sourceBodies(c.Scope),
		Entities: make([]entityBody, 0, len(v.Entities)),
	}
	for _, e := range v.Entities {
		out.Entities = append(out.Entities, entityBody{Code: e.Code, Name: e.Name})
	}
	if r := v.Revocation; r != nil {
		out.Revocation = &complianceSummaryRevocationBody{EffectiveAt: r.EffectiveAt.UTC(), Channel: r.Channel, NoticeStatus: string(r.Summary)}
	}
	return out
}

func compliancePageFrom(p domain.CompliancePage) complianceConsentPageBody {
	ind := p.Indicators
	out := complianceConsentPageBody{
		Items: make([]complianceSummaryBody, 0, len(p.Items)),
		Page:  p.Page, PageSize: p.PageSize, TotalItems: p.TotalItems, TotalPages: p.TotalPages,
		Indicators: complianceIndicatorsBody{
			Active: ind.Active, Expired: ind.Expired,
			RevokedLast30Days: revokedCountBody{
				Total: ind.RevokedLast30Days.Total, MobileApp: ind.RevokedLast30Days.MobileApp,
				CompliancePortal: ind.RevokedLast30Days.CompliancePortal,
			},
			RevocationsOutOfDeadline: ind.OutOfDeadline, AsOf: ind.AsOf.UTC(),
		},
		Entities: make([]entityBody, 0, len(p.Entities)),
	}
	for _, v := range p.Items {
		out.Items = append(out.Items, complianceSummaryFrom(v))
	}
	for _, e := range p.Entities {
		out.Entities = append(out.Entities, entityBody{Code: e.Code, Name: e.Name})
	}
	return out
}

func listComplianceConsents(svc ComplianceConsentLister) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		v := req.URL.Query()
		q, err := domain.ComplianceListRequest{
			Status: v.Get("status"), GrantedFrom: v.Get("granted_from"), GrantedTo: v.Get("granted_to"),
			Entity: v.Get("entity"), Search: v.Get("q"), Page: v.Get("page"), PageSize: v.Get("page_size"),
		}.Validate()
		var verr *domain.ValidationError
		if errors.As(err, &verr) {
			writeValidation(w, verr)
			return
		}
		page, err := svc.List(req.Context(), q)
		if err != nil {
			slog.ErrorContext(req.Context(), "could not list the consents for compliance", "error", err)
			writeError(w, http.StatusServiceUnavailable, "service_unavailable", "the consents could not be listed; try again")
			return
		}
		writeJSON(w, http.StatusOK, compliancePageFrom(page))
	}
}
