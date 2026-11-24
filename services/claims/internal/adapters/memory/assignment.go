package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// AssignmentStore keeps the locations, the searches and the assignments of
// the claims of a CaseStore (HU-W2.3-01), with the same rules as PostgreSQL:
// they are only added, each takes the next place of its claim, and an
// assignment makes its provider of the ProviderStore busy, all or nothing.
// Everything is lost when the service restarts.
type AssignmentStore struct {
	cases     *CaseStore
	providers *ProviderStore

	mu          sync.Mutex
	locations   map[string][]domain.IncidentLocation
	searches    map[string][]domain.ProviderSearch
	assignments map[string]domain.Assignment
	notices     map[string]ports.ProviderNotice
}

// NewAssignmentStore builds the store over the claims of cases and the
// registry of providers.
func NewAssignmentStore(cases *CaseStore, providers *ProviderStore) *AssignmentStore {
	return &AssignmentStore{cases: cases, providers: providers, locations: map[string][]domain.IncidentLocation{},
		searches: map[string][]domain.ProviderSearch{}, assignments: map[string]domain.Assignment{},
		notices: map[string]ports.ProviderNotice{}}
}

var _ ports.AssignmentStore = (*AssignmentStore)(nil)

// Case returns the claim with its assignment.
func (s *AssignmentStore) Case(ctx context.Context, claimID string) (ports.AssignmentCase, error) {
	c, err := s.cases.Get(ctx, claimID)
	if err != nil {
		return ports.AssignmentCase{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.caseOf(c), nil
}

// Cases returns the claims of the ids that exist with their assignment.
func (s *AssignmentStore) Cases(ctx context.Context, ids []string) (map[string]ports.AssignmentCase, error) {
	out := make(map[string]ports.AssignmentCase, len(ids))
	for _, id := range ids {
		k, err := s.Case(ctx, id)
		if err == nil {
			out[id] = k
		}
	}
	return out, nil
}

// caseOf joins the claim and copies of its last location, its last search
// and its assignment. The caller holds s.mu.
func (s *AssignmentStore) caseOf(c domain.ClaimRecord) ports.AssignmentCase {
	k := ports.AssignmentCase{Claim: c}
	if l := s.locations[c.ID]; len(l) > 0 {
		last := copyLocation(l[len(l)-1])
		k.Location = &last
	}
	if r := s.searches[c.ID]; len(r) > 0 {
		last := copySearch(r[len(r)-1])
		k.LastSearch = &last
	}
	if a, ok := s.assignments[c.ID]; ok {
		a = copyAssignment(a)
		n := s.notices[c.ID]
		k.Assignment, k.Notice = &a, &n
	}
	return k
}

// RecordLocation adds the location of the analyst in its place.
func (s *AssignmentStore) RecordLocation(_ context.Context, claimID string, l domain.IncidentLocation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch _, assigned := s.assignments[claimID]; {
	case assigned:
		return domain.ErrAssignmentExists
	case l.Seq != len(s.locations[claimID])+1:
		return ports.ErrLocationConflict
	}
	s.locations[claimID] = append(s.locations[claimID], copyLocation(l))
	return nil
}

// RecordSearch adds the search in its place.
func (s *AssignmentStore) RecordSearch(_ context.Context, r domain.ProviderSearch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch _, assigned := s.assignments[r.ClaimID]; {
	case assigned:
		return domain.ErrAssignmentExists
	case r.Seq != len(s.searches[r.ClaimID])+1:
		return ports.ErrSearchConflict
	}
	s.searches[r.ClaimID] = append(s.searches[r.ClaimID], copySearch(r))
	return nil
}

// Assign records the assignment, makes the provider busy and leaves the
// notice due.
func (s *AssignmentStore) Assign(ctx context.Context, a domain.Assignment, n ports.ProviderNotice) error {
	c, err := s.cases.Get(ctx, a.ClaimID)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	searches := s.searches[a.ClaimID]
	switch _, assigned := s.assignments[a.ClaimID]; {
	case assigned:
		return domain.ErrAssignmentExists
	case !domain.Assignable(c.Status):
		return domain.ErrClaimNotAssignable
	case len(searches) != a.SearchSeq:
		return domain.ErrSearchOutdated
	case !s.providers.occupy(a.ProviderID, a.ClaimID, a.AssignedBy.Email, a.AssignedAt):
		return domain.ErrProviderUnavailable
	}
	s.assignments[a.ClaimID] = copyAssignment(a)
	n.Status, n.Attempts = ports.NoticePending, 0
	s.notices[a.ClaimID] = n
	return nil
}

// PendingAssignment returns the claims pending assignment, oldest searched
// first.
func (s *AssignmentStore) PendingAssignment(_ context.Context, limit int) ([]string, error) {
	claims := s.cases.snapshot(func(c domain.ClaimRecord) bool { return domain.Assignable(c.Status) })
	s.mu.Lock()
	defer s.mu.Unlock()
	type pending struct {
		id string
		at time.Time
	}
	var found []pending
	for _, c := range claims {
		r := s.searches[c.ID]
		if _, assigned := s.assignments[c.ID]; assigned || len(r) == 0 {
			continue
		}
		last := r[len(r)-1]
		if last.Result.Outcome != domain.SearchExhausted || !s.searchedFromCurrent(c.ID, last) {
			continue
		}
		found = append(found, pending{id: c.ID, at: last.SearchedAt})
	}
	sort.Slice(found, func(i, j int) bool {
		if !found[i].at.Equal(found[j].at) {
			return found[i].at.Before(found[j].at)
		}
		return found[i].id < found[j].id
	})
	out := make([]string, 0, min(len(found), limit))
	for _, p := range found {
		if len(out) == limit {
			break
		}
		out = append(out, p.id)
	}
	return out, nil
}

// searchedFromCurrent tells whether the search measured from the location in
// force: a location of the app does not change, one of the analyst is the
// last recorded. The snapshot of the claims carries no evidence, so it is
// told by the source. The caller holds s.mu.
func (s *AssignmentStore) searchedFromCurrent(claimID string, r domain.ProviderSearch) bool {
	return r.Location.Source == domain.LocationFromApp || r.Location.Seq == len(s.locations[claimID])
}

// ClaimNoticesDue returns the notices due at now and holds them for lease.
func (s *AssignmentStore) ClaimNoticesDue(_ context.Context, now time.Time, lease time.Duration,
	limit int) ([]ports.ProviderNotice, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ports.ProviderNotice
	for _, n := range s.notices {
		if n.Status == ports.NoticePending && !n.NextAttemptAt.After(now) {
			out = append(out, n)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].NextAttemptAt.Equal(out[j].NextAttemptAt) {
			return out[i].NextAttemptAt.Before(out[j].NextAttemptAt)
		}
		return out[i].ClaimID < out[j].ClaimID
	})
	if len(out) > limit {
		out = out[:limit]
	}
	for i, n := range out {
		n.NextAttemptAt = now.Add(lease).UTC()
		s.notices[n.ClaimID] = n
		out[i] = n
	}
	return out, nil
}

// NoticeSent records the delivery of the notice.
func (s *AssignmentStore) NoticeSent(_ context.Context, claimID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.notices[claimID]; ok && n.Status == ports.NoticePending {
		n.Status, n.SentAt, n.Attempts, n.NextAttemptAt, n.LastError = ports.NoticeSent, at.UTC(), n.Attempts+1,
			time.Time{}, ""
		s.notices[claimID] = n
	}
	return nil
}

// NoticeFailed counts a failed delivery.
func (s *AssignmentStore) NoticeFailed(_ context.Context, claimID string, next time.Time, cause string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if n, ok := s.notices[claimID]; ok && n.Status == ports.NoticePending {
		n.Attempts, n.NextAttemptAt, n.LastError = n.Attempts+1, next.UTC(), cause
		s.notices[claimID] = n
	}
	return nil
}

// copyLocation returns a location that shares no pointer with l.
func copyLocation(l domain.IncidentLocation) domain.IncidentLocation {
	if l.AccuracyM != nil {
		a := *l.AccuracyM
		l.AccuracyM = &a
	}
	if l.RecordedBy != nil {
		by := *l.RecordedBy
		l.RecordedBy = &by
	}
	return l
}

// copySearch returns a search that shares no slice nor pointer with r.
func copySearch(r domain.ProviderSearch) domain.ProviderSearch {
	r.Location = copyLocation(r.Location)
	if r.SearchedBy != nil {
		by := *r.SearchedBy
		r.SearchedBy = &by
	}
	r.Result.Attempts = append([]domain.SearchAttempt(nil), r.Result.Attempts...)
	if r.Result.Candidates == nil {
		return r
	}
	candidates := make([]domain.Candidate, len(r.Result.Candidates))
	for i, c := range r.Result.Candidates {
		c.Specialties = append([]domain.Specialty(nil), c.Specialties...)
		if c.UnavailableUntil != nil {
			t := *c.UnavailableUntil
			c.UnavailableUntil = &t
		}
		candidates[i] = c
	}
	r.Result.Candidates = candidates
	return r
}

// copyAssignment returns an assignment that shares no pointer with a.
func copyAssignment(a domain.Assignment) domain.Assignment {
	a.ContactPhone = copyString(a.ContactPhone)
	a.Location = copyLocation(a.Location)
	return a
}
