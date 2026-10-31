package filing

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Page sizes of the list of claims.
const (
	DefaultPageSize = 20
	MaxPageSize     = 50
)

// ErrInvalidQuery means a filter or the page token of the list is not valid.
var ErrInvalidQuery = errors.New("claims: invalid query of the claims")

// QueryService reads the insured's own claims, from both channels: the list,
// the detail and the filing receipt (HU-W2.1-01 criteria 4 and 6).
type QueryService struct {
	claims   ports.ClaimRecords
	receipts ports.ReceiptRenderer
	objects  ports.ObjectStore
}

// EvidenceLinkTTL is how long the signed read of a piece of evidence works.
const EvidenceLinkTTL = 10 * time.Minute

// WithObjects lets the detail of a claim filed from the mobile app link its
// evidence: the photographs and the video are read from the object store with
// a short-lived signed URL. Without it the detail lists the evidence without
// links.
func (s *QueryService) WithObjects(objects ports.ObjectStore) *QueryService {
	s.objects = objects
	return s
}

// EvidenceLink is a piece of evidence of a claim and, when it could be
// signed, where to read it.
type EvidenceLink struct {
	Evidence  domain.Evidence
	URL       string
	ExpiresAt time.Time
}

// EvidenceLinks lists the evidence of the claim in the order it was captured.
// A read that cannot be signed comes without URL: the rest of the detail is
// still worth showing.
func (s *QueryService) EvidenceLinks(ctx context.Context, c domain.ClaimRecord) []EvidenceLink {
	out := make([]EvidenceLink, 0, len(c.Evidence))
	for _, e := range c.Evidence {
		link := EvidenceLink{Evidence: e}
		if s.objects != nil {
			signed, err := s.objects.SignDownload(ctx, e.ObjectKey, EvidenceLinkTTL)
			if err != nil {
				slog.WarnContext(ctx, "could not sign the read of the evidence", "claim_id", c.ID, "error", err)
			} else {
				link.URL, link.ExpiresAt = signed.URL, signed.ExpiresAt
			}
		}
		out = append(out, link)
	}
	return out
}

// NewQueryService builds the service.
func NewQueryService(claims ports.ClaimRecords, receipts ports.ReceiptRenderer) *QueryService {
	return &QueryService{claims: claims, receipts: receipts}
}

// ClaimQuery is the list the insured asks for. From is a day, YYYY-MM-DD, read
// in Colombian time. PageSize 0 is DefaultPageSize.
type ClaimQuery struct {
	Status    string
	PolicyID  string
	From      string
	PageSize  int
	PageToken string
}

// ClaimPage is one page of the list. NextPageToken is empty on the last page.
type ClaimPage struct {
	Claims        []domain.ClaimRecord
	NextPageToken string
}

// List returns one page of the subject's claims, newest first.
func (s *QueryService) List(ctx context.Context, subject string, q ClaimQuery) (ClaimPage, error) {
	filter, size, after, err := q.parse()
	if err != nil {
		return ClaimPage{}, err
	}
	// One more than the page tells whether there is a next page.
	claims, err := s.claims.ListByOwner(ctx, subject, filter, after, size+1)
	if err != nil {
		return ClaimPage{}, fmt.Errorf("list the claims: %w", err)
	}
	page := ClaimPage{Claims: claims}
	if len(claims) > size {
		page.Claims = claims[:size]
		last := page.Claims[size-1]
		page.NextPageToken = encodeCursor(ports.ClaimCursor{FiledAt: last.FiledAt, ID: last.ID})
	}
	return page, nil
}

// Get returns the claim when the caller filed it. Anyone else gets
// security.ErrForbidden; an unknown id, ports.ErrClaimNotFound.
func (s *QueryService) Get(ctx context.Context, id string) (domain.ClaimRecord, error) {
	c, err := s.claims.Get(ctx, id)
	if err != nil {
		return domain.ClaimRecord{}, err
	}
	// HU-W2.1-03 will let the claims advisor read it too.
	if err := security.RequireOwnerOrRole(ctx, c.Subject); err != nil {
		return domain.ClaimRecord{}, err
	}
	return c, nil
}

// Receipt returns the claim and its filing receipt in PDF, with the same
// rules as Get.
func (s *QueryService) Receipt(ctx context.Context, id string) (domain.ClaimRecord, []byte, error) {
	c, err := s.Get(ctx, id)
	if err != nil {
		return domain.ClaimRecord{}, nil, err
	}
	pdf, err := s.receipts.Render(c.Receipt())
	if err != nil {
		return domain.ClaimRecord{}, nil, fmt.Errorf("render the receipt: %w", err)
	}
	return c, pdf, nil
}

func (q ClaimQuery) parse() (ports.ClaimFilter, int, *ports.ClaimCursor, error) {
	var f ports.ClaimFilter
	if q.Status != "" && !domain.Known(domain.Status(q.Status)) {
		return f, 0, nil, fmt.Errorf("%w: status", ErrInvalidQuery)
	}
	f.Status = domain.Status(q.Status)
	f.PolicyID = q.PolicyID
	if q.From != "" {
		day, err := time.ParseInLocation(time.DateOnly, q.From, domain.ColombiaTime)
		if err != nil {
			return f, 0, nil, fmt.Errorf("%w: from", ErrInvalidQuery)
		}
		f.FiledFrom = day.UTC()
	}

	size := q.PageSize
	switch {
	case size == 0:
		size = DefaultPageSize
	case size < 0 || size > MaxPageSize:
		return f, 0, nil, fmt.Errorf("%w: page_size", ErrInvalidQuery)
	}

	if q.PageToken == "" {
		return f, size, nil, nil
	}
	after, err := decodeCursor(q.PageToken)
	if err != nil {
		return f, 0, nil, fmt.Errorf("%w: page_token", ErrInvalidQuery)
	}
	return f, size, &after, nil
}

// cursorBody is the opaque page token: base64 of this JSON.
type cursorBody struct {
	FiledAt time.Time `json:"t"`
	ID      string    `json:"i"`
}

func encodeCursor(c ports.ClaimCursor) string {
	raw, _ := json.Marshal(cursorBody{FiledAt: c.FiledAt.UTC(), ID: c.ID})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(token string) (ports.ClaimCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return ports.ClaimCursor{}, err
	}
	var b cursorBody
	if err := json.Unmarshal(raw, &b); err != nil {
		return ports.ClaimCursor{}, err
	}
	if b.ID == "" || b.FiledAt.IsZero() {
		return ports.ClaimCursor{}, errors.New("incomplete cursor")
	}
	return ports.ClaimCursor{FiledAt: b.FiledAt, ID: b.ID}, nil
}
