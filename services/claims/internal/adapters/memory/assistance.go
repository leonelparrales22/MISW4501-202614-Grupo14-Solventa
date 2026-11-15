package memory

import (
	"context"
	"sync"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// AssistanceStore keeps the requests for on-site assistance without a
// database (HU-M2.3-01), with the same rules as PostgreSQL: one request per
// subject and key, its own numbering, and what was stored is never changed.
// Everything is lost when the service restarts.
type AssistanceStore struct {
	mu     sync.Mutex
	byID   map[string]domain.AssistanceRequest
	byKey  map[assistanceKey]string
	number int64
}

type assistanceKey struct{ subject, key string }

// NewAssistanceStore builds an empty store.
func NewAssistanceStore() *AssistanceStore {
	return &AssistanceStore{byID: map[string]domain.AssistanceRequest{}, byKey: map[assistanceKey]string{}}
}

var _ ports.AssistanceStore = (*AssistanceStore)(nil)

// Create stores the request with the next number, or answers the one the
// subject already sent with the key.
func (s *AssistanceStore) Create(_ context.Context, r domain.AssistanceRequest) (domain.AssistanceRequest, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	k := assistanceKey{r.Subject, r.Key}
	if id, ok := s.byKey[k]; ok {
		existing := s.byID[id]
		if existing.Fingerprint != r.Fingerprint {
			return domain.AssistanceRequest{}, false, ports.ErrAssistanceKeyReused
		}
		return copyAssistance(existing), false, nil
	}
	s.number++
	r = copyAssistance(r)
	r.Number = domain.AssistanceNumber(r.RequestedAt, s.number)
	s.byID[r.ID] = r
	s.byKey[k] = r.ID
	return copyAssistance(r), true, nil
}

// Get returns the request with the id, whoever owns it.
func (s *AssistanceStore) Get(_ context.Context, id string) (domain.AssistanceRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[id]
	if !ok {
		return domain.AssistanceRequest{}, ports.ErrAssistanceRequestNotFound
	}
	return copyAssistance(r), nil
}

// copyAssistance returns a request that shares no pointer with r, so neither
// the caller nor the store can change what the other holds.
func copyAssistance(r domain.AssistanceRequest) domain.AssistanceRequest {
	l := r.Location
	if l.Coordinates != nil {
		c := *l.Coordinates
		l.Coordinates = &c
	}
	if l.AccuracyM != nil {
		a := *l.AccuracyM
		l.AccuracyM = &a
	}
	if l.CapturedAt != nil {
		at := *l.CapturedAt
		l.CapturedAt = &at
	}
	r.Location = l
	if r.Place != nil {
		p := *r.Place
		r.Place = &p
	}
	return r
}
