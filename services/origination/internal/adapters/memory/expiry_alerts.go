package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// HU-M3.2-01: the alerts 30, 15 and 5 days before a policy expires.

var (
	_ ports.LinkedHolders       = (*CustomerStore)(nil)
	_ ports.ExpiryAlertRecorder = (*ExpiryAlertStore)(nil)
)

// LinkedSubjects returns the subjects of the customers with an account, in
// order.
func (s *CustomerStore) LinkedSubjects(_ context.Context) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, c := range s.byDocument {
		if c.CognitoSub != "" {
			out = append(out, c.CognitoSub)
		}
	}
	sort.Strings(out)
	return out, nil
}

// ExpiryAlertStore is an in-memory ports.ExpiryAlertRecorder, the one origination
// uses without DATABASE_URL: it adds the PolicyExpiryApproaching events to
// the outbox of the customers, so the same relay publishes them. The alerts
// are lost on restart, like the outbox, so a restarted process alerts the
// last milestone of each policy again.
type ExpiryAlertStore struct {
	mu       sync.Mutex
	recorded map[string]struct{}
	outbox   *Outbox
}

// NewExpiryAlertStore builds an empty store that writes to outbox.
func NewExpiryAlertStore(outbox *Outbox) *ExpiryAlertStore {
	return &ExpiryAlertStore{recorded: map[string]struct{}{}, outbox: outbox}
}

// RecordExpiryAlert records the alert and adds its event under the same lock.
// The same policy, period and milestone again records nothing.
func (s *ExpiryAlertStore) RecordExpiryAlert(_ context.Context, e domain.PolicyExpiryApproaching) (bool, error) {
	env, err := e.Envelope()
	if err != nil {
		return false, fmt.Errorf("build the PolicyExpiryApproaching event: %w", err)
	}
	body, err := env.Encode()
	if err != nil {
		return false, fmt.Errorf("encode the PolicyExpiryApproaching event: %w", err)
	}
	key := fmt.Sprintf("%s|%s|%d", e.PolicyID, e.CoverageEnd.UTC().Format(time.RFC3339Nano), e.MilestoneDays)
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.recorded[key]; ok {
		return false, nil
	}
	s.recorded[key] = struct{}{}
	s.outbox.Add(env.EventID, env.EventType, body, env.OccurredAt)
	return true, nil
}
