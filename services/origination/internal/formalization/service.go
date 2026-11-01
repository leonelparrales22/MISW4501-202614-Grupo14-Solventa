// Package formalization lets an advisor formalize the purchase of a valid
// quote on behalf of the customer (HU-W1.1-03). The customer's consent is a
// confirmation code sent to them and typed by the advisor (CA-5); formalizing
// records who, when and from which quote (CA-4), starts the issuance through
// the acceptance without a manual step (CA-3), refuses an expired quote
// (CA-2) and lets exactly one of two concurrent formalizations complete
// (CA-6).
package formalization

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Errors of the formalization. A quote that does not exist is
// ports.ErrQuoteNotFound.
var (
	// ErrAlreadyFormalized: another advisor formalized the quote first.
	ErrAlreadyFormalized = errors.New("quote already formalized")
	// ErrConsentCodeRequired: no code was sent to the customer yet.
	ErrConsentCodeRequired = errors.New("no confirmation code was sent for the quote")
	// ErrConsentCodeInvalid: the code is not the one sent.
	ErrConsentCodeInvalid = errors.New("the confirmation code is not valid")
	// ErrConsentCodeExpired: the code was sent more than domain.ConsentCodeTTL ago.
	ErrConsentCodeExpired = errors.New("the confirmation code expired")
	// ErrConsentCodeLocked: the quote used all its attempts.
	ErrConsentCodeLocked = errors.New("too many confirmation code attempts")
	// ErrConsentCodeLimit: the quote used all the codes it may be sent.
	ErrConsentCodeLimit = ports.ErrConsentCodeLimit
)

// QuoteExpiredError is returned when the quote is no longer valid (CA-2): the
// advisor must generate a new one.
type QuoteExpiredError struct {
	QuoteID   string
	ExpiresAt time.Time
}

func (e *QuoteExpiredError) Error() string {
	return fmt.Sprintf("quote %s expired at %s", e.QuoteID, e.ExpiresAt.UTC().Format(time.RFC3339))
}

// Accepter registers the acceptance of the offer on behalf of the customer
// and reads it back; issuance.AcceptanceService implements it.
type Accepter interface {
	AcceptForCustomer(ctx context.Context, offerID string) (domain.Acceptance, bool, error)
	GetByOffer(ctx context.Context, offerID string) (domain.Acceptance, error)
}

// Deps are the collaborators of the Service.
type Deps struct {
	Quotes ports.QuoteRepository
	// Customers gives the account of the customer, if they have one. May be nil.
	Customers    ports.CustomerRepository
	Offers       ports.PersonalizedOfferStore
	Formalized   ports.FormalizationStore
	Challenges   ports.ConsentChallengeStore
	Sender       ports.ConsentCodeSender
	Acceptances  Accepter
	Clock        ports.Clock
	NewCode      func() (string, error)
	OnFormalized func()
}

// Service formalizes purchases.
type Service struct {
	Deps
}

// New builds a Service. NewCode and OnFormalized may be nil.
func New(d Deps) *Service {
	if d.NewCode == nil {
		d.NewCode = randomCode
	}
	return &Service{Deps: d}
}

// ConsentRequest says where the code of a quote went.
type ConsentRequest struct {
	QuoteID   string
	SentTo    string
	ExpiresAt time.Time
}

// Result is a formalized purchase and the acceptance that starts its issuance.
type Result struct {
	Formalization domain.Formalization
	Acceptance    domain.Acceptance
	// Created is false when the caller repeated a formalization that was
	// already complete.
	Created bool
}

