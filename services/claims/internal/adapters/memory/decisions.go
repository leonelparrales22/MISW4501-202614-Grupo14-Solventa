package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// DecisionStore keeps the decisions of the claims of a CaseStore
// (HU-W2.2-03) with the same rules as PostgreSQL: every write moves the
// status of the claim too, and the result of a payout is recorded only while
// its claim is held. Everything is lost when the service restarts.
type DecisionStore struct {
	cases *CaseStore

	mu        sync.Mutex
	decisions map[string]domain.Decision
}

// NewDecisionStore builds the store over the claims of cases.
func NewDecisionStore(cases *CaseStore) *DecisionStore {
	return &DecisionStore{cases: cases, decisions: map[string]domain.Decision{}}
}

var _ ports.DecisionStore = (*DecisionStore)(nil)

// setStatus moves the claim. The caller holds s.mu.
func (s *DecisionStore) setStatus(claimID string, st domain.Status) {
	s.cases.mu.Lock()
	defer s.cases.mu.Unlock()
	c := s.cases.byID[claimID]
	c.Status = st
	s.cases.byID[claimID] = c
}

// Decide records the decision of a claim in evaluation.
func (s *DecisionStore) Decide(_ context.Context, d domain.Decision) (domain.Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cases.mu.Lock()
	c, ok := s.cases.byID[d.ClaimID]
	s.cases.mu.Unlock()
	switch {
	case !ok:
		return domain.Decision{}, ports.ErrClaimNotFound
	case !domain.Decidable(c.Status):
		return domain.Decision{}, ports.ErrNotDecidable
	}
	if _, decided := s.decisions[d.ClaimID]; decided {
		return domain.Decision{}, ports.ErrNotDecidable
	}
	d.FilingNumber = c.FilingNumber
	s.decisions[d.ClaimID] = copyDecision(d)
	s.setStatus(d.ClaimID, d.StatusOf())
	return copyDecision(d), nil
}

// Decision returns the decision of a claim.
func (s *DecisionStore) Decision(_ context.Context, claimID string) (domain.Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.decisions[claimID]
	if !ok {
		return domain.Decision{}, ports.ErrDecisionNotFound
	}
	return copyDecision(d), nil
}

func due(p *domain.Payout, now time.Time) bool {
	return p != nil && ((p.Status == idempotency.StatusPending && !p.NextAttemptAt.After(now)) ||
		(p.Status == idempotency.StatusInProgress && p.ClaimedUntil.Before(now)))
}

