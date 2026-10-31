package memory

import (
	"context"
	"sort"
	"sync"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// TriageStore keeps the calculations of the level of the claims of a
// CaseStore (HU-W2.2-01), with the same rules as PostgreSQL: calculations are
// only added, and each one takes the next place of its claim. Everything is
// lost when the service restarts.
type TriageStore struct {
	cases *CaseStore

	mu sync.Mutex
	// records holds the calculations of each claim, oldest first.
	records map[string][]domain.TriageRecord
}

// NewTriageStore builds the store over the claims of cases.
func NewTriageStore(cases *CaseStore) *TriageStore {
	return &TriageStore{cases: cases, records: map[string][]domain.TriageRecord{}}
}

var _ ports.TriageStore = (*TriageStore)(nil)

// Pending returns the claims to assess, oldest filed first.
func (s *TriageStore) Pending(_ context.Context, statuses []domain.Status, policyVersion string,
	limit int) ([]ports.TriageCandidate, error) {
	wanted := map[domain.Status]bool{}
	for _, st := range statuses {
		wanted[st] = true
	}
	claims := s.cases.snapshot(func(c domain.ClaimRecord) bool { return wanted[c.Status] })
	sort.Slice(claims, func(i, j int) bool {
		if !claims[i].FiledAt.Equal(claims[j].FiledAt) {
			return claims[i].FiledAt.Before(claims[j].FiledAt)
		}
		return claims[i].ID < claims[j].ID
	})

	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ports.TriageCandidate
	for _, c := range claims {
		last := s.last(c.ID)
		if last != nil && !changed(c, last, policyVersion) {
			continue
		}
		out = append(out, ports.TriageCandidate{Claim: c, Last: last})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// changed tells whether the claim has another amount, claim type or policy
// version than its last calculation.
func changed(c domain.ClaimRecord, last *domain.TriageRecord, policyVersion string) bool {
	in := last.Assessment.Input
	switch {
	case in.PolicyVersion != policyVersion, in.IncidentType != c.IncidentType:
		return true
	case in.EstimatedAmountMinor == nil || c.EstimatedAmountMinor == nil:
		return in.EstimatedAmountMinor != c.EstimatedAmountMinor
	}
	return *in.EstimatedAmountMinor != *c.EstimatedAmountMinor
}

// Candidate returns the claim and its last calculation.
func (s *TriageStore) Candidate(_ context.Context, claimID string) (ports.TriageCandidate, error) {
	found := s.cases.snapshot(func(c domain.ClaimRecord) bool { return c.ID == claimID })
	if len(found) == 0 {
		return ports.TriageCandidate{}, ports.ErrClaimNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return ports.TriageCandidate{Claim: found[0], Last: s.last(claimID)}, nil
}

// Record adds the calculation in its place, or returns ErrTriageConflict.
func (s *TriageStore) Record(_ context.Context, r domain.TriageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Seq != len(s.records[r.ClaimID])+1 {
		return ports.ErrTriageConflict
	}
	s.records[r.ClaimID] = append(s.records[r.ClaimID], copyTriage(r))
	return nil
}

// History returns the calculations of the claim, newest first.
func (s *TriageStore) History(_ context.Context, claimID string) ([]domain.TriageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records := s.records[claimID]
	out := make([]domain.TriageRecord, 0, len(records))
	for i := len(records) - 1; i >= 0; i-- {
		out = append(out, copyTriage(records[i]))
	}
	return out, nil
}

// Current returns the status and the last calculation of the claims that
// exist.
func (s *TriageStore) Current(_ context.Context, ids []string) (map[string]ports.TriageCandidate, error) {
	wanted := map[string]bool{}
	for _, id := range ids {
		wanted[id] = true
	}
	claims := s.cases.snapshot(func(c domain.ClaimRecord) bool { return wanted[c.ID] })

	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]ports.TriageCandidate, len(claims))
	for _, c := range claims {
		out[c.ID] = ports.TriageCandidate{
			Claim: domain.ClaimRecord{Claim: domain.Claim{ID: c.ID, Status: c.Status}},
			Last:  s.last(c.ID),
		}
	}
	return out, nil
}

// last returns a copy of the last calculation of the claim, or nil. The
// caller holds the lock.
func (s *TriageStore) last(claimID string) *domain.TriageRecord {
	records := s.records[claimID]
	if len(records) == 0 {
		return nil
	}
	r := copyTriage(records[len(records)-1])
	return &r
}

// snapshot returns copies of the claims that match, without supports nor
// evidence.
func (s *CaseStore) snapshot(match func(domain.ClaimRecord) bool) []domain.ClaimRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.ClaimRecord
	for _, c := range s.byID {
		if match(c) {
			c = copyRecord(c)
			c.Supports, c.Evidence = nil, nil
			out = append(out, c)
		}
	}
	return out
}

// copyTriage returns a calculation that shares no slice nor pointer with r.
func copyTriage(r domain.TriageRecord) domain.TriageRecord {
	r.Causes = append([]domain.TriageCause(nil), r.Causes...)
	if r.PreviousLevel != nil {
		p := *r.PreviousLevel
		r.PreviousLevel = &p
	}
	if amount := r.Assessment.Input.EstimatedAmountMinor; amount != nil {
		a := *amount
		r.Assessment.Input.EstimatedAmountMinor = &a
	}
	return r
}