// RequestConsent sends the customer a confirmation code for the quote (CA-5).
// A new code replaces the previous one, up to domain.MaxConsentCodeSends.
func (s *Service) RequestConsent(ctx context.Context, quoteID string) (ConsentRequest, error) {
	if err := security.RequireAnyRole(ctx, security.RoleAdvisor); err != nil {
		return ConsentRequest{}, err
	}
	claims, ok := security.ClaimsFromContext(ctx)
	if !ok {
		return ConsentRequest{}, security.ErrUnauthenticated
	}
	quote, err := s.Quotes.Get(ctx, quoteID)
	if err != nil {
		return ConsentRequest{}, err
	}
	if _, err := s.Formalized.GetByQuote(ctx, quoteID); err == nil {
		return ConsentRequest{}, ErrAlreadyFormalized
	} else if !errors.Is(err, ports.ErrFormalizationNotFound) {
		return ConsentRequest{}, err
	}
	now := s.Clock.Now().UTC()
	if !quote.CanFormalize(now) {
		return ConsentRequest{}, &QuoteExpiredError{QuoteID: quote.ID, ExpiresAt: quote.ExpiresAt}
	}

	code, err := s.NewCode()
	if err != nil {
		return ConsentRequest{}, err
	}
	expires := now.Add(domain.ConsentCodeTTL)
	if expires.After(quote.ExpiresAt) {
		expires = quote.ExpiresAt
	}
	challenge, err := s.Challenges.Issue(ctx, domain.ConsentChallenge{
		QuoteID: quote.ID, AdvisorID: claims.Subject, CodeHash: domain.HashConsentCode(quote.ID, code),
		CreatedAt: now, ExpiresAt: expires,
	})
	if err != nil {
		return ConsentRequest{}, err
	}
	if err := s.Sender.SendConsentCode(ctx, domain.ConsentCodeNotice{
		To: quote.Applicant.Email, CustomerName: quote.Applicant.FullName, Code: code, ExpiresAt: expires,
		IdempotencyKey: fmt.Sprintf("consent-code-%s-%d", quote.ID, challenge.Sends),
	}); err != nil {
		return ConsentRequest{}, fmt.Errorf("send the confirmation code: %w", err)
	}
	slog.InfoContext(ctx, "confirmation code sent", "quote_id", quote.ID, "advisor_id", claims.Subject, "sends", challenge.Sends)
	return ConsentRequest{QuoteID: quote.ID, SentTo: MaskEmail(quote.Applicant.Email), ExpiresAt: expires}, nil
}

// Input is what the advisor gives to formalize: how and when the customer
// accepted (CA-5) and, when the method is the confirmation code, the code the
// customer received.
type Input struct {
	Acceptance domain.ConsentAcceptance
	Code       string
}

// Formalize formalizes the purchase of the quote on behalf of the customer. The
// acceptance is validated (a *domain.ValidationError names every invalid
// field) and, with the confirmation code method, so is the code. Repeating it
// by the advisor who formalized returns the same result, whatever the input;
// another advisor gets ErrAlreadyFormalized (CA-6).
func (s *Service) Formalize(ctx context.Context, quoteID string, in Input) (Result, error) {
	if err := security.RequireAnyRole(ctx, security.RoleAdvisor); err != nil {
		return Result{}, err
	}
	claims, ok := security.ClaimsFromContext(ctx)
	if !ok {
		return Result{}, security.ErrUnauthenticated
	}
	quote, err := s.Quotes.Get(ctx, quoteID)
	if err != nil {
		return Result{}, err
	}

	switch existing, err := s.Formalized.GetByQuote(ctx, quoteID); {
	case err == nil:
		return s.resume(ctx, existing, claims.Subject)
	case !errors.Is(err, ports.ErrFormalizationNotFound):
		return Result{}, err
	}

	now := s.Clock.Now().UTC()
	if !quote.CanFormalize(now) {
		return Result{}, &QuoteExpiredError{QuoteID: quote.ID, ExpiresAt: quote.ExpiresAt}
	}
	acceptance := in.Acceptance
	acceptance.Reference = strings.TrimSpace(acceptance.Reference)
	if err := acceptance.Validate(now, quote.CreatedAt); err != nil {
		return Result{}, err
	}
	if acceptance.Method == domain.ConsentConfirmationCode {
		if err := s.verifyCode(ctx, quote.ID, in.Code, now); err != nil {
			return Result{}, err
		}
	}

	stored, created, err := s.Formalized.Create(ctx, domain.Formalization{
		ID: domain.FormalizationID(quote.ID), QuoteID: quote.ID, OfferID: domain.FormalizationOfferID(quote.ID), CustomerID: quote.CustomerID,
		AdvisorID: claims.Subject, AdvisorEmail: claims.Email, Consent: acceptance, FormalizedAt: now,
	})
	if err != nil {
		return Result{}, fmt.Errorf("record the formalization: %w", err)
	}
	if !created {
		// Another advisor recorded it between the check and here.
		return s.resume(ctx, stored, claims.Subject)
	}
	res, err := s.complete(ctx, stored)
	res.Created = true
	return res, err
}

