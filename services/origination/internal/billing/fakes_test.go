package billing_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// memRepo is an in-memory ports.ChargeRepository with the same conditional
// semantics as the PostgreSQL one (claims are compare-and-swap).
type memRepo struct {
	mu   sync.Mutex
	rows map[string]domain.Charge

	createErr, getErr, listErr, writeErr, claimErr error
}

var _ ports.ChargeRepository = (*memRepo)(nil)

func newMemRepo() *memRepo { return &memRepo{rows: map[string]domain.Charge{}} }

func (r *memRepo) CreatePending(_ context.Context, c domain.Charge) (domain.Charge, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.createErr != nil {
		return domain.Charge{}, false, r.createErr
	}
	if existing, ok := r.rows[c.AcceptanceID]; ok {
		return existing, false, nil
	}
	r.rows[c.AcceptanceID] = c
	return c, true, nil
}

func (r *memRepo) Get(_ context.Context, id string) (domain.Charge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.getErr != nil {
		return domain.Charge{}, r.getErr
	}
	c, ok := r.rows[id]
	if !ok {
		return domain.Charge{}, ports.ErrChargeNotFound
	}
	return c, nil
}

func (r *memRepo) put(c domain.Charge) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows[c.AcceptanceID] = c
}

func (r *memRepo) get(id string) domain.Charge {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rows[id]
}

func chargeDue(c domain.Charge, now time.Time) bool {
	return (c.Status == domain.ChargePending && !c.NextAttemptAt.After(now)) ||
		(c.Status == domain.ChargeInProgress && c.ClaimedUntil.Before(now))
}

func activationDue(c domain.Charge, now time.Time) bool {
	return c.Status == domain.ChargeConfirmed && c.ActivationPublishedAt.IsZero() &&
		!c.ActivationNextAttemptAt.After(now) && (c.ClaimedUntil.IsZero() || c.ClaimedUntil.Before(now))
}

func (r *memRepo) ids(match func(domain.Charge) bool, limit int) []string {
	var out []string
	for id, c := range r.rows {
		if match(c) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (r *memRepo) ListDue(_ context.Context, now time.Time, limit int) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.ids(func(c domain.Charge) bool { return chargeDue(c, now) }, limit), nil
}

func (r *memRepo) ListActivationsDue(_ context.Context, now time.Time, limit int) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	return r.ids(func(c domain.Charge) bool { return activationDue(c, now) }, limit), nil
}

func (r *memRepo) ListExceptions(_ context.Context, limit int) ([]domain.Charge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.listErr != nil {
		return nil, r.listErr
	}
	var out []domain.Charge
	for _, id := range r.ids(func(c domain.Charge) bool { return c.Status == domain.ChargeFailed }, limit) {
		out = append(out, r.rows[id])
	}
	return out, nil
}

func (r *memRepo) Claim(_ context.Context, id string, now time.Time, lease time.Duration) (domain.Charge, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claimErr != nil {
		return domain.Charge{}, false, r.claimErr
	}
	c, ok := r.rows[id]
	if !ok || !chargeDue(c, now) {
		return domain.Charge{}, false, nil
	}
	c.Status, c.ClaimedUntil, c.LastAttemptAt = domain.ChargeInProgress, now.Add(lease), now
	r.rows[id] = c
	return c, true, nil
}

func (r *memRepo) held(c domain.Charge, status domain.ChargeStatus) (domain.Charge, error) {
	if r.writeErr != nil {
		return domain.Charge{}, r.writeErr
	}
	row, ok := r.rows[c.AcceptanceID]
	if !ok || row.Status != status || !row.ClaimedUntil.Equal(c.ClaimedUntil) {
		return domain.Charge{}, idempotency.ErrClaimLost
	}
	return row, nil
}

func (r *memRepo) Confirm(_ context.Context, c domain.Charge, now time.Time, out idempotency.Outcome) (domain.Charge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, err := r.held(c, domain.ChargeInProgress)
	if err != nil {
		return domain.Charge{}, err
	}
	row = row.Confirmed(now, out.Reference)
	r.rows[row.AcceptanceID] = row
	return row, nil
}

func (r *memRepo) Reschedule(_ context.Context, c domain.Charge, now, next time.Time, cause string) (domain.Charge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, err := r.held(c, domain.ChargeInProgress)
	if err != nil {
		return domain.Charge{}, err
	}
	row.Status, row.Attempts, row.NextAttemptAt, row.ClaimedUntil = domain.ChargePending, row.Attempts+1, next, time.Time{}
	row.LastFailureAt, row.LastError = now, cause
	r.rows[row.AcceptanceID] = row
	return row, nil
}

func (r *memRepo) Fail(_ context.Context, c domain.Charge, now time.Time, reason idempotency.FailureReason, cause string) (domain.Charge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, err := r.held(c, domain.ChargeInProgress)
	if err != nil {
		return domain.Charge{}, err
	}
	row.Status, row.Attempts, row.NextAttemptAt, row.ClaimedUntil = domain.ChargeFailed, row.Attempts+1, time.Time{}, time.Time{}
	row.FailureReason, row.LastFailureAt, row.LastError = domain.ChargeFailureReasonFor(reason), now, cause
	r.rows[row.AcceptanceID] = row
	return row, nil
}

