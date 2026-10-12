package memory

import (
	"context"
	"log/slog"
	"sort"
	"sync"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// ReviewQueueStub is an in-memory ports.ReviewQueue. PENDING: the advisor
// inbox of HU-W1.1-02, which will list, open and decide these requests.
type ReviewQueueStub struct {
	mu       sync.Mutex
	requests map[string]domain.ReviewRequest
}

// NewReviewQueueStub builds an empty ReviewQueueStub.
func NewReviewQueueStub() *ReviewQueueStub {
	return &ReviewQueueStub{requests: map[string]domain.ReviewRequest{}}
}

// Enqueue stores the request once per acceptance.
func (q *ReviewQueueStub) Enqueue(_ context.Context, req domain.ReviewRequest) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, ok := q.requests[req.AcceptanceID]; ok {
		return nil
	}
	q.requests[req.AcceptanceID] = req
	slog.Info("request routed to human decision", "acceptance_id", req.AcceptanceID, "offer_id", req.OfferID, "reason", req.Reason)
	return nil
}

// Pending returns the enqueued requests, oldest first.
func (q *ReviewQueueStub) Pending() []domain.ReviewRequest {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]domain.ReviewRequest, 0, len(q.requests))
	for _, r := range q.requests {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ReceivedAt.Before(out[j].ReceivedAt) })
	return out
}