// Get returns the formalization of the quote and its acceptance, or
// ports.ErrFormalizationNotFound.
func (s *Service) Get(ctx context.Context, quoteID string) (Result, error) {
	if err := security.RequireAnyRole(ctx, security.RoleAdvisor); err != nil {
		return Result{}, err
	}
	f, err := s.Formalized.GetByQuote(ctx, quoteID)
	if err != nil {
		return Result{}, err
	}
	a, err := s.Acceptances.GetByOffer(ctx, f.OfferID)
	if err != nil && !errors.Is(err, ports.ErrAcceptanceNotFound) {
		return Result{}, err
	}
	return Result{Formalization: f, Acceptance: a}, nil
}

// resume finishes a formalization that exists: the advisor who made it gets it
// back (and its acceptance is completed if the process stopped before), anyone
// else is refused.
func (s *Service) resume(ctx context.Context, f domain.Formalization, advisorID string) (Result, error) {
	if f.AdvisorID != advisorID {
		return Result{}, ErrAlreadyFormalized
	}
	return s.complete(ctx, f)
}

// complete makes sure the offer of the quote and the acceptance exist, which
// starts the issuance (CA-3). Every step is idempotent, so a formalization that
// stopped half way is finished by repeating it.
func (s *Service) complete(ctx context.Context, f domain.Formalization) (Result, error) {
	quote, err := s.Quotes.Get(ctx, f.QuoteID)
	if err != nil {
		return Result{Formalization: f}, err
	}
	if _, _, err := s.Offers.Create(ctx, buildOffer(quote, f)); err != nil {
		return Result{Formalization: f}, fmt.Errorf("store the offer of the quote: %w", err)
	}
	if err := s.assignOwner(ctx, f); err != nil {
		return Result{Formalization: f}, err
	}
	a, _, err := s.Acceptances.AcceptForCustomer(ctx, f.OfferID)
	if err != nil {
		return Result{Formalization: f}, fmt.Errorf("register the acceptance of the formalization: %w", err)
	}
	if s.OnFormalized != nil {
		s.OnFormalized()
	}
	slog.InfoContext(ctx, "purchase formalized", "quote_id", f.QuoteID, "offer_id", f.OfferID,
		"acceptance_id", a.ID, "advisor_id", f.AdvisorID)
	return Result{Formalization: f, Acceptance: a}, nil
}

// assignOwner makes the customer's account the owner of the offer when they
// have one, so the acceptance belongs to them: they can follow the charge,
// give their payment method and read their policy. A customer an advisor
// quoted who has no account yet leaves the offer without owner.
func (s *Service) assignOwner(ctx context.Context, f domain.Formalization) error {
	if s.Customers == nil {
		return nil
	}
	quote, err := s.Quotes.Get(ctx, f.QuoteID)
	if err != nil {
		return err
	}
	customer, err := s.Customers.GetByDocument(ctx, quote.Applicant.DocumentID)
	switch {
	case errors.Is(err, ports.ErrCustomerNotFound):
		return nil
	case err != nil:
		return fmt.Errorf("read the customer of the quote: %w", err)
	case !customer.Registered():
		return nil
	}
	if _, err := s.Offers.Claim(ctx, f.OfferID, customer.CognitoSub); err != nil {
		return fmt.Errorf("assign the offer to the customer: %w", err)
	}
	return nil
}

