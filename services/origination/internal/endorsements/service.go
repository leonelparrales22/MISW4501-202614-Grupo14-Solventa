// Package endorsements holds the changes and endorsements of a policy with
// their traceability. HU-W1.4-01 lets the insured ask for a change of one of
// their policies in force (new beneficiaries, insured value or contact data)
// and follow it; HU-W1.4-02 reviews and applies it in this same module.
//
// Only the owner of the policy, with the insured role, files and reads its
// requests (CA-4). A request is filed only when every field is valid (CA-3)
// and, per policy and type, only one can be open at a time (decision D-6 a).
package endorsements

import (
	"context"
	"errors"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

const instrumentation = "origination/endorsements"

// Service is the use case behind the change requests of the insured.
type Service struct {
	policies ports.PolicyRepository
	requests ports.ChangeRequestRepository
	terms    *Terms
	clock    ports.Clock
}

// Config is what the service needs to be built.
type Config struct {
	Policies ports.PolicyRepository
	Requests ports.ChangeRequestRepository
	Origins  Origins
	// PartnerNames are the display names of the partners (decision D-14 a);
	// optional: without a name the onerous beneficiary shows its code.
	PartnerNames map[string]string
	// Endorsements reads the last applied endorsement of a policy
	// (HU-W1.4-02); optional: without it the terms are the ones of the
	// policy and its origin.
	Endorsements EndorsedTermsReader
	Clock        ports.Clock
}

// NewService builds a Service.
func NewService(c Config) (*Service, error) {
	if c.Policies == nil || c.Requests == nil || c.Origins == nil || c.Clock == nil {
		return nil, errors.New("endorsements: policies, requests, origins and clock are required")
	}
	terms := NewTerms(c.Origins, c.PartnerNames)
	if c.Endorsements != nil {
		terms.WithEndorsements(c.Endorsements)
	}
	return &Service{policies: c.Policies, requests: c.Requests, terms: terms, clock: c.Clock}, nil
}

// Terms returns the builder of the current terms, for the analyst of
// HU-W1.4-02.
func (s *Service) Terms() *Terms { return s.terms }

// Current is a policy with its current terms, whether it is in force now and
// its open requests (at most one per type).
type Current struct {
	Policy  domain.Policy
	InForce bool
	Terms   domain.PolicyTerms
	Open    []domain.ChangeRequest
}

// ownedPolicy reads the policy for its owner with the insured role. Any other
// role is forbidden before anything is read, and another insured's policy
// before anything else is (decision D-11 a).
func (s *Service) ownedPolicy(ctx context.Context, policyID string) (domain.Policy, error) {
	if err := security.RequireAnyRole(ctx, security.RoleInsured); err != nil {
		return domain.Policy{}, err
	}
	p, err := s.policies.Get(ctx, policyID)
	if err != nil {
		return domain.Policy{}, err
	}
	// No privileged roles: only the owner asks for a change of their policy.
	if err := security.RequireOwnerOrRole(ctx, p.CustomerID); err != nil {
		return domain.Policy{}, err
	}
	return p, nil
}

// CurrentTerms returns what screen A3 starts from (CA-1): the terms the
// insured can change as they stand now, whether the policy is in force and
// its open requests.
func (s *Service) CurrentTerms(ctx context.Context, policyID string) (Current, error) {
	p, err := s.ownedPolicy(ctx, policyID)
	if err != nil {
		return Current{}, err
	}
	terms, err := s.terms.Of(ctx, p)
	if err != nil {
		return Current{}, err
	}
	open, err := s.requests.ListOpen(ctx, p.ID)
	if err != nil {
		return Current{}, err
	}
	return Current{Policy: p, InForce: p.InForceAt(s.clock.Now().UTC()), Terms: terms, Open: open}, nil
}

// File files a change request of the policy for its owner (CA-1, CA-2). It
// returns domain.ErrPolicyNotInForce for a policy that is not in force, a
// *ports.ChangeRequestOpenError when the policy already has an open request
// of the same type, or a *domain.ValidationError naming every invalid field
// (CA-3); in those cases nothing is stored.
func (s *Service) File(ctx context.Context, policyID string, in domain.ChangeInput) (domain.ChangeRequest, error) {
	ctx, span := observability.Tracer(instrumentation).Start(ctx, "change_request.file")
	defer span.End()

	p, err := s.ownedPolicy(ctx, policyID)
	if err != nil {
		return domain.ChangeRequest{}, err
	}
	now := s.clock.Now().UTC()
	if !p.InForceAt(now) {
		return domain.ChangeRequest{}, domain.ErrPolicyNotInForce
	}
	terms, err := s.terms.Of(ctx, p)
	if err != nil {
		return domain.ChangeRequest{}, failed(span, err)
	}
	// The open request of the same type answers first: no change of that type
	// can be filed until it is closed, whatever it asks for.
	if t, ok := domain.ParseChangeType(in.Type); ok {
		open, err := s.requests.ListOpen(ctx, p.ID)
		if err != nil {
			return domain.ChangeRequest{}, failed(span, err)
		}
		for _, r := range open {
			if r.Type == t {
				return domain.ChangeRequest{}, &ports.ChangeRequestOpenError{RequestID: r.ID}
			}
		}
	}
	claims, _ := security.ClaimsFromContext(ctx)
	r, err := domain.NewChangeRequest(p, terms, in,
		domain.Requester{Subject: claims.Subject, Email: claims.Email, Name: claims.Name}, now)
	if err != nil {
		span.SetAttributes(attribute.Bool("change_request.valid", false))
		return domain.ChangeRequest{}, err
	}
	stored, err := s.requests.Create(ctx, r)
	if err != nil {
		if !errors.Is(err, ports.ErrChangeRequestOpen) {
			return domain.ChangeRequest{}, failed(span, err)
		}
		return domain.ChangeRequest{}, err
	}
	span.SetAttributes(attribute.String("change_request.id", stored.ID), attribute.String("change_request.type", string(stored.Type)))
	// Never the names, documents, address, phone or e-mail it carries.
	slog.InfoContext(ctx, "change request filed", "request_id", stored.ID, "policy_id", stored.PolicyID,
		"change_type", string(stored.Type))
	return stored, nil
}

// failed records an infrastructure failure on the span.
func failed(span trace.Span, err error) error {
	span.RecordError(err)
	span.SetStatus(codes.Error, "could not file the change request")
	return err
}

// List returns the page of the requests of the caller that pass the filter
// (screen A11), with the indicators and the policies that have requests.
func (s *Service) List(ctx context.Context, f domain.ChangeRequestFilter) (domain.ChangeRequestPage, error) {
	if err := security.RequireAnyRole(ctx, security.RoleInsured); err != nil {
		return domain.ChangeRequestPage{}, err
	}
	claims, _ := security.ClaimsFromContext(ctx)
	f = f.WithDefaults()
	f.Now = s.clock.Now().UTC()
	if err := f.Validate(); err != nil {
		return domain.ChangeRequestPage{}, err
	}
	return s.requests.ListByCustomer(ctx, claims.Subject, f)
}

// Get returns a request of the caller (screen A3b). Another insured's request
// is forbidden; an id that is not one is not found.
func (s *Service) Get(ctx context.Context, requestID string) (domain.ChangeRequest, error) {
	if err := security.RequireAnyRole(ctx, security.RoleInsured); err != nil {
		return domain.ChangeRequest{}, err
	}
	if _, _, ok := domain.ParseChangeRequestID(requestID); !ok {
		return domain.ChangeRequest{}, ports.ErrChangeRequestNotFound
	}
	r, err := s.requests.Get(ctx, requestID)
	if err != nil {
		return domain.ChangeRequest{}, err
	}
	if err := security.RequireOwnerOrRole(ctx, r.CustomerID); err != nil {
		return domain.ChangeRequest{}, err
	}
	return r, nil
}
