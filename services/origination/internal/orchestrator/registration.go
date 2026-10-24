package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ErrAlreadyRegistered is returned when the document or the email already has
// an account.
var ErrAlreadyRegistered = errors.New("the person already has an account")

// RegistrationService signs up an insured: it stores the customer and creates
// the account they sign in with.
type RegistrationService struct {
	customers ports.CustomerRepository
	identity  ports.IdentityProvider
	clock     ports.Clock
	newID     func() (string, error)
}

// NewRegistrationService builds the service.
func NewRegistrationService(customers ports.CustomerRepository, identity ports.IdentityProvider,
	clock ports.Clock) *RegistrationService {
	return &RegistrationService{customers: customers, identity: identity, clock: clock, newID: newID}
}

// Register stores the customer, creates the account and returns the customer
// with its subject.
//
// The customer is written before the account exists. A failure creating the
// account leaves a customer without a subject, which is a registration that
// can be retried; the opposite order would leave an account nobody owns.
func (s *RegistrationService) Register(ctx context.Context, r domain.RegistrationRequest) (domain.Customer, error) {
	c, err := r.Validate(s.clock.Now().UTC())
	if err != nil {
		return domain.Customer{}, err
	}
	if err := s.free(ctx, c.DocumentID, c.Email); err != nil {
		return domain.Customer{}, err
	}

	now := s.clock.Now().UTC()
	c.CreatedAt, c.UpdatedAt = now, now
	if c.ID, err = s.newID(); err != nil {
		return domain.Customer{}, fmt.Errorf("could not generate the customer id: %w", err)
	}
	stored, err := s.customers.Save(ctx, c)
	if err != nil {
		return domain.Customer{}, err
	}

	subject, err := s.identity.CreateAccount(ctx, c.Email, r.Password, c.FullName)
	if err != nil {
		if errors.Is(err, ports.ErrAccountExists) {
			return domain.Customer{}, ErrAlreadyRegistered
		}
		// The customer stays stored without a subject: the registration is
		// incomplete and retrying it with the same document finds the same row.
		slog.ErrorContext(ctx, "the customer was stored but the account could not be created",
			"customer_id", stored.ID, "error", err)
		return domain.Customer{}, err
	}

	stored.CognitoSub = subject
	stored.UpdatedAt = s.clock.Now().UTC()
	registered, err := s.customers.Save(ctx, stored)
	if err != nil {
		return domain.Customer{}, err
	}
	slog.InfoContext(ctx, "insured registered", "customer_id", registered.ID)
	return registered, nil
}

// free reports ErrAlreadyRegistered when the document or the email belongs to
// someone who already finished a registration. A customer without a subject
// is a registration left halfway and can be completed.
func (s *RegistrationService) free(ctx context.Context, documentID, email string) error {
	for _, lookup := range []func() (domain.Customer, error){
		func() (domain.Customer, error) { return s.customers.GetByDocument(ctx, documentID) },
		func() (domain.Customer, error) { return s.customers.GetByEmail(ctx, email) },
	} {
		existing, err := lookup()
		switch {
		case errors.Is(err, ports.ErrCustomerNotFound):
		case err != nil:
			return err
		case existing.Registered():
			return ErrAlreadyRegistered
		}
	}
	return nil
}