func (r *memRepo) Resume(_ context.Context, id string, res domain.ChargeResumption) (domain.Charge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writeErr != nil {
		return domain.Charge{}, r.writeErr
	}
	row, ok := r.rows[id]
	if !ok {
		return domain.Charge{}, ports.ErrChargeNotFound
	}
	if row.Status != domain.ChargeFailed {
		return domain.Charge{}, ports.ErrChargeNotResumable
	}
	row.Status, row.Attempts, row.FailureReason = domain.ChargePending, 0, ""
	// Sin medio de pago vuelve a esperarlo, no a cobrarse (HU-W1.3-02).
	row.NextAttemptAt = res.At
	if !row.PaymentMethod.Captured() {
		row.NextAttemptAt = time.Time{}
	}
	row.WindowStartedAt, row.WindowEndsAt = res.At, res.WindowEndsAt
	row.ResumeCount++
	row.LastResumedBy, row.LastResumedAt, row.ResumeNote = res.By, res.At, res.Note
	r.rows[id] = row
	return row, nil
}

func (r *memRepo) ClaimActivation(_ context.Context, id string, now time.Time, lease time.Duration) (domain.Charge, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.rows[id]
	if !ok || !activationDue(c, now) {
		return domain.Charge{}, false, nil
	}
	c.ClaimedUntil = now.Add(lease)
	r.rows[id] = c
	return c, true, nil
}

func (r *memRepo) MarkActivationPublished(_ context.Context, c domain.Charge, now time.Time) (domain.Charge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, err := r.held(c, domain.ChargeConfirmed)
	if err != nil {
		return domain.Charge{}, err
	}
	row.ActivationPublishedAt, row.ActivationNextAttemptAt, row.ClaimedUntil = now, time.Time{}, time.Time{}
	r.rows[row.AcceptanceID] = row
	return row, nil
}

func (r *memRepo) RescheduleActivation(_ context.Context, c domain.Charge, _, next time.Time, cause string) (domain.Charge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, err := r.held(c, domain.ChargeConfirmed)
	if err != nil {
		return domain.Charge{}, err
	}
	row.ActivationAttempts++
	row.ActivationNextAttemptAt, row.ActivationLastError, row.ClaimedUntil = next, cause, time.Time{}
	r.rows[row.AcceptanceID] = row
	return row, nil
}

// gateway is a controllable ports.PaymentGateway that records the snapshot
// of every charge it receives.
type gateway struct {
	mu    sync.Mutex
	errs  []error // one per call; when exhausted, nextErr
	next  error
	calls []domain.Charge
	// seen is checked when the gateway is called: the charge must already be
	// persisted (CA-1).
	repo     *memRepo
	unstored int
}

func (g *gateway) Charge(_ context.Context, c domain.Charge) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.repo != nil {
		if stored := g.repo.get(c.AcceptanceID); stored.Status != domain.ChargeInProgress {
			g.unstored++
		}
	}
	g.calls = append(g.calls, c)
	if len(g.errs) > 0 {
		err := g.errs[0]
		g.errs = g.errs[1:]
		return "ch-" + c.AcceptanceID, err
	}
	return "ch-" + c.AcceptanceID, g.next
}

func (g *gateway) set(next error, first ...error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next, g.errs = next, first
}

func (g *gateway) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.calls)
}

// handler is a controllable ports.CoverageActivationHandler.
type handler struct {
	mu     sync.Mutex
	events []domain.CoverageActivated
	fail   int // fail the first N deliveries
}

func (h *handler) Receive(_ context.Context, ev domain.CoverageActivated) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, ev)
	if h.fail > 0 {
		h.fail--
		return errors.New("consumer unavailable")
	}
	return nil
}

func (h *handler) received() []domain.CoverageActivated {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]domain.CoverageActivated(nil), h.events...)
}

// clock is a settable ports.Clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

func (c *clock) advance(d time.Duration) { c.set(c.Now().Add(d)) }

func (r *memRepo) AttachPaymentMethod(_ context.Context, id string,
	m domain.PaymentMethod, now time.Time) (domain.Charge, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writeErr != nil {
		return domain.Charge{}, r.writeErr
	}
	row, ok := r.rows[id]
	if !ok {
		return domain.Charge{}, ports.ErrChargeNotFound
	}
	if !row.TakesPaymentMethod() {
		return domain.Charge{}, ports.ErrChargeNotPayable
	}
	row = row.WithPaymentMethod(m, now)
	r.rows[id] = row
	return row, nil
}

func (r *memRepo) ExpireWithoutPaymentMethod(_ context.Context, now time.Time) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var expired []string
	for id, row := range r.rows {
		if row.Status != domain.ChargePending || !row.NextAttemptAt.IsZero() ||
			row.WindowEndsAt.IsZero() || row.WindowEndsAt.After(now) {
			continue
		}
		row.Status, row.FailureReason, row.LastFailureAt = domain.ChargeFailed, domain.ChargePaymentMethodMissing, now
		r.rows[id] = row
		expired = append(expired, id)
	}
	sort.Strings(expired)
	return expired, nil
}
