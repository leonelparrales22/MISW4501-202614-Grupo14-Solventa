// Package evaluation is what the operations analyst reads to evaluate a claim
// (HU-W2.2-03): the claims in evaluation and the detail of one, with the
// terms of its policy, who the insured is, what they sent and the history of
// its priority. Deciding and paying is not here: this package only reads.
package evaluation

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/filing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/triage"
)

// Page sizes of the claims in evaluation.
const (
	DefaultPageSize = 20
	MaxPageSize     = 50
)

// SupportLinkTTL is how long the signed read of a support works, the same as
// the evidence of the phone.
const SupportLinkTTL = filing.EvidenceLinkTTL

// ErrInvalidQuery means the page size or the page token is not valid.
var ErrInvalidQuery = errors.New("claims: invalid query of the claims in evaluation")

// TriageReader is the priority of the claims (HU-W2.2-01).
type TriageReader interface {
	Summaries(ctx context.Context, ids []string) ([]triage.Summary, error)
	View(ctx context.Context, claimID string) (triage.View, error)
}

// EvidenceSigner signs the reads of the evidence of a claim.
type EvidenceSigner interface {
	EvidenceLinks(ctx context.Context, c domain.ClaimRecord) []filing.EvidenceLink
}

// Service reads the claims operations evaluates.
type Service struct {
	claims   ports.ClaimRecords
	queue    ports.ClaimQueue
	triage   TriageReader
	evidence EvidenceSigner
	terms    ports.PolicyTermsReader
	objects  ports.ObjectStore
	// decisions is nil until WithDecisions: then the detail carries no
	// decision.
	decisions DecisionReader
}

// DecisionReader is the decision of a claim (HU-W2.2-03), or
// ports.ErrDecisionNotFound.
type DecisionReader interface {
	Decision(ctx context.Context, claimID string) (domain.Decision, error)
}

// WithDecisions makes the detail carry the decision of the claim, when it has
// one.
func (s *Service) WithDecisions(decisions DecisionReader) *Service {
	s.decisions = decisions
	return s
}

// NewService builds the service. terms and objects may be nil: without the
// policy core the detail says so, and without the object store the supports
// come without links.
func NewService(claims ports.ClaimRecords, queue ports.ClaimQueue, priorities TriageReader,
	evidence EvidenceSigner, terms ports.PolicyTermsReader, objects ports.ObjectStore) *Service {
	return &Service{claims: claims, queue: queue, triage: priorities, evidence: evidence, terms: terms, objects: objects}
}

// QueueItem is a claim in evaluation with its priority.
type QueueItem struct {
	Claim  domain.ClaimRecord
	Triage triage.Summary
}

// QueuePage is one page of the claims in evaluation. NextPageToken is empty on
// the last page.
type QueuePage struct {
	Items         []QueueItem
	NextPageToken string
}

// Queue returns the claims in evaluation, oldest filed first. They are the
// claims the triage evaluates (active and in_evaluation): while the review of
// the advisor (HU-W2.1-03) does not exist, a filed claim enters evaluation as
// it is filed (assumption S5 of HU-W2.2-01). The order by priority is
// HU-W2.2-02; this list is what the evaluation needs to open a claim.
func (s *Service) Queue(ctx context.Context, pageSize int, pageToken string) (QueuePage, error) {
	return s.page(ctx, domain.TriageableStatuses(), pageSize, pageToken)
}

// Exceptions returns the payment exceptions, oldest filed first: the approved
// claims whose payout failed and wait for the analyst to retry it (screen
// O6b), with the priority they had when they were decided.
func (s *Service) Exceptions(ctx context.Context, pageSize int, pageToken string) (QueuePage, error) {
	return s.page(ctx, []domain.Status{domain.StatusPaymentException}, pageSize, pageToken)
}

// page returns one page of the claims in the statuses with their priority.
func (s *Service) page(ctx context.Context, statuses []domain.Status, pageSize int, pageToken string) (QueuePage, error) {
	switch {
	case pageSize == 0:
		pageSize = DefaultPageSize
	case pageSize < 1 || pageSize > MaxPageSize:
		return QueuePage{}, ErrInvalidQuery
	}
	after, err := decodeCursor(pageToken)
	if err != nil {
		return QueuePage{}, err
	}
	// One more than the page tells whether there is a next page.
	claims, err := s.queue.ListByStatus(ctx, statuses, after, pageSize+1)
	if err != nil {
		return QueuePage{}, fmt.Errorf("list the claims in %v: %w", statuses, err)
	}
	var page QueuePage
	if len(claims) > pageSize {
		claims = claims[:pageSize]
		last := claims[len(claims)-1]
		page.NextPageToken = encodeCursor(ports.ClaimCursor{FiledAt: last.FiledAt, ID: last.ID})
	}
	if len(claims) == 0 {
		return page, nil
	}
	ids := make([]string, 0, len(claims))
	for _, c := range claims {
		ids = append(ids, c.ID)
	}
	summaries, err := s.triage.Summaries(ctx, ids)
	if err != nil {
		return QueuePage{}, fmt.Errorf("read the priority of the claims: %w", err)
	}
	byID := make(map[string]triage.Summary, len(summaries))
	for _, sum := range summaries {
		byID[sum.ClaimID] = sum
	}
	for _, c := range claims {
		page.Items = append(page.Items, QueueItem{Claim: c, Triage: byID[c.ID]})
	}
	return page, nil
}

