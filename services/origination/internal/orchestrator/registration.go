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

// ErrIdentityNotVerified is returned when the identity provider refused the
// person, or when the document was closed by an earlier refusal. It carries no
// reason on purpose: the route is public, and telling the person they are on a
// restrictive list, or that their document looks forged, is exactly what
// criterion 3 forbids.
var ErrIdentityNotVerified = errors.New("the identity of the person could not be verified")

// RegistrationService signs up an insured: it keeps the two faces of their
// identity document, stores the customer, verifies who they are and only then
// creates the account they sign in with.
type RegistrationService struct {
	customers ports.CustomerRepository
	documents ports.DocumentStore
	verifier  ports.IdentityVerifier
	identity  ports.IdentityProvider
	clock     ports.Clock
	newID     func() (string, error)
}

// NewRegistrationService builds the service.
func NewRegistrationService(customers ports.CustomerRepository, documents ports.DocumentStore,
	verifier ports.IdentityVerifier, identity ports.IdentityProvider, clock ports.Clock) *RegistrationService {
	return &RegistrationService{
		customers: customers, documents: documents, verifier: verifier, identity: identity, clock: clock,
		newID: newID,
	}
}

// ReadDocument asks the identity provider to read the two faces, so the person
// confirms the data printed on the document before signing up (criterion 1).
// Nothing is stored: the faces are kept by the sign-up that follows.
func (s *RegistrationService) ReadDocument(ctx context.Context, front, back string) (domain.DocumentData, error) {
	f, b, err := domain.DocumentFaces(front, back)
	if err != nil {
		return domain.DocumentData{}, err
	}
	data, err := s.verifier.ReadDocument(ctx, f, b)
	if err != nil {
		return domain.DocumentData{}, err
	}
	return data, nil
}

// Register keeps the identity document, stores the customer, verifies the
// person, creates the account and returns the customer with its subject.
//
// The order is the one that leaves no claim without its backing: the faces go
// to the document store first, because the customer records the key of each
// one; then the customer, before the account exists. The identity is verified
// BEFORE the account is created (criterion 5): a person the provider refused
// never gets an account, so there is no account to disable or to forget to
// disable. A failure after the customer is stored leaves a registration that
// can be retried with the same document.
func (s *RegistrationService) Register(ctx context.Context, r domain.RegistrationRequest) (domain.Customer, error) {
	registration, err := r.Validate(s.clock.Now().UTC())
	if err != nil {
		return domain.Customer{}, err
	}
	c := registration.Customer
	if err := s.free(ctx, c.DocumentID, c.Email); err != nil {
		return domain.Customer{}, err
	}
	if err := s.keep(ctx, registration); err != nil {
		return domain.Customer{}, err
	}

	now := s.clock.Now().UTC()
	c.CreatedAt, c.UpdatedAt = now, now
	c.IdentityStatus = domain.IdentityPending
	if c.ID, err = s.newID(); err != nil {
		return domain.Customer{}, fmt.Errorf("could not generate the customer id: %w", err)
	}
	stored, err := s.customers.Save(ctx, c)
	if err != nil {
		return domain.Customer{}, err
	}

	if stored, err = s.verify(ctx, stored, registration); err != nil {
		return domain.Customer{}, err
	}

	subject, err := s.identity.CreateAccount(ctx, c.Email, r.Password, c.FullName)
	if err != nil {
		if errors.Is(err, ports.ErrAccountExists) {
			return domain.Customer{}, ErrAlreadyRegistered
		}
		// The customer stays stored, verified and without a subject: the
		// registration is incomplete and retrying it with the same document
		// finds the same row.
		slog.ErrorContext(ctx, "the customer was verified but the account could not be created",
			"customer_id", stored.ID, "error", err)
		return domain.Customer{}, err
	}

	// The subject and the CustomerLinked event are written together (outbox,
	// HU-M1.3-01): audit learns who the holder of the consents is without a
	// call between services. If this write fails the customer stays without
	// a subject, as before, and the registration can be retried.
	now = s.clock.Now().UTC()
	stored.CognitoSub = subject
	stored.UpdatedAt = now
	linked, err := domain.NewCustomerLinked(stored, now)
	if err != nil {
		return domain.Customer{}, err
	}
	registered, err := s.customers.Link(ctx, stored, linked)
	if err != nil {
		slog.ErrorContext(ctx, "the account was created but the customer could not be linked to it",
			"customer_id", stored.ID, "error", err)
		return domain.Customer{}, err
	}
	slog.InfoContext(ctx, "insured registered", "customer_id", registered.ID,
		"document_front_key", registered.DocumentFrontKey, "document_back_key", registered.DocumentBackKey)
	return registered, nil
}

// verify asks the identity provider and records its answer on the customer.
// An unavailable provider leaves the customer pending and the sign-up
// retryable; a refusal records why, for the platform only, and ends the
// sign-up with ErrIdentityNotVerified.
func (s *RegistrationService) verify(ctx context.Context, c domain.Customer, registration domain.Registration) (domain.Customer, error) {
	verdict, err := s.verifier.Verify(ctx, domain.IdentityCheck{
		DocumentID: c.DocumentID, FullName: c.FullName, BirthDate: c.BirthDate,
		FrontSHA256: registration.Front.SHA256, BackSHA256: registration.Back.SHA256,
	})
	if err != nil {
		slog.ErrorContext(ctx, "the identity of the insured could not be checked", "customer_id", c.ID, "error", err)
		return domain.Customer{}, err
	}

	c.IdentityVerificationID, c.IdentityCheckedAt = verdict.VerificationID, verdict.CheckedAt
	c.UpdatedAt = s.clock.Now().UTC()
	if verdict.Approved {
		c.IdentityStatus, c.IdentityRejection = domain.IdentityVerified, ""
	} else {
		c.IdentityStatus, c.IdentityRejection = domain.IdentityRejected, verdict.Rejection
	}
	stored, err := s.customers.Save(ctx, c)
	if err != nil {
		return domain.Customer{}, err
	}
	if !verdict.Approved {
		// The reason stays in the log, with the provider's id to look the
		// case up; the answer the person gets says nothing about it.
		slog.WarnContext(ctx, "the identity provider refused the insured", "customer_id", stored.ID,
			"verification_id", verdict.VerificationID, "reason", verdict.Rejection, "final", verdict.Rejection.Final())
		return domain.Customer{}, ErrIdentityNotVerified
	}
	return stored, nil
}

// keep writes the two faces of the identity document. The key of each one is
// derived from its content, so writing the same face again is the same object:
// a retried sign-up neither duplicates the image nor collides with the
// immutability of the store.
func (s *RegistrationService) keep(ctx context.Context, registration domain.Registration) error {
	for _, d := range registration.Documents() {
		if err := s.documents.Put(ctx, d.Key(), d.Content, d.ContentType()); err != nil {
			return fmt.Errorf("store the %s of the identity document: %w", d.Face, err)
		}
	}
	return nil
}

// free reports ErrAlreadyRegistered when the document or the email belongs to
// someone who already finished a registration, and ErrIdentityNotVerified when
// the document was closed for good by a refusal (restrictive list). A customer
// without a subject is a registration left halfway and can be completed.
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
		case existing.SignUpClosed():
			return ErrIdentityNotVerified
		}
	}
	return nil
}
