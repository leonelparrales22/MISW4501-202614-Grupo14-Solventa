package underwriting_test

import (
	"context"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

var t0 = time.Date(2026, 10, 16, 15, 20, 0, 0, time.UTC)

// testClock is a clock the test moves by hand.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func as(role security.Role, subject string) context.Context {
	return security.WithClaims(context.Background(), security.Claims{
		Subject: subject, Email: subject + "@example.com", Roles: []security.Role{role},
	})
}

var advisor = as(security.RoleAdvisor, "adv-1")

// reviews wraps the in-memory inbox to inject failures.
type reviews struct {
	*memory.ReviewQueueStub
	getErr, recordErr, listDueErr, adjustedErr error
	// updateErrs fail the next UpdateNotification calls, one per element.
	updateErrs []error
	// lostClaim makes RecordDecision report that another advisor won.
	lostClaim bool
}

func (r *reviews) Get(ctx context.Context, id string) (domain.ReviewCase, error) {
	if r.getErr != nil {
		return domain.ReviewCase{}, r.getErr
	}
	return r.ReviewQueueStub.Get(ctx, id)
}

func (r *reviews) RecordDecision(ctx context.Context, d domain.HumanDecision) (domain.ReviewCase, error) {
	if r.recordErr != nil {
		return domain.ReviewCase{}, r.recordErr
	}
	if r.lostClaim {
		other := d
		other.ID, other.Advisor = "dec-other", domain.Advisor{ID: "adv-2", Email: "adv-2@example.com"}
		if _, err := r.ReviewQueueStub.RecordDecision(ctx, other); err != nil {
			return domain.ReviewCase{}, err
		}
	}
	return r.ReviewQueueStub.RecordDecision(ctx, d)
}

func (r *reviews) ListDueNotifications(ctx context.Context, now time.Time, limit int) ([]domain.ReviewCase, error) {
	if r.listDueErr != nil {
		return nil, r.listDueErr
	}
	return r.ReviewQueueStub.ListDueNotifications(ctx, now, limit)
}

func (r *reviews) UpdateNotification(ctx context.Context, id string, n domain.ReviewNotification) error {
	if len(r.updateErrs) > 0 {
		err := r.updateErrs[0]
		r.updateErrs = r.updateErrs[1:]
		if err != nil {
			return err
		}
	}
	return r.ReviewQueueStub.UpdateNotification(ctx, id, n)
}

func (r *reviews) DecisionForAdjustedOffer(ctx context.Context, offerID string) (domain.HumanDecision, bool, error) {
	if r.adjustedErr != nil {
		return domain.HumanDecision{}, false, r.adjustedErr
	}
	return r.ReviewQueueStub.DecisionForAdjustedOffer(ctx, offerID)
}

// acceptances wraps the in-memory store to inject failures.
type acceptances struct {
	*memory.AcceptanceStore
	getErr error
	// updateErrs fail the next Update calls, one per element.
	updateErrs []error
}

func (a *acceptances) Get(ctx context.Context, id string) (domain.Acceptance, error) {
	if a.getErr != nil {
		return domain.Acceptance{}, a.getErr
	}
	return a.AcceptanceStore.Get(ctx, id)
}

func (a *acceptances) Update(ctx context.Context, acc domain.Acceptance) error {
	if len(a.updateErrs) > 0 {
		err := a.updateErrs[0]
		a.updateErrs = a.updateErrs[1:]
		if err != nil {
			return err
		}
	}
	return a.AcceptanceStore.Update(ctx, acc)
}

// offers wraps the in-memory offers to inject failures.
type offers struct {
	*memory.OfferStub
	getErr, createErr error
}

func (o *offers) Get(ctx context.Context, id string) (domain.Offer, error) {
	if o.getErr != nil {
		return domain.Offer{}, o.getErr
	}
	return o.OfferStub.Get(ctx, id)
}

func (o *offers) Create(ctx context.Context, offer domain.Offer) error {
	if o.createErr != nil {
		return o.createErr
	}
	return o.OfferStub.Create(ctx, offer)
}

// recordingNotifier records the notices and fails while errs has elements.
type recordingNotifier struct {
	mu   sync.Mutex
	sent []domain.ReviewOutcomeNotice
	errs []error
}

func (n *recordingNotifier) NotifyReviewOutcome(_ context.Context, notice domain.ReviewOutcomeNotice) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if len(n.errs) > 0 {
		err := n.errs[0]
		n.errs = n.errs[1:]
		return err
	}
	n.sent = append(n.sent, notice)
	return nil
}

func (n *recordingNotifier) notices() []domain.ReviewOutcomeNotice {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]domain.ReviewOutcomeNotice(nil), n.sent...)
}

// counter counts the calls of a wake-up callback.
type counter struct {
	mu sync.Mutex
	n  int
}

func (c *counter) inc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func (c *counter) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}
