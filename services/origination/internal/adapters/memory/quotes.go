package memory

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// CustomerStore is an in-memory ports.CustomerRepository, the one the service
// uses without DATABASE_URL: the customers are lost when the service
// restarts. With DATABASE_URL the service uses postgres.CustomerStore
// instead, with or without a payment gateway (fix C5). Its outbox (Outbox)
// holds the CustomerLinked events of Link (HU-M1.3-01) and is lost on restart
// too.
type CustomerStore struct {
	mu         sync.Mutex
	byDocument map[string]domain.Customer
	outbox     *Outbox
}

// NewCustomerStore builds an empty CustomerStore with an empty outbox.
func NewCustomerStore() *CustomerStore {
	return &CustomerStore{byDocument: map[string]domain.Customer{}, outbox: NewOutbox()}
}

// Outbox returns the outbox where Link writes the CustomerLinked events.
func (s *CustomerStore) Outbox() *Outbox { return s.outbox }

// GetByDocument returns the customer with the given document id.
func (s *CustomerStore) GetByDocument(_ context.Context, documentID string) (domain.Customer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.byDocument[documentID]
	if !ok {
		return domain.Customer{}, ports.ErrCustomerNotFound
	}
	return c, nil
}

// GetByEmail returns ErrCustomerNotFound when the email is unknown.
func (s *CustomerStore) GetByEmail(_ context.Context, email string) (domain.Customer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.byDocument {
		if strings.EqualFold(c.Email, email) {
			return c, nil
		}
	}
	return domain.Customer{}, ports.ErrCustomerNotFound
}

// Save stores the customer under its document id, replacing the previous data.
func (s *CustomerStore) Save(_ context.Context, c domain.Customer) (domain.Customer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(c), nil
}

// Link stores the customer with its subject and adds the CustomerLinked event
// to the outbox under the same lock (HU-M1.3-01), so no reader sees one
// without the other. Linking again adds no second event.
func (s *CustomerStore) Link(_ context.Context, c domain.Customer, e domain.CustomerLinked) (domain.Customer, error) {
	env, err := e.Envelope()
	if err != nil {
		return domain.Customer{}, fmt.Errorf("build the CustomerLinked event: %w", err)
	}
	body, err := env.Encode()
	if err != nil {
		return domain.Customer{}, fmt.Errorf("encode the CustomerLinked event: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored := s.save(c)
	s.outbox.Add(env.EventID, env.EventType, body, e.LinkedAt)
	return stored, nil
}

// save stores c; the caller holds the lock.
func (s *CustomerStore) save(c domain.Customer) domain.Customer {
	if existing, ok := s.byDocument[c.DocumentID]; ok {
		c.ID = existing.ID
		c.CreatedAt = existing.CreatedAt
		if c.CognitoSub == "" {
			c.CognitoSub = existing.CognitoSub
		}
	}
	s.byDocument[c.DocumentID] = c
	return c
}

// QuoteStore is an in-memory ports.QuoteRepository. PENDING: the PostgreSQL
// repository; until then the quotes are lost when the service restarts.
type QuoteStore struct {
	mu   sync.Mutex
	byID map[string]domain.Quote
}

// NewQuoteStore builds an empty QuoteStore.
func NewQuoteStore() *QuoteStore { return &QuoteStore{byID: map[string]domain.Quote{}} }

// Create stores the quote.
func (s *QuoteStore) Create(_ context.Context, q domain.Quote) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[q.ID] = cloneQuote(q)
	return nil
}

// Get returns the quote with the given id.
func (s *QuoteStore) Get(_ context.Context, quoteID string) (domain.Quote, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.byID[quoteID]
	if !ok {
		return domain.Quote{}, ports.ErrQuoteNotFound
	}
	return cloneQuote(q), nil
}

func cloneQuote(q domain.Quote) domain.Quote {
	q.Coverages = slices.Clone(q.Coverages)
	return q
}