// ListPayoutsDue returns the claims whose payout is due at now.
func (s *DecisionStore) ListPayoutsDue(_ context.Context, now time.Time, limit int) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for id, d := range s.decisions {
		if due(d.Payout, now) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// Claim moves a due payout to in_progress with a lease.
func (s *DecisionStore) Claim(_ context.Context, claimID string, now time.Time, lease time.Duration) (domain.Decision, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.decisions[claimID]
	if !ok || !due(d.Payout, now) {
		return domain.Decision{}, false, nil
	}
	d.Payout.Status, d.Payout.ClaimedUntil, d.Payout.LastAttemptAt = idempotency.StatusInProgress, now.Add(lease), now
	s.decisions[claimID] = d
	return copyDecision(d), true, nil
}

// held applies change to the payout of rec if its claim is still held, and
// moves the claim to the status the decision is left in.
func (s *DecisionStore) held(rec domain.Decision, change func(p *domain.Payout)) (domain.Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.decisions[rec.ClaimID]
	if !ok || d.Payout == nil || d.Payout.Status != idempotency.StatusInProgress ||
		!d.Payout.ClaimedUntil.Equal(rec.Payout.ClaimedUntil) {
		return domain.Decision{}, idempotency.ErrClaimLost
	}
	change(d.Payout)
	s.decisions[rec.ClaimID] = d
	s.setStatus(rec.ClaimID, d.StatusOf())
	return copyDecision(d), nil
}

// Confirm records the confirmed payout.
func (s *DecisionStore) Confirm(_ context.Context, rec domain.Decision, now time.Time, out idempotency.Outcome) (domain.Decision, error) {
	return s.held(rec, func(p *domain.Payout) {
		p.Status, p.ConfirmedAt, p.Reference = idempotency.StatusConfirmed, now.UTC(), out.Reference
		p.NextAttemptAt, p.ClaimedUntil, p.Failure = time.Time{}, time.Time{}, ""
	})
}

// Reschedule makes the payout pending again with one more failed attempt.
func (s *DecisionStore) Reschedule(_ context.Context, rec domain.Decision, now, next time.Time, cause string) (domain.Decision, error) {
	return s.held(rec, func(p *domain.Payout) {
		p.Status, p.Attempts, p.NextAttemptAt, p.ClaimedUntil = idempotency.StatusPending, p.Attempts+1, next.UTC(), time.Time{}
		p.LastFailureAt, p.LastError = now.UTC(), cause
	})
}

// Fail marks the payout failed.
func (s *DecisionStore) Fail(_ context.Context, rec domain.Decision, now time.Time, reason idempotency.FailureReason, cause string) (domain.Decision, error) {
	return s.held(rec, func(p *domain.Payout) {
		p.Status, p.Attempts, p.NextAttemptAt, p.ClaimedUntil = idempotency.StatusFailed, p.Attempts+1, time.Time{}, time.Time{}
		p.Failure, p.LastFailureAt, p.LastError = domain.PayoutFailureFor(reason), now.UTC(), cause
	})
}

// RecordGatewayCode keeps the gateway's reason of a failed payout.
func (s *DecisionStore) RecordGatewayCode(_ context.Context, claimID, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.decisions[claimID]; ok && d.Payout != nil && d.Payout.Status == idempotency.StatusFailed {
		d.Payout.GatewayCode = code
	}
	return nil
}

// RetryPayout makes a failed payout due again.
func (s *DecisionStore) RetryPayout(_ context.Context, claimID string, r domain.PayoutRetry) (domain.Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.decisions[claimID]
	if !ok || d.Payout == nil || d.Payout.Status != idempotency.StatusFailed {
		s.cases.mu.Lock()
		_, exists := s.cases.byID[claimID]
		s.cases.mu.Unlock()
		if !exists {
			return domain.Decision{}, ports.ErrClaimNotFound
		}
		return domain.Decision{}, ports.ErrPayoutNotRetryable
	}
	p := d.Payout
	p.Status, p.Attempts, p.NextAttemptAt, p.ClaimedUntil = idempotency.StatusPending, 0, r.At.UTC(), time.Time{}
	p.Failure, p.GatewayCode = "", ""
	if r.Destination != nil {
		p.Destination = *r.Destination
	}
	p.ManualRetries, p.LastRetriedBy, p.LastRetriedAt = p.ManualRetries+1, r.By, r.At.UTC()
	s.setStatus(claimID, d.StatusOf())
	return copyDecision(d), nil
}

// ListNoticesDue returns the rejections whose notice is due at now.
func (s *DecisionStore) ListNoticesDue(_ context.Context, now time.Time, limit int) ([]domain.Decision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.Decision
	for _, d := range s.decisions {
		if n := d.Notice; n != nil && n.SentAt.IsZero() && !n.NextAttemptAt.After(now) {
			out = append(out, copyDecision(d))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ClaimID < out[j].ClaimID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// NoticeSent records the delivery of the notice of a rejection.
func (s *DecisionStore) NoticeSent(_ context.Context, claimID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.decisions[claimID]; ok && d.Notice != nil && d.Notice.SentAt.IsZero() {
		d.Notice.SentAt, d.Notice.Attempts, d.Notice.NextAttemptAt, d.Notice.LastError = at.UTC(), d.Notice.Attempts+1, time.Time{}, ""
	}
	return nil
}

// NoticeFailed counts a failed delivery.
func (s *DecisionStore) NoticeFailed(_ context.Context, claimID string, _, next time.Time, cause string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if d, ok := s.decisions[claimID]; ok && d.Notice != nil && d.Notice.SentAt.IsZero() {
		d.Notice.Attempts, d.Notice.NextAttemptAt, d.Notice.LastError = d.Notice.Attempts+1, next.UTC(), cause
	}
	return nil
}

// copyDecision returns a decision that shares no pointer with d.
func copyDecision(d domain.Decision) domain.Decision {
	if d.Payout != nil {
		p := *d.Payout
		d.Payout = &p
	}
	if d.Notice != nil {
		n := *d.Notice
		d.Notice = &n
	}
	return d
}
