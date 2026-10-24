package memory

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ReviewQueueStub is an in-memory ports.ReviewRepository: the advisor inbox
// of HU-W1.1-02 with the requests routed to a human decision, the
// append-only record of the advisor decisions and the state of the customer
// notices. PENDING: the PostgreSQL inbox of origination; until then the
// inbox and its decisions survive dependency failures but not a restart.
type ReviewQueueStub struct {
	mu    sync.Mutex
	cases map[string]domain.ReviewCase
	// byAdjustedOffer maps an adjusted offer id to its acceptance id.
	byAdjustedOffer map[string]string
}

var _ ports.ReviewRepository = (*ReviewQueueStub)(nil)

// NewReviewQueueStub builds an empty ReviewQueueStub.
func NewReviewQueueStub() *ReviewQueueStub {
	return &ReviewQueueStub{cases: map[string]domain.ReviewCase{}, byAdjustedOffer: map[string]string{}}
}

// Enqueue stores the request once per acceptance, pending.
func (q *ReviewQueueStub) Enqueue(ctx context.Context, req domain.ReviewRequest) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.cases[req.AcceptanceID]; ok {
		return nil
	}
	q.cases[req.AcceptanceID] = cloneCase(domain.ReviewCase{Request: req, Status: domain.ReviewPending})
	slog.InfoContext(ctx, "request routed to human decision", "acceptance_id", req.AcceptanceID, "offer_id", req.OfferID, "reason", req.Reason)
	return nil
}

// Pending returns the pending requests, oldest first.
func (q *ReviewQueueStub) Pending() []domain.ReviewRequest {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]domain.ReviewRequest, 0, len(q.cases))
	for _, c := range q.cases {
		if c.Status == domain.ReviewPending {
			out = append(out, c.Request)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ReceivedAt.Before(out[j].ReceivedAt) })
	return out
}

// List returns the cases that match the filter ordered by arrival (oldest
// first unless Descending) and the counts per status for the filter reason.
// An empty status lists every status.
func (q *ReviewQueueStub) List(_ context.Context, f domain.ReviewFilter) ([]domain.ReviewCase, domain.ReviewCounts, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var counts domain.ReviewCounts
	out := []domain.ReviewCase{}
	for _, c := range q.cases {
		if f.Reason != "" && c.Request.Reason != f.Reason {
			continue
		}
		if c.Status == domain.ReviewPending {
			counts.Pending++
		} else {
			counts.Decided++
		}
		if f.Status == "" || c.Status == f.Status {
			out = append(out, cloneCase(c))
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Request, out[j].Request
		if !a.ReceivedAt.Equal(b.ReceivedAt) {
			return a.ReceivedAt.Before(b.ReceivedAt) != f.Descending
		}
		return a.AcceptanceID < b.AcceptanceID
	})
	return out, counts, nil
}

// Get returns the case of the acceptance.
func (q *ReviewQueueStub) Get(_ context.Context, acceptanceID string) (domain.ReviewCase, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	c, ok := q.cases[acceptanceID]
	if !ok {
		return domain.ReviewCase{}, ports.ErrReviewRequestNotFound
	}
	return cloneCase(c), nil
}

// RecordDecision appends the decision and marks the case decided under the
// same lock, only if the case is still pending (compare-and-set).
func (q *ReviewQueueStub) RecordDecision(_ context.Context, d domain.HumanDecision) (domain.ReviewCase, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	c, ok := q.cases[d.AcceptanceID]
	if !ok {
		return domain.ReviewCase{}, ports.ErrReviewRequestNotFound
	}
	if c.Status != domain.ReviewPending {
		return domain.ReviewCase{}, ports.ErrReviewAlreadyDecided
	}
	c.Status = domain.ReviewDecided
	c.Decision = cloneDecision(&d)
	q.cases[d.AcceptanceID] = c
	if d.AdjustedOffer != nil {
		q.byAdjustedOffer[d.AdjustedOffer.ID] = d.AcceptanceID
	}
	return cloneCase(c), nil
}

// DecisionForAdjustedOffer returns the decision that created the offer.
func (q *ReviewQueueStub) DecisionForAdjustedOffer(_ context.Context, offerID string) (domain.HumanDecision, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	id, ok := q.byAdjustedOffer[offerID]
	if !ok {
		return domain.HumanDecision{}, false, nil
	}
	return *cloneDecision(q.cases[id].Decision), true, nil
}

// ListDueNotifications returns the cases whose notice is pending and due at
// now, the earliest due first.
func (q *ReviewQueueStub) ListDueNotifications(_ context.Context, now time.Time, limit int) ([]domain.ReviewCase, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	var due []domain.ReviewCase
	for _, c := range q.cases {
		n := c.Notification
		if n != nil && n.Status == domain.NotificationPending && !n.NextAttemptAt.After(now) {
			due = append(due, cloneCase(c))
		}
	}
	sort.Slice(due, func(i, j int) bool {
		a, b := due[i].Notification.NextAttemptAt, due[j].Notification.NextAttemptAt
		if !a.Equal(b) {
			return a.Before(b)
		}
		return due[i].Request.AcceptanceID < due[j].Request.AcceptanceID
	})
	if limit > 0 && len(due) > limit {
		due = due[:limit]
	}
	return due, nil
}

// UpdateNotification replaces the notice of the case.
func (q *ReviewQueueStub) UpdateNotification(_ context.Context, acceptanceID string, n domain.ReviewNotification) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	c, ok := q.cases[acceptanceID]
	if !ok {
		return ports.ErrReviewRequestNotFound
	}
	c.Notification = &n
	q.cases[acceptanceID] = c
	return nil
}

// cloneCase copies the case so callers never share its decision, its notice
// or the coverages of its offer snapshot.
func cloneCase(c domain.ReviewCase) domain.ReviewCase {
	c.Request.Terms.Coverages = append([]domain.Coverage(nil), c.Request.Terms.Coverages...)
	c.Decision = cloneDecision(c.Decision)
	if c.Notification != nil {
		n := *c.Notification
		c.Notification = &n
	}
	return c
}

func cloneDecision(d *domain.HumanDecision) *domain.HumanDecision {
	if d == nil {
		return nil
	}
	out := *d
	if d.PremiumAdjustmentPct != nil {
		p := *d.PremiumAdjustmentPct
		out.PremiumAdjustmentPct = &p
	}
	out.AdjustedOffer = cloneAdjustedOffer(d.AdjustedOffer)
	return &out
}

func cloneAdjustedOffer(o *domain.AdjustedOffer) *domain.AdjustedOffer {
	if o == nil {
		return nil
	}
	out := *o
	return &out
}
