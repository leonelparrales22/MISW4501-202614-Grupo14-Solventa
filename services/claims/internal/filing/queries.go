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
	// activity and clock read the last change of each claim and the tally of
	// the insured's claims (HU-W2.1-02); nil until WithActivity.
	activity ports.ClaimActivity
	clock    ports.Clock
}

// WithActivity makes the list and the detail say when each claim last changed
// status, and the list carry the tally of the insured's claims (HU-W2.1-02).
// clock tells the year whose payouts the tally adds up.
func (s *QueryService) WithActivity(activity ports.ClaimActivity, clock ports.Clock) *QueryService {
	s.activity, s.clock = activity, clock
	return s
}

// SupportLinkTTL is how long the signed read of a support works.
const SupportLinkTTL = 10 * time.Minute

// SupportLink is a support of the claim and where to read it.
type SupportLink struct {
	File      domain.SupportFile
	URL       string
	ExpiresAt time.Time
}

// ErrDownloadsUnavailable means there is no object store to sign the read.
var ErrDownloadsUnavailable = errors.New("claims: the supports cannot be downloaded now")

// SupportLink signs the read of a support the claim shows (HU-W2.1-02, screen
// A8), with the rules of Get: only the insured who filed it. A support that is
// not one of the claim, or that a resubmission replaced, is
// domain.ErrSupportNotFound.
func (s *QueryService) SupportLink(ctx context.Context, claimID, supportID string) (SupportLink, error) {
	c, err := s.Get(ctx, claimID)
	if err != nil {
		return SupportLink{}, err
	}
	for _, a := range c.Supports {
		if a.File.ID != supportID {
			continue
		}
		if s.objects == nil {
			return SupportLink{}, ErrDownloadsUnavailable
		}
		signed, err := s.objects.SignDownload(ctx, a.File.ObjectKey(), SupportLinkTTL)
		if err != nil {
			return SupportLink{}, fmt.Errorf("sign the read of the support: %w", err)
		}
		return SupportLink{File: a.File, URL: signed.URL, ExpiresAt: signed.ExpiresAt}, nil
	}
	return SupportLink{}, domain.ErrSupportNotFound
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
	// Summary is the tally of every claim of the insured, whatever the
	// filters and the page (HU-W2.1-02); nil without WithActivity.
	Summary *domain.ClaimTally
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
	if s.activity == nil {
		return page, nil
	}
	if err := s.withLastChanges(ctx, page.Claims); err != nil {
		return ClaimPage{}, err
	}
	tally, err := s.activity.Tally(ctx, subject, domain.StartOfYear(s.clock.Now()))
	if err != nil {
		return ClaimPage{}, fmt.Errorf("tally the claims: %w", err)
	}
	page.Summary = &tally
	return page, nil
}

// withLastChanges sets when each claim last changed status; a claim without
// a recorded change keeps the time it was filed.
func (s *QueryService) withLastChanges(ctx context.Context, claims []domain.ClaimRecord) error {
	ids := make([]string, 0, len(claims))
	for _, c := range claims {
		ids = append(ids, c.ID)
	}
	changes, err := s.activity.LastChanges(ctx, ids)
	if err != nil {
		return fmt.Errorf("read the last change of the claims: %w", err)
	}
	for i := range claims {
		claims[i].LastChangedAt = claims[i].FiledAt
		if at, ok := changes[claims[i].ID]; ok {
			claims[i].LastChangedAt = at
		}
	}
	return nil
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
	if s.activity != nil {
		one := []domain.ClaimRecord{c}
		if err := s.withLastChanges(ctx, one); err != nil {
			return domain.ClaimRecord{}, err
		}
		c = one[0]
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
