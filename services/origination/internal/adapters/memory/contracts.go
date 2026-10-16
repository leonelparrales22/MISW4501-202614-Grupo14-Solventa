package memory

import (
	"context"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ContractStore is an in-memory ports.ContractRepository. PENDING: the
// PostgreSQL repository; until then an issuance survives dependency failures
// but not a restart of the service.
type ContractStore struct {
	mu       sync.Mutex
	byPolicy map[string]domain.ContractIssuance
}

// NewContractStore builds an empty ContractStore.
func NewContractStore() *ContractStore {
	return &ContractStore{byPolicy: map[string]domain.ContractIssuance{}}
}

// Create stores the issuance unless its policy already has one.
func (s *ContractStore) Create(_ context.Context, c domain.ContractIssuance) (domain.ContractIssuance, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.byPolicy[c.Event.PolicyID]; ok {
		return cloneContract(existing), false, nil
	}
	s.byPolicy[c.Event.PolicyID] = cloneContract(c)
	return cloneContract(c), true, nil
}

// Get returns the issuance of the policy.
func (s *ContractStore) Get(_ context.Context, policyID string) (domain.ContractIssuance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.byPolicy[policyID]
	if !ok {
		return domain.ContractIssuance{}, ports.ErrContractNotFound
	}
	return cloneContract(c), nil
}

// Update replaces a stored issuance.
func (s *ContractStore) Update(_ context.Context, c domain.ContractIssuance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byPolicy[c.Event.PolicyID]; !ok {
		return ports.ErrContractNotFound
	}
	s.byPolicy[c.Event.PolicyID] = cloneContract(c)
	return nil
}

// ListDue returns the issuances in progress that are due at now, oldest first.
func (s *ContractStore) ListDue(_ context.Context, now time.Time, limit int) ([]domain.ContractIssuance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var due []domain.ContractIssuance
	for _, c := range s.byPolicy {
		inProgress := c.Status == domain.ContractPending || c.Status == domain.ContractDocumentReady
		if inProgress && !c.NextAttemptAt.After(now) {
			due = append(due, cloneContract(c))
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].ReceivedAt.Before(due[j].ReceivedAt) })
	if limit > 0 && len(due) > limit {
		due = due[:limit]
	}
	return due, nil
}

func cloneContract(c domain.ContractIssuance) domain.ContractIssuance {
	c.Event.Coverages = slices.Clone(c.Event.Coverages)
	if c.Document != nil {
		d := *c.Document
		c.Document = &d
	}
	return c
}

// IssuanceAuditLog is an in-memory, append-only ports.IssuanceAudit. PENDING:
// the audit service, which will receive these facts as events.
type IssuanceAuditLog struct {
	mu       sync.Mutex
	byPolicy map[string][]domain.IssuanceAuditEntry
}

// NewIssuanceAuditLog builds an empty log.
func NewIssuanceAuditLog() *IssuanceAuditLog {
	return &IssuanceAuditLog{byPolicy: map[string][]domain.IssuanceAuditEntry{}}
}

// Record appends the entry.
func (l *IssuanceAuditLog) Record(_ context.Context, e domain.IssuanceAuditEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.byPolicy[e.PolicyID] = append(l.byPolicy[e.PolicyID], e)
	return nil
}

// List returns the entries of the policy, oldest first.
func (l *IssuanceAuditLog) List(_ context.Context, policyID string) ([]domain.IssuanceAuditEntry, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.byPolicy[policyID]), nil
}
