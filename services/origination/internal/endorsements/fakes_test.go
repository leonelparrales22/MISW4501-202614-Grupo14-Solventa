package endorsements_test

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

// fakePolicies counts the reads, so a test can tell that a forbidden caller
// read nothing.
type fakePolicies struct {
	rows  map[string]domain.Policy
	reads int
	err   error
}

func (f *fakePolicies) Create(context.Context, domain.Policy) error { return nil }

func (f *fakePolicies) Get(_ context.Context, id string) (domain.Policy, error) {
	f.reads++
	if f.err != nil {
		return domain.Policy{}, f.err
	}
	p, ok := f.rows[id]
	if !ok {
		return domain.Policy{}, ports.ErrPolicyNotFound
	}
	return p, nil
}

func (f *fakePolicies) ListByCustomer(context.Context, string) ([]domain.Policy, error) {
	return nil, nil
}

func (f *fakePolicies) ListLapsed(context.Context, time.Time, time.Time, int) ([]domain.Policy, error) {
	return nil, nil
}

// fakeRequests numbers the requests by year and keeps one open per policy
// and type, as the PostgreSQL store does.
type fakeRequests struct {
	mu       sync.Mutex
	rows     map[string]domain.ChangeRequest
	counters map[int]int
	creates  int
	err      error
	got      domain.ChangeRequestFilter
	gotOwner string
}

func newFakeRequests() *fakeRequests {
	return &fakeRequests{rows: map[string]domain.ChangeRequest{}, counters: map[int]int{}}
}

func (f *fakeRequests) Create(_ context.Context, r domain.ChangeRequest) (domain.ChangeRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.creates++
	if f.err != nil {
		return domain.ChangeRequest{}, f.err
	}
	for _, o := range f.rows {
		if o.PolicyID == r.PolicyID && o.Type == r.Type && o.Open() {
			return domain.ChangeRequest{}, &ports.ChangeRequestOpenError{RequestID: o.ID}
		}
	}
	year := domain.ChangeRequestYear(r.FiledAt)
	f.counters[year]++
	r.ID = domain.FormatChangeRequestID(year, f.counters[year])
	f.rows[r.ID] = r
	return r, nil
}

func (f *fakeRequests) Get(_ context.Context, id string) (domain.ChangeRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return domain.ChangeRequest{}, f.err
	}
	r, ok := f.rows[id]
	if !ok {
		return domain.ChangeRequest{}, ports.ErrChangeRequestNotFound
	}
	return r, nil
}

func (f *fakeRequests) ListOpen(_ context.Context, policyID string) ([]domain.ChangeRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := []domain.ChangeRequest{}
	for _, r := range f.rows {
		if r.PolicyID == policyID && r.Open() {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out, nil
}

func (f *fakeRequests) ListByCustomer(_ context.Context, customerID string, filter domain.ChangeRequestFilter) (domain.ChangeRequestPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got, f.gotOwner = filter, customerID
	if f.err != nil {
		return domain.ChangeRequestPage{}, f.err
	}
	page := domain.ChangeRequestPage{Items: []domain.ChangeRequest{}, Page: filter.Page, PageSize: filter.PageSize}
	for _, r := range f.rows {
		if r.CustomerID == customerID && filter.Matches(r) {
			page.Items = append(page.Items, r)
		}
	}
	page.Total = len(page.Items)
	return page, nil
}

// fakeCancellations is the set of the policies cancelled for non-payment
// (HU-W1.6-02).
type fakeCancellations struct {
	cancelled map[string]bool
	err       error
}

func (f *fakeCancellations) IsCancelled(_ context.Context, policyID string) (bool, error) {
	return f.cancelled[policyID], f.err
}

// fakeOrigins answers the origin of each policy.
type fakeOrigins struct {
	rows map[string]renewal.Origin
	err  error
}

func (f fakeOrigins) Find(_ context.Context, p domain.Policy) (renewal.Origin, error) {
	return f.rows[p.ID], f.err
}