// verifyCode checks the code against the one sent. Every typed code counts,
// whether it is right or not.
func (s *Service) verifyCode(ctx context.Context, quoteID, code string, now time.Time) error {
	challenge, err := s.Challenges.Get(ctx, quoteID)
	if errors.Is(err, ports.ErrConsentChallengeNotFound) {
		return ErrConsentCodeRequired
	}
	if err != nil {
		return err
	}
	if challenge.IsExpired(now) {
		return ErrConsentCodeExpired
	}
	attempts, err := s.Challenges.RegisterAttempt(ctx, quoteID)
	if err != nil {
		return err
	}
	if attempts > domain.MaxConsentCodeAttempts {
		return ErrConsentCodeLocked
	}
	if !domain.ValidConsentCode(code) || !challenge.Matches(code) {
		return ErrConsentCodeInvalid
	}
	return nil
}

// buildOffer turns the quote into the offer the acceptance and the charge
// read. Its id comes from the quote, and it is valid as long as the quote is.
func buildOffer(q domain.Quote, f domain.Formalization) domain.PersonalizedOffer {
	o := domain.PersonalizedOffer{
		ID: f.OfferID, CustomerID: q.CustomerID, Channel: domain.OfferChannelAssisted,
		QuoteID: q.ID, ConsentID: domain.FormalizationID(q.ID), PartnerID: domain.AdvisorPartnerID, ApplicationID: q.ID,
		Applicant: q.Applicant, BirthDate: q.BirthDate, AgeAtQuote: domain.AgeAt(q.BirthDate, q.CreatedAt),
		Terms:   domain.CoverageTerms{InsuredAmount: q.InsuredAmount, TermMonths: q.TermMonths, Coverages: q.Coverages},
		Premium: q.Premium, Currency: q.Currency,
		Mode: q.Mode, ProfileSource: q.ProfileSource, ProfileDate: q.ProfileDate, UnavailableSource: q.UnavailableSource,
		Profile: domain.ProfileSnapshot{
			Score: q.ProfileScore, Category: q.ProfileCategory, CalculatedAt: q.ProfileDate, Partial: q.Mode == domain.ModeProvisional,
		},
		RaterVersion: q.RatingVersion, CreatedAt: f.FormalizedAt, ExpiresAt: q.ExpiresAt,
	}
	if q.Mode == domain.ModeProvisional {
		o.ProvisionalReason = domain.ProvisionalCachedProfile
		if q.ProfileSource == domain.SourceBaseActuarial {
			o.ProvisionalReason = domain.ProvisionalBaseProfile
		}
	}
	return o
}

// randomCode returns a uniformly random code of domain.ConsentCodeLength digits.
func randomCode() (string, error) {
	max := new(big.Int).Exp(big.NewInt(10), big.NewInt(domain.ConsentCodeLength), nil)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return "", fmt.Errorf("could not generate the confirmation code: %w", err)
	}
	return fmt.Sprintf("%0*d", domain.ConsentCodeLength, n), nil
}

// MaskEmail hides most of an address ("jo***@example.com") so the advisor can
// tell the customer where the code went without the screen leaking the
// address.
func MaskEmail(addr string) string {
	at := strings.LastIndex(addr, "@")
	if at <= 0 {
		return ""
	}
	local := addr[:at]
	keep := 2
	if len(local) < keep {
		keep = len(local)
	}
	return local[:keep] + "***" + addr[at:]
}

// ErrCodeDeliveryUnavailable is returned when the confirmation code cannot be
// delivered: the e-mail provider is not configured or it is down.
var ErrCodeDeliveryUnavailable = errors.New("the confirmation code cannot be delivered")

// UnavailableSender is the ports.ConsentCodeSender of a service without an
// e-mail provider: the formalization cannot get the customer's consent, so no
// purchase is formalized without it.
type UnavailableSender struct{}

// SendConsentCode always fails with ErrCodeDeliveryUnavailable.
func (UnavailableSender) SendConsentCode(context.Context, domain.ConsentCodeNotice) error {
	return ErrCodeDeliveryUnavailable
}
