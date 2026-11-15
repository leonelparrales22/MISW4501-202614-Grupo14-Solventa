// Package assistance is the on-site assistance of the claims service
// (decision #11). HU-M2.3-01: it records the requests the phone sends with the
// location of the insured. The inbox of operations, the dispatch and the
// closing with a reason are HU-W2.3-03, which calls assessors to choose the
// provider; this package does not compute distances.
package assistance

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Reasons a policy does not take a request, as the answer names them (the
// same ones as the web filing, HU-W2.1-01).
const (
	ReasonPolicyNotFound  = "policy_not_found"
	ReasonPolicyNotActive = "policy_not_active"
)

// PolicyNotEligibleError says why the policy does not take the request.
// Policy is nil when the policy is not one of the insured's: a policy of
// someone else is reported as one that does not exist.
type PolicyNotEligibleError struct {
	Reason string
	Policy *domain.Policy
}

func (e *PolicyNotEligibleError) Error() string {
	return "claims: the policy does not take the request for assistance: " + e.Reason
}

// Requester is who sends the request, as the token says it.
type Requester struct {
	Subject string
	// Name is the name of the token; empty when it has none.
	Name string
}

// Service records and reads the requests for on-site assistance.
type Service struct {
	requests ports.AssistanceStore
	policies ports.PolicyDirectory
	clock    ports.Clock
	newID    func() (string, error)
}

// NewService builds the service over its store, the policy core and the
// clock of the server.
func NewService(requests ports.AssistanceStore, policies ports.PolicyDirectory, clock ports.Clock) *Service {
	return &Service{requests: requests, policies: policies, clock: clock, newID: newID}
}

// Request records a request for assistance of the requester (criteria 2, 4
// and 5). The body is checked first, so an invalid one costs no call to the
// policy core; then the policy must be an active one of the requester. If the
// core does not answer nothing is recorded (decision D2). created is false
// when the key had already recorded this same request.
//
// Errors: *domain.ValidationError, *PolicyNotEligibleError,
// ports.ErrPolicyDirectoryUnavailable, ports.ErrAssistanceKeyReused or a
// failure of the store.
func (s *Service) Request(ctx context.Context, requester Requester, key string,
	in domain.AssistanceRequestInput) (domain.AssistanceRequest, bool, error) {
	// The database keeps microseconds; so does the answer.
	now := s.clock.Now().UTC().Truncate(time.Microsecond)
	draft, err := in.Validate(now)
	if err != nil {
		return domain.AssistanceRequest{}, false, err
	}

	policies, err := s.policies.HolderPolicies(ctx, requester.Subject)
	if err != nil {
		return domain.AssistanceRequest{}, false, err
	}
	policy, err := domain.EligibleForAssistance(policies, draft.PolicyID, now)
	switch {
	case errors.Is(err, domain.ErrPolicyNotFound):
		return domain.AssistanceRequest{}, false, &PolicyNotEligibleError{Reason: ReasonPolicyNotFound}
	case errors.Is(err, domain.ErrPolicyNotActive):
		return domain.AssistanceRequest{}, false, &PolicyNotEligibleError{Reason: ReasonPolicyNotActive, Policy: &policy}
	}

	id, err := s.newID()
	if err != nil {
		return domain.AssistanceRequest{}, false, err
	}
	return s.requests.Create(ctx, domain.AssistanceRequest{
		ID:            id,
		Subject:       requester.Subject,
		RequesterName: requester.Name,
		Key:           key,
		Fingerprint:   draft.Fingerprint(),
		Policy:        domain.AssistancePolicyOf(policy),
		Location:      draft.Location,
		Place:         draft.Place,
		Status:        domain.AssistanceNew,
		RequestedAt:   now,
	})
}

// Get returns the request with the id when it is the subject's. A request of
// someone else is ports.ErrAssistanceRequestNotFound, as one that does not
// exist (assumption S9).
func (s *Service) Get(ctx context.Context, subject, id string) (domain.AssistanceRequest, error) {
	r, err := s.requests.Get(ctx, id)
	if err != nil {
		return domain.AssistanceRequest{}, err
	}
	if r.Subject != subject {
		return domain.AssistanceRequest{}, ports.ErrAssistanceRequestNotFound
	}
	return r, nil
}

// newID returns a random UUID (version 4). The one of filing is not exported
// and this package does not import it.
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("could not generate an id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
