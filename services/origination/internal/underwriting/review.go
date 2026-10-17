// Package underwriting owns the human decision of the requests that the
// automatic underwriting routes to the advisor inbox (HU-W1.1-02): the advisor
// lists and opens them, decides (accept, adjust the premium or reject) and the
// origination flow goes on by itself. Every decision is recorded append-only
// with the advisor, the date and time, the type and the observations (CA-4).
package underwriting

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// AdjustedOfferValidity is the validity of the offer an adjust decision
// creates, counted from the decision (HU-W1.2-03: 24 hours).
const AdjustedOfferValidity = 24 * time.Hour

// ErrDecisionNotRegistered means the decision could not be recorded; nothing
// changed and retrying is safe.
var ErrDecisionNotRegistered = errors.New("the decision could not be registered")

// ErrContinuationPending means the decision is recorded but the origination
// flow did not continue yet; retrying the same decision completes it.
var ErrContinuationPending = errors.New("the decision is registered but the origination flow did not continue yet")

// ReviewDeps are the ports the human decision needs.
type ReviewDeps struct {
	Reviews     ports.ReviewRepository
	Acceptances ports.AcceptanceRepository
	Offers      ports.OfferRepository
	OfferWriter ports.OfferWriter
	Clock       ports.Clock
	// AcceptanceResumed is called after an accept decision moved the
	// acceptance back to registered, so the acceptance processor starts the
	// issuance at once (issuance.Processor.Notify). It may be nil.
	AcceptanceResumed func()
	// NotificationScheduled turns the customer notice of an adjust or reject
	// decision on (decision D-3 c): the notice is scheduled in the case and
	// this function wakes the worker that sends it. When nil (the e-mail
	// channel is not configured) no notice is scheduled.
	NotificationScheduled func()
}

// ReviewDetail is a case of the inbox with the mode and the validity of the
// routed offer; both are zero when the offer could not be read.
type ReviewDetail struct {
	Case           domain.ReviewCase
	OfferMode      domain.OfferMode
	OfferExpiresAt time.Time
}

// ReviewService lists, opens and decides the requests of the advisor inbox.
// Every operation requires the advisor role (defense in depth: the HTTP routes
// check it too).
type ReviewService struct {
	d ReviewDeps
}

// NewReviewService builds a ReviewService.
func NewReviewService(d ReviewDeps) *ReviewService {
	if d.AcceptanceResumed == nil {
		d.AcceptanceResumed = func() {}
	}
	return &ReviewService{d: d}
}

// List returns the cases that match the filter, ordered by arrival, and the
// counts per status for the filter reason (CA-1, CA-5).
func (s *ReviewService) List(ctx context.Context, f domain.ReviewFilter) ([]domain.ReviewCase, domain.ReviewCounts, error) {
	if err := security.RequireAnyRole(ctx, security.RoleAdvisor); err != nil {
		return nil, domain.ReviewCounts{}, err
	}
	return s.d.Reviews.List(ctx, f)
}

// Get returns a case with the mode and the validity of its offer.
func (s *ReviewService) Get(ctx context.Context, acceptanceID string) (ReviewDetail, error) {
	if err := security.RequireAnyRole(ctx, security.RoleAdvisor); err != nil {
		return ReviewDetail{}, err
	}
	c, err := s.d.Reviews.Get(ctx, acceptanceID)
	if err != nil {
		return ReviewDetail{}, err
	}
	out := ReviewDetail{Case: c}
	offer, err := s.d.Offers.Get(ctx, c.Request.OfferID)
	if err != nil {
		slog.WarnContext(ctx, "could not read the offer of a review request", "acceptance_id", acceptanceID,
			"offer_id", c.Request.OfferID, "error", err)
		return out, nil
	}
	out.OfferMode, out.OfferExpiresAt = offer.Mode, offer.ExpiresAt
	return out, nil
}

