package memory

import (
	"context"
	"sync"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// FormalizationStore is the in-memory ports.FormalizationStore the service
// uses without DATABASE_URL (HU-W1.1-03). It never changes a stored record.
type FormalizationStore struct {
	mu      sync.Mutex
	byQuote map[string]domain.Formalization
}

var _ ports.FormalizationStore = (*FormalizationStore)(nil)

// NewFormalizationStore builds an empty store.
func NewFormalizationStore() *FormalizationStore {
	return &FormalizationStore{byQuote: map[string]domain.Formalization{}}
}

// Create stores the formalization unless its quote already has one.
func (s *FormalizationStore) Create(_ context.Context, f domain.Formalization) (domain.Formalization, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.byQuote[f.QuoteID]; ok {
		return existing, false, nil
	}
	s.byQuote[f.QuoteID] = f
	return f, true, nil
}

// GetByQuote returns the formalization of the quote.
func (s *FormalizationStore) GetByQuote(_ context.Context, quoteID string) (domain.Formalization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.byQuote[quoteID]
	if !ok {
		return domain.Formalization{}, ports.ErrFormalizationNotFound
	}
	return f, nil
}

// ConsentChallengeStore is the in-memory ports.ConsentChallengeStore.
type ConsentChallengeStore struct {
	mu      sync.Mutex
	byQuote map[string]domain.ConsentChallenge
}

var _ ports.ConsentChallengeStore = (*ConsentChallengeStore)(nil)

// NewConsentChallengeStore builds an empty store.
func NewConsentChallengeStore() *ConsentChallengeStore {
	return &ConsentChallengeStore{byQuote: map[string]domain.ConsentChallenge{}}
}

// Issue stores the challenge as the code of its quote.
func (s *ConsentChallengeStore) Issue(_ context.Context, c domain.ConsentChallenge) (domain.ConsentChallenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	previous := s.byQuote[c.QuoteID]
	if previous.Sends >= domain.MaxConsentCodeSends {
		return domain.ConsentChallenge{}, ports.ErrConsentCodeLimit
	}
	c.Sends, c.Attempts = previous.Sends+1, previous.Attempts
	s.byQuote[c.QuoteID] = c
	return c, nil
}

// Get returns the challenge of the quote.
func (s *ConsentChallengeStore) Get(_ context.Context, quoteID string) (domain.ConsentChallenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.byQuote[quoteID]
	if !ok {
		return domain.ConsentChallenge{}, ports.ErrConsentChallengeNotFound
	}
	return c, nil
}

// RegisterAttempt counts one typed code.
func (s *ConsentChallengeStore) RegisterAttempt(_ context.Context, quoteID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.byQuote[quoteID]
	if !ok {
		return 0, ports.ErrConsentChallengeNotFound
	}
	c.Attempts++
	s.byQuote[quoteID] = c
	return c.Attempts, nil
}
