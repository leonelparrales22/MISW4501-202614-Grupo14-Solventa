package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Limits of the issuance exceptions list.
const (
	DefaultExceptionsLimit = 50
	MaxExceptionsLimit     = 200
)

// ErrInvalidNote is returned when a resumption has no note or a note longer
// than domain.MaxResumeNoteLength characters.
var ErrInvalidNote = errors.New("a note of 1 to 500 characters is required to resume a charge")

// ErrInvalidLimit is returned for a list limit outside 1..MaxExceptionsLimit.
var ErrInvalidLimit = errors.New("limit must be between 1 and 200")

// Service answers the state of a premium charge (screen E4b, CA-2) and lets
// operations see the issuance exceptions and resume them (CA-3; the monitoring
// screen is HU-W1.3-04).
type Service struct {
	repo   ports.ChargeRepository
	clock  ports.Clock
	policy resilience.RetryPolicy
	notify func()
	// contracts and wakeContracts retry a failed contractual issuance
	// (HU-W1.3-04); nil until WithContractRetry.
	contracts     ports.ContractRetrier
	wakeContracts func()
}

// NewService builds a Service. policy gives the window of a resumed charge;
// notify, when not nil, wakes the worker after a resumption.
func NewService(repo ports.ChargeRepository, clock ports.Clock, policy resilience.RetryPolicy, notify func()) *Service {
	if notify == nil {
		notify = func() {}
	}
	return &Service{repo: repo, clock: clock, policy: policy, notify: notify}
}

// Get returns the charge of an acceptance to the insured who owns it, an
// advisor or an operations analyst.
func (s *Service) Get(ctx context.Context, acceptanceID string) (domain.Charge, error) {
	c, err := s.repo.Get(ctx, acceptanceID)
	if err != nil {
		return domain.Charge{}, err
	}
	if err := security.RequireOwnerOrRole(ctx, c.CustomerID, security.RoleAdvisor, security.RoleOperations); err != nil {
		return domain.Charge{}, err
	}
	return c, nil
}

// ListExceptions returns the failed charges, the latest first, to operations.
func (s *Service) ListExceptions(ctx context.Context, limit int) ([]domain.Charge, error) {
	if err := security.RequireAnyRole(ctx, security.RoleOperations); err != nil {
		return nil, err
	}
	if limit < 1 || limit > MaxExceptionsLimit {
		return nil, ErrInvalidLimit
	}
	return s.repo.ListExceptions(ctx, limit)
}

// Resume moves a failed charge back to pending with the same idempotency key
// and a new retry window. Only operations may do it, with a note; the author
// is the subject of their token.
func (s *Service) Resume(ctx context.Context, acceptanceID, note string) (domain.Charge, error) {
	if err := security.RequireAnyRole(ctx, security.RoleOperations); err != nil {
		return domain.Charge{}, err
	}
	note, err := cleanNote(note)
	if err != nil {
		return domain.Charge{}, err
	}
	return s.resume(ctx, acceptanceID, note)
}

// cleanNote trims the note of an analyst and checks its length.
func cleanNote(note string) (string, error) {
	note = strings.TrimSpace(note)
	if note == "" || utf8.RuneCountInString(note) > domain.MaxResumeNoteLength {
		return "", ErrInvalidNote
	}
	return note, nil
}

func (s *Service) resume(ctx context.Context, acceptanceID, note string) (domain.Charge, error) {
	caller, _ := security.ClaimsFromContext(ctx)
	now := s.clock.Now().UTC()
	c, err := s.repo.Resume(ctx, acceptanceID, domain.ChargeResumption{
		By: caller.Subject, Note: note, At: now, WindowEndsAt: s.policy.WindowEnd(now),
	})
	if err != nil {
		if errors.Is(err, ports.ErrChargeNotFound) || errors.Is(err, ports.ErrChargeNotResumable) {
			return domain.Charge{}, err
		}
		return domain.Charge{}, fmt.Errorf("could not resume the charge of acceptance %s: %w", acceptanceID, err)
	}
	slog.InfoContext(ctx, "premium charge resumed", "acceptance_id", c.AcceptanceID, "resumed_by", c.LastResumedBy,
		"resume_count", c.ResumeCount, "retry_window_ends_at", c.WindowEndsAt)
	s.notify()
	return c, nil
}