// Decide records the advisor decision on a pending case and continues the
// origination flow (CA-2, CA-3, CA-4). The decision is persisted before
// acting on it: if the flow cannot continue, ErrContinuationPending is
// returned and retrying the same request completes it. A case that is already
// decided returns ports.ErrReviewAlreadyDecided after completing its
// continuation, if it was still pending.
func (s *ReviewService) Decide(ctx context.Context, acceptanceID string, in domain.DecisionInput) (domain.ReviewCase, error) {
	if err := security.RequireAnyRole(ctx, security.RoleAdvisor); err != nil {
		return domain.ReviewCase{}, err
	}
	caller, _ := security.ClaimsFromContext(ctx)
	if err := in.Validate(); err != nil {
		return domain.ReviewCase{}, err
	}

	c, err := s.d.Reviews.Get(ctx, acceptanceID)
	if err != nil {
		return domain.ReviewCase{}, err
	}
	if c.Status == domain.ReviewDecided {
		return s.alreadyDecided(ctx, c)
	}
	a, err := s.d.Acceptances.Get(ctx, acceptanceID)
	if err != nil {
		return domain.ReviewCase{}, err
	}
	if a.Status != domain.AcceptanceInReview {
		return domain.ReviewCase{}, fmt.Errorf("%w: the acceptance is %s", ports.ErrReviewAlreadyDecided, a.Status)
	}

	id, err := newDecisionID()
	if err != nil {
		return domain.ReviewCase{}, fmt.Errorf("%w: %w", ErrDecisionNotRegistered, err)
	}
	now := s.d.Clock.Now().UTC()
	d := domain.HumanDecision{
		ID:           id,
		AcceptanceID: acceptanceID,
		Type:         in.Type,
		Advisor:      domain.Advisor{ID: caller.Subject, Email: caller.Email},
		DecidedAt:    now,
		Observations: strings.TrimSpace(in.Observations),
	}
	if in.Type == domain.DecisionAdjust {
		pct := *in.PremiumAdjustmentPct
		d.PremiumAdjustmentPct = &pct
		if d.AdjustedOffer, err = s.createAdjustedOffer(ctx, a, pct, now); err != nil {
			return domain.ReviewCase{}, fmt.Errorf("%w: %w", ErrDecisionNotRegistered, err)
		}
	}

	stored, err := s.d.Reviews.RecordDecision(ctx, d)
	switch {
	case errors.Is(err, ports.ErrReviewAlreadyDecided):
		// Another advisor won the claim: show what they decided.
		if c, err = s.d.Reviews.Get(ctx, acceptanceID); err != nil {
			return domain.ReviewCase{}, err
		}
		return s.alreadyDecided(ctx, c)
	case err != nil:
		return domain.ReviewCase{}, fmt.Errorf("%w: %w", ErrDecisionNotRegistered, err)
	}
	slog.InfoContext(ctx, "human decision recorded", "acceptance_id", acceptanceID, "decision_id", d.ID,
		"type", d.Type, "advisor_id", d.Advisor.ID, "decided_at", d.DecidedAt)

	if err := s.continueFlow(ctx, stored); err != nil {
		slog.WarnContext(ctx, "the origination flow did not continue after the human decision; retrying the decision completes it",
			"acceptance_id", acceptanceID, "error", err)
		return stored, fmt.Errorf("%w: %w", ErrContinuationPending, err)
	}
	return s.refreshed(ctx, stored), nil
}

// createAdjustedOffer stores the new offer of an adjust decision: same owner,
// applicant, currency, mode and score as the routed offer, the premium with the
// advisor surcharge and a 24-hour validity. Its id is deterministic, so a retry
// never creates a second offer.
func (s *ReviewService) createAdjustedOffer(ctx context.Context, a domain.Acceptance, pct float64, now time.Time) (*domain.AdjustedOffer, error) {
	offer, err := s.d.Offers.Get(ctx, a.OfferID)
	if err != nil {
		return nil, fmt.Errorf("could not read the routed offer: %w", err)
	}
	adjusted := domain.Offer{
		ID:           domain.AdjustedOfferID(a.ID),
		CustomerID:   offer.CustomerID,
		Applicant:    offer.Applicant,
		Premium:      domain.AdjustPremium(offer.Premium, pct),
		Currency:     offer.Currency,
		Mode:         offer.Mode,
		ProfileScore: offer.ProfileScore,
		ExpiresAt:    now.Add(AdjustedOfferValidity),
	}
	if err := s.d.OfferWriter.Create(ctx, adjusted); err != nil {
		return nil, fmt.Errorf("could not create the adjusted offer: %w", err)
	}
	return &domain.AdjustedOffer{ID: adjusted.ID, Premium: adjusted.Premium, Currency: adjusted.Currency, ExpiresAt: adjusted.ExpiresAt}, nil
}

