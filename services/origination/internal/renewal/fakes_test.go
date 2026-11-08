package renewal_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type fakePolicies struct {
	mu       sync.Mutex
	rows     map[string]domain.Policy
	renewals *fakeRenewals
}

func newFakePolicies() *fakePolicies { return &fakePolicies{rows: map[string]domain.Policy{}} }

func (f *fakePolicies) Create(_ context.Context, p domain.Policy) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, found := f.rows[p.ID]; !found {
		f.rows[p.ID] = p
	}
	return nil
}

func (f *fakePolicies) Get(_ context.Context, id string) (domain.Policy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, found := f.rows[id]
	if !found {
		return domain.Policy{}, ports.ErrPolicyNotFound
	}
	return p, nil
}

func (f *fakePolicies) ListByCustomer(_ context.Context, customerID string) ([]domain.Policy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Policy
	for _, p := range f.rows {
		if p.CustomerID == customerID {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// lapsedWith is what ListLapsed returns: the policies with no renewal that
// ended inside the range, as the SQL does.
func (f *fakePolicies) ListLapsed(_ context.Context, since, now time.Time, limit int) ([]domain.Policy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Policy
	for _, p := range f.rows {
		if !p.CoverageEnd.Before(since) && !p.CoverageEnd.After(now) && f.renewals != nil && !f.renewals.has(p) {
			out = append(out, p)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

type fakeRenewals struct {
	mu   sync.Mutex
	rows map[string]domain.Renewal
}

func newFakeRenewals() *fakeRenewals { return &fakeRenewals{rows: map[string]domain.Renewal{}} }

func (f *fakeRenewals) has(p domain.Policy) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, found := f.rows[domain.RenewalID(p.ID, p.CoverageEnd)]
	return found
}

func (f *fakeRenewals) Create(_ context.Context, r domain.Renewal) (domain.Renewal, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, found := f.rows[r.ID]; found {
		return existing, false, nil
	}
	f.rows[r.ID] = r
	return r, true, nil
}

func (f *fakeRenewals) Get(_ context.Context, policyID string, periodEnd time.Time) (domain.Renewal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, found := f.rows[domain.RenewalID(policyID, periodEnd)]
	if !found {
		return domain.Renewal{}, ports.ErrRenewalNotFound
	}
	return r, nil
}

func (f *fakeRenewals) ListByPolicies(_ context.Context, ids []string) ([]domain.Renewal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Renewal
	for _, r := range f.rows {
		for _, id := range ids {
			if r.PolicyID == id {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

func (f *fakeRenewals) close(id string, change func(*domain.Renewal)) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, found := f.rows[id]
	if !found || r.Status != domain.RenewalAccepted {
		return false
	}
	change(&r)
	f.rows[id] = r
	return true
}

func (f *fakeRenewals) MarkRenewed(_ context.Context, id, newPolicyID string, at time.Time) (bool, error) {
	return f.close(id, func(r *domain.Renewal) { r.Status, r.NewPolicyID, r.ClosedAt = domain.RenewalRenewed, newPolicyID, at }), nil
}

func (f *fakeRenewals) MarkNotRenewed(_ context.Context, id string, reason domain.NotRenewedReason, at time.Time) (bool, error) {
	return f.close(id, func(r *domain.Renewal) { r.Status, r.Reason, r.ClosedAt = domain.RenewalNotRenewed, reason, at }), nil
}

func (f *fakeRenewals) ListAccepted(context.Context, int) ([]domain.Renewal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Renewal
	for _, r := range f.rows {
		if r.Status == domain.RenewalAccepted {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeRenewals) ListUnnotified(context.Context, int) ([]domain.Renewal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Renewal
	for _, r := range f.rows {
		if r.Status == domain.RenewalNotRenewed && r.NotifiedAt.IsZero() && !r.NotificationFailed {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeRenewals) MarkNotified(_ context.Context, id string, at time.Time, failed bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.rows[id]
	if failed {
		r.NotificationFailed = true
	} else {
		r.NotifiedAt = at
	}
	f.rows[id] = r
	return nil
}

// fakeCharges implements only what the renewal uses of the repository.
type fakeCharges struct {
	ports.ChargeRepository
	mu       sync.Mutex
	rows     map[string]domain.Charge
	attached []string
}

func newFakeCharges() *fakeCharges { return &fakeCharges{rows: map[string]domain.Charge{}} }

func (f *fakeCharges) CreatePending(_ context.Context, c domain.Charge) (domain.Charge, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, found := f.rows[c.AcceptanceID]; found {
		return existing, false, nil
	}
	f.rows[c.AcceptanceID] = c
	return c, true, nil
}

func (f *fakeCharges) Get(_ context.Context, id string) (domain.Charge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, found := f.rows[id]
	if !found {
		return domain.Charge{}, ports.ErrChargeNotFound
	}
	return c, nil
}

func (f *fakeCharges) AttachPaymentMethod(_ context.Context, id string, m domain.PaymentMethod, now time.Time) (domain.Charge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, found := f.rows[id]
	if !found {
		return domain.Charge{}, ports.ErrChargeNotFound
	}
	c.PaymentMethod, c.NextAttemptAt = m, now
	f.rows[id] = c
	f.attached = append(f.attached, id)
	return c, nil
}

func (f *fakeCharges) set(id string, change func(*domain.Charge)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.rows[id]
	change(&c)
	f.rows[id] = c
}

type fakeCustomers struct {
	customer domain.Customer
	err      error
}

func (f fakeCustomers) GetBySubject(context.Context, string) (domain.Customer, error) {
	return f.customer, f.err
}

type fakeProfiles struct{ err error }

func (f fakeProfiles) Resolve(context.Context, string, bool) (domain.ProfileResolution, error) {
	return domain.ProfileResolution{Profile: domain.RiskProfile{Score: 0.4}}, f.err
}

type fakeRater struct {
	premium float64
	err     error
	got     ports.RatingInput
}

func (f *fakeRater) Rate(_ context.Context, in ports.RatingInput) (domain.Rating, error) {
	f.got = in
	return domain.Rating{Premium: f.premium, Currency: "COP", Version: "test"}, f.err
}

type fakeClaims struct {
	n   int
	err error
}

func (f fakeClaims) ClaimsInLastYear(context.Context, string) (int, error) { return f.n, f.err }

type fakeNotifier struct {
	mu      sync.Mutex
	err     error
	notices []domain.RenewalNotice
}

func (f *fakeNotifier) Notify(_ context.Context, n domain.RenewalNotice) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.notices = append(f.notices, n)
	return nil
}

var errBoom = errors.New("boom")

// Fakes of the assisted renewal (HU-W1.5-03).

type fakeOffers struct {
	rows map[string]domain.PersonalizedOffer
	err  error
}

func (f fakeOffers) Get(_ context.Context, id string) (domain.PersonalizedOffer, error) {
	if f.err != nil {
		return domain.PersonalizedOffer{}, f.err
	}
	o, found := f.rows[id]
	if !found {
		return domain.PersonalizedOffer{}, ports.ErrOfferNotFound
	}
	return o, nil
}

type fakeCustomersByDocument struct {
	rows map[string]domain.Customer
	err  error
}

func (f fakeCustomersByDocument) GetByDocument(_ context.Context, doc string) (domain.Customer, error) {
	if f.err != nil {
		return domain.Customer{}, f.err
	}
	c, found := f.rows[doc]
	if !found {
		return domain.Customer{}, ports.ErrCustomerNotFound
	}
	return c, nil
}

type fakeRequotes struct {
	mu   sync.Mutex
	rows map[string]domain.RenewalRequote
	err  error
}

func newFakeRequotes() *fakeRequotes { return &fakeRequotes{rows: map[string]domain.RenewalRequote{}} }

func (f *fakeRequotes) Create(_ context.Context, r domain.RenewalRequote) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.rows[r.ID] = r
	return nil
}

func (f *fakeRequotes) Get(_ context.Context, id string) (domain.RenewalRequote, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	r, found := f.rows[id]
	if !found {
		return domain.RenewalRequote{}, ports.ErrRenewalRequoteNotFound
	}
	return r, nil
}

func (f *fakeRequotes) Latest(_ context.Context, policyID string, periodEnd time.Time) (domain.RenewalRequote, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.RenewalRequote
	for _, r := range f.rows {
		if r.PolicyID == policyID && r.PeriodEnd.Equal(periodEnd) {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return domain.RenewalRequote{}, ports.ErrRenewalRequoteNotFound
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID > out[j].ID
	})
	return out[0], nil
}

// fakeAssisted records the advisor's formalizations next to the renewals of
// the rig, as the store does in one transaction.
type fakeAssisted struct {
	mu       sync.Mutex
	renewals *fakeRenewals
	rows     map[string]domain.RenewalFormalization
}

func (f *fakeAssisted) CreateAssisted(ctx context.Context, r domain.Renewal, fz domain.RenewalFormalization) (domain.Renewal,
	*domain.RenewalFormalization, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored, created, err := f.renewals.Create(ctx, r)
	if err != nil {
		return domain.Renewal{}, nil, false, err
	}
	if !created {
		if existing, found := f.rows[stored.ID]; found {
			return stored, &existing, false, nil
		}
		return stored, nil, false, nil
	}
	f.rows[r.ID] = fz
	return stored, &fz, true, nil
}

func (f *fakeAssisted) GetFormalization(_ context.Context, renewalID string) (domain.RenewalFormalization, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fz, found := f.rows[renewalID]
	if !found {
		return domain.RenewalFormalization{}, ports.ErrRenewalFormalizationNotFound
	}
	return fz, nil
}

type fakeFreshProfiles struct {
	mu    sync.Mutex
	res   domain.ProfileResolution
	err   error
	calls int
	got   []string
}

func (f *fakeFreshProfiles) ResolveFresh(_ context.Context, customerID string, consent bool) (domain.ProfileResolution, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.got = append(f.got, customerID)
	if !consent {
		return domain.ProfileResolution{}, errors.New("no consent")
	}
	return f.res, f.err
}

type fakeConsents struct {
	mu     sync.Mutex
	status domain.ConsentStatus
	err    error
	calls  int
	got    []string
}

func (f *fakeConsents) Verify(_ context.Context, customerID string) (domain.ConsentStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.got = append(f.got, customerID)
	return f.status, f.err
}

type fakeSender struct {
	mu      sync.Mutex
	err     error
	notices []domain.ConsentCodeNotice
}

func (f *fakeSender) SendConsentCode(_ context.Context, n domain.ConsentCodeNotice) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.notices = append(f.notices, n)
	return nil
}

func (f *fakeSender) last() domain.ConsentCodeNotice {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.notices[len(f.notices)-1]
}