// ErrInvalidPaymentMethod is returned when the payment method of screen E4 is
// not usable.
var ErrInvalidPaymentMethod = errors.New("the payment method is not valid")

// AttachPaymentMethod stores the payment method the insured handed over in
// screen E4 and makes the charge due, so the worker of HA-D02 charges it with
// the mechanism unchanged (HU-W1.3-02).
//
// Only the insured who owns the acceptance: paying is not something an
// advisor or an analyst does on someone else's behalf.
func (s *Service) AttachPaymentMethod(ctx context.Context, acceptanceID string,
	m domain.PaymentMethod) (domain.Charge, error) {
	current, err := s.repo.Get(ctx, acceptanceID)
	if err != nil {
		return domain.Charge{}, err
	}
	// No privileged roles on purpose: paying is not something an advisor or
	// an analyst does on someone else's behalf, so only the owner passes.
	if err := security.RequireOwnerOrRole(ctx, current.CustomerID); err != nil {
		return domain.Charge{}, err
	}
	if err := validatePaymentMethod(m); err != nil {
		return domain.Charge{}, err
	}

	c, err := s.repo.AttachPaymentMethod(ctx, acceptanceID, m, s.clock.Now().UTC())
	if err != nil {
		if errors.Is(err, ports.ErrChargeNotFound) || errors.Is(err, ports.ErrChargeNotPayable) {
			return domain.Charge{}, err
		}
		return domain.Charge{}, fmt.Errorf("could not attach the payment method of acceptance %s: %w", acceptanceID, err)
	}
	// The token is never logged: it is the credential of a card.
	slog.InfoContext(ctx, "payment method stored", "acceptance_id", c.AcceptanceID,
		"method", string(c.PaymentMethod.Kind), "payment_attempt", c.PaymentAttempt,
		"idempotency_key", c.IdempotencyKey)
	s.notify()
	return c, nil
}

// validatePaymentMethod keeps out what the gateway or the screen could not
// use. The strictest rule is about the token: a value that looks like a card
// number means the client sent the PAN instead of tokenising it, and that
// must fail loudly rather than be charged.
func validatePaymentMethod(m domain.PaymentMethod) error {
	switch m.Kind {
	case domain.PaymentCard:
		if m.Token == "" || !validToken(m.Token) {
			return fmt.Errorf("%w: a card needs its gateway token", ErrInvalidPaymentMethod)
		}
		if looksLikeACardNumber(m.Token) {
			return fmt.Errorf("%w: the token looks like a card number; the card is tokenised by the browser, never sent here",
				ErrInvalidPaymentMethod)
		}
		if m.Last4 != "" && !fourDigits(m.Last4) {
			return fmt.Errorf("%w: last4 must be exactly four digits", ErrInvalidPaymentMethod)
		}
	case domain.PaymentPSE:
		if m.BankCode == "" {
			return fmt.Errorf("%w: PSE needs the bank", ErrInvalidPaymentMethod)
		}
		// PSE asks the bank whether the payer is a person or a company
		// before it can take the debit, so the screen has to say which.
		if !domain.ValidPayerType(m.PayerType) {
			return fmt.Errorf("%w: PSE needs the payer type, natural or legal", ErrInvalidPaymentMethod)
		}
	default:
		return fmt.Errorf("%w: the method must be card or pse", ErrInvalidPaymentMethod)
	}
	if m.Installments < 1 || m.Installments > domain.MaxInstallments {
		return fmt.Errorf("%w: instalments must be between 1 and %d", ErrInvalidPaymentMethod, domain.MaxInstallments)
	}
	return nil
}

func validToken(s string) bool {
	if len(s) < 8 || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// looksLikeACardNumber is true for 13 to 19 digits and nothing else.
func looksLikeACardNumber(s string) bool {
	if len(s) < 13 || len(s) > 19 {
		return false
	}
	return allDigits(s)
}

func fourDigits(s string) bool { return len(s) == 4 && allDigits(s) }

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