// alreadyDecided completes the continuation of a decided case, if it was
// still pending, and reports that the case is already decided.
func (s *ReviewService) alreadyDecided(ctx context.Context, c domain.ReviewCase) (domain.ReviewCase, error) {
	if err := s.continueFlow(ctx, c); err != nil {
		return c, fmt.Errorf("%w: %w", ErrContinuationPending, err)
	}
	return s.refreshed(ctx, c), ports.ErrReviewAlreadyDecided
}

// continueFlow moves the acceptance according to the decision (CA-3) and
// schedules the customer notice. Each step is idempotent: the acceptance is
// only moved while it is in review and the notice only scheduled once.
//   - accept: back to registered with an approved underwriting decision, so
//     the acceptance processor starts the issuance with its own retries
//     (decision D-6 a);
//   - adjust: adjusted, with the new offer the customer accepts through the
//     same acceptance flow;
//   - reject: rejected; the case is closed.
func (s *ReviewService) continueFlow(ctx context.Context, c domain.ReviewCase) error {
	d := c.Decision
	if d == nil {
		return errors.New("the case has no decision")
	}
	a, err := s.d.Acceptances.Get(ctx, d.AcceptanceID)
	if err != nil {
		return fmt.Errorf("could not read the acceptance: %w", err)
	}
	now := s.d.Clock.Now().UTC()
	if a.Status == domain.AcceptanceInReview {
		a.Review = &domain.AcceptanceReview{Outcome: d.Type, DecidedAt: d.DecidedAt}
		if d.AdjustedOffer != nil {
			o := *d.AdjustedOffer
			a.Review.AdjustedOffer = &o
		}
		a.NextAttemptAt = time.Time{}
		a.UpdatedAt = now
		switch d.Type {
		case domain.DecisionAccept:
			a.Status = domain.AcceptanceRegistered
			a.Decision = &domain.UnderwritingDecision{Outcome: domain.UnderwritingApproved, DecidedAt: d.DecidedAt}
			a.NextAttemptAt = now
		case domain.DecisionAdjust:
			a.Status = domain.AcceptanceAdjusted
		default:
			a.Status = domain.AcceptanceRejected
		}
		if err := s.d.Acceptances.Update(ctx, a); err != nil {
			return fmt.Errorf("could not update the acceptance: %w", err)
		}
		slog.InfoContext(ctx, "origination flow continued after the human decision", "acceptance_id", a.ID, "status", a.Status)
		if d.Type == domain.DecisionAccept {
			s.d.AcceptanceResumed()
		}
	}

	if d.Type == domain.DecisionAccept || s.d.NotificationScheduled == nil || c.Notification != nil {
		return nil
	}
	if err := s.d.Reviews.UpdateNotification(ctx, d.AcceptanceID, domain.ReviewNotification{
		Channel: domain.ChannelEmail, Status: domain.NotificationPending, NextAttemptAt: now,
	}); err != nil {
		return fmt.Errorf("could not schedule the customer notice: %w", err)
	}
	s.d.NotificationScheduled()
	return nil
}

// refreshed reads the case again, with its scheduled notice; on a read
// failure it returns the case it already had.
func (s *ReviewService) refreshed(ctx context.Context, c domain.ReviewCase) domain.ReviewCase {
	got, err := s.d.Reviews.Get(ctx, c.Request.AcceptanceID)
	if err != nil {
		return c
	}
	return got
}

// newDecisionID returns a random UUID (version 4).
func newDecisionID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("could not generate the decision id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