// SupportLink is a support of a web claim and, when it could be signed, where
// to read it.
type SupportLink struct {
	Support   domain.AttachedSupport
	URL       string
	ExpiresAt time.Time
}

// Evaluation is everything the analyst needs to decide (screen O6).
type Evaluation struct {
	Claim    domain.ClaimRecord
	Evidence []filing.EvidenceLink
	Supports []SupportLink
	Triage   triage.View
	// Terms is nil when the core does not know the policy any more, or when
	// it did not answer (PolicyUnavailable).
	Terms *domain.PolicyTerms
	// Coverage is the coverage of the incident of the claim, nil when the
	// policy does not cover it or its terms are unknown.
	Coverage *domain.Coverage
	// Holder is nil when the core does not know the insured, or when it did
	// not answer (PolicyUnavailable).
	Holder *domain.Holder
	// PolicyUnavailable is true when the policy core did not answer: the rest
	// of the claim is still worth reading, but nothing can be decided on it.
	PolicyUnavailable bool
	// Decision is what operations decided, nil while the claim is in
	// evaluation.
	Decision *domain.Decision
}

// Detail returns the claim with everything around it, or ports.ErrClaimNotFound.
func (s *Service) Detail(ctx context.Context, claimID string) (Evaluation, error) {
	c, err := s.claims.Get(ctx, claimID)
	if err != nil {
		return Evaluation{}, err
	}
	view, err := s.triage.View(ctx, claimID)
	if err != nil {
		return Evaluation{}, fmt.Errorf("read the priority of the claim: %w", err)
	}
	out := Evaluation{Claim: c, Triage: view, Supports: s.supportLinks(ctx, c)}
	if s.evidence != nil {
		out.Evidence = s.evidence.EvidenceLinks(ctx, c)
	}
	if s.decisions != nil {
		d, err := s.decisions.Decision(ctx, claimID)
		switch {
		case errors.Is(err, ports.ErrDecisionNotFound):
		case err != nil:
			return Evaluation{}, fmt.Errorf("read the decision of the claim: %w", err)
		default:
			out.Decision = &d
		}
	}

	if s.terms == nil {
		out.PolicyUnavailable = true
		return out, nil
	}
	terms, err := s.terms.PolicyTerms(ctx, c.Subject, c.PolicyID)
	switch {
	case errors.Is(err, ports.ErrPolicyNotFound):
		slog.WarnContext(ctx, "the policy core no longer knows the policy of the claim", "claim_id", c.ID)
	case err != nil:
		slog.WarnContext(ctx, "could not read the terms of the policy of the claim", "claim_id", c.ID, "error", err)
		out.PolicyUnavailable = true
	default:
		out.Terms = &terms
		if cov, ok := terms.CoverageOf(c.IncidentType); ok {
			out.Coverage = &cov
		}
	}
	holder, err := s.terms.Holder(ctx, c.Subject)
	switch {
	case errors.Is(err, ports.ErrHolderNotFound):
		slog.WarnContext(ctx, "the policy core does not know the insured of the claim", "claim_id", c.ID)
	case err != nil:
		slog.WarnContext(ctx, "could not read the insured of the claim", "claim_id", c.ID, "error", err)
		out.PolicyUnavailable = true
	default:
		out.Holder = &holder
	}
	return out, nil
}

// supportLinks signs the read of every support of a web claim. A read that
// cannot be signed comes without URL: the rest of the detail is still worth
// showing.
func (s *Service) supportLinks(ctx context.Context, c domain.ClaimRecord) []SupportLink {
	out := make([]SupportLink, 0, len(c.Supports))
	for _, a := range c.Supports {
		link := SupportLink{Support: a}
		if s.objects != nil {
			signed, err := s.objects.SignDownload(ctx, a.File.ObjectKey(), SupportLinkTTL)
			if err != nil {
				slog.WarnContext(ctx, "could not sign the read of the support", "claim_id", c.ID, "error", err)
			} else {
				link.URL, link.ExpiresAt = signed.URL, signed.ExpiresAt
			}
		}
		out = append(out, link)
	}
	return out
}

// The page token is the cursor of the last claim of the page: when it was
// filed and its id. It is opaque to the caller.
func encodeCursor(c ports.ClaimCursor) string {
	return base64.RawURLEncoding.EncodeToString([]byte(c.FiledAt.UTC().Format(time.RFC3339Nano) + "|" + c.ID))
}

func decodeCursor(token string) (*ports.ClaimCursor, error) {
	if token == "" {
		return nil, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, ErrInvalidQuery
	}
	at, id, ok := strings.Cut(string(raw), "|")
	if !ok || id == "" {
		return nil, ErrInvalidQuery
	}
	filedAt, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return nil, ErrInvalidQuery
	}
	return &ports.ClaimCursor{FiledAt: filedAt, ID: id}, nil
}
