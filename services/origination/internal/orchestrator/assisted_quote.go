// Package orchestrator coordinates the quote use cases: profiling and rating
// run in process, without a network hop between them.
package orchestrator

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

const instrumentation = "origination/orchestrator"

// ProfileResolver is the profiling the quote needs; *profiling.Resolver
// implements it, with the degradation chain of HA-D01.
type ProfileResolver interface {
	Resolve(ctx context.Context, customerID string, consentActive bool) (domain.ProfileResolution, error)
}

// ErrConsentRequired is returned when the customer has no active consent.
var ErrConsentRequired = profiling.ErrConsentRequired

// AssistedQuoteService is the use case of HU-W1.1-01: an advisor registers or
// recovers a customer and gets a quote priced with the same profiling and
// rating as the embedded channel.
type AssistedQuoteService struct {
	customers ports.CustomerRepository
	quotes    ports.QuoteRepository
	profiles  ProfileResolver
	rater     ports.Rater
	clock     ports.Clock
	newID     func() (string, error)

	created metric.Int64Counter
}

// NewAssistedQuoteService builds the service.
func NewAssistedQuoteService(customers ports.CustomerRepository, quotes ports.QuoteRepository,
	profiles ProfileResolver, rater ports.Rater, clock ports.Clock) *AssistedQuoteService {
	created, _ := observability.Meter(instrumentation).Int64Counter("assisted_quotes.created",
		metric.WithDescription("Assisted quotes generated, by profile mode and source"), metric.WithUnit("{quote}"))
	return &AssistedQuoteService{
		customers: customers, quotes: quotes, profiles: profiles, rater: rater, clock: clock,
		newID: newID, created: created,
	}
}

// Create validates the request, profiles and rates the customer and persists
// the quote with a validity of 24 hours. A *domain.ValidationError names the
// invalid fields and no quote is generated. When the external sources are
// down the quote is built in degraded mode (cached or base actuarial profile)
// and says so.
func (s *AssistedQuoteService) Create(ctx context.Context, advisorID string, req domain.QuoteRequest) (domain.Quote, error) {
	ctx, span := observability.Tracer(instrumentation).Start(ctx, "assisted_quote.create")
	defer span.End()

	now := s.clock.Now().UTC()
	customer, err := req.Validate(now)
	if err != nil {
		span.SetAttributes(attribute.Bool("quote.valid_request", false))
		return domain.Quote{}, err
	}
	if !*req.ConsentActive {
		return domain.Quote{}, ErrConsentRequired
	}

	if customer, err = s.saveCustomer(ctx, customer); err != nil {
		return domain.Quote{}, fail(span, "could not save the customer", err)
	}

	res, err := s.profile(ctx, customer.ID)
	if err != nil {
		return domain.Quote{}, fail(span, "could not profile the customer", err)
	}
	rating, err := s.rate(ctx, customer, req, res.Profile, now)
	if err != nil {
		return domain.Quote{}, fail(span, "could not rate the quote", err)
	}

	id, err := s.newID()
	if err != nil {
		return domain.Quote{}, fail(span, "could not generate the quote id", err)
	}
	q := buildQuote(id, advisorID, customer, req, res, rating, now)
	if err := s.quotes.Create(ctx, q); err != nil {
		return domain.Quote{}, fail(span, "could not save the quote", err)
	}

	span.SetAttributes(attribute.String("quote.mode", string(q.Mode)), attribute.String("quote.profile_source", string(q.ProfileSource)))
	s.created.Add(ctx, 1, metric.WithAttributes(
		attribute.String("mode", string(q.Mode)), attribute.String("profile_source", string(q.ProfileSource))))
	slog.InfoContext(ctx, "assisted quote created", "quote_id", q.ID, "customer_id", q.CustomerID, "advisor_id", advisorID,
		"mode", q.Mode, "profile_source", q.ProfileSource, "premium", q.Premium)
	return q, nil
}

// Get returns a quote. It stays readable after it expires, so the advisor can
// be told why it can no longer be formalized.
func (s *AssistedQuoteService) Get(ctx context.Context, quoteID string) (domain.Quote, error) {
	return s.quotes.Get(ctx, quoteID)
}

// CustomerByDocument recovers an existing customer so the advisor does not
// type the data again. It returns ports.ErrCustomerNotFound for an unknown one.
func (s *AssistedQuoteService) CustomerByDocument(ctx context.Context, documentID string) (domain.Customer, error) {
	if !domain.ValidDocumentID(documentID) {
		return domain.Customer{}, &domain.ValidationError{Fields: []domain.FieldError{{Field: "document_id", Code: domain.CodeInvalidFormat}}}
	}
	return s.customers.GetByDocument(ctx, documentID)
}

// Now returns the service clock, so callers compute a quote status against
// the same time source.
func (s *AssistedQuoteService) Now() time.Time { return s.clock.Now().UTC() }

// saveCustomer creates the customer or updates the one that already has the
// document, keeping its ID so the profile cache keeps working.
func (s *AssistedQuoteService) saveCustomer(ctx context.Context, c domain.Customer) (domain.Customer, error) {
	if existing, err := s.customers.GetByDocument(ctx, c.DocumentID); err == nil {
		c.ID = existing.ID
	} else if errors.Is(err, ports.ErrCustomerNotFound) {
		if c.ID, err = s.newID(); err != nil {
			return domain.Customer{}, err
		}
	} else {
		return domain.Customer{}, err
	}
	return s.customers.Save(ctx, c)
}

func (s *AssistedQuoteService) profile(ctx context.Context, customerID string) (domain.ProfileResolution, error) {
	ctx, span := observability.Tracer(instrumentation).Start(ctx, "assisted_quote.profile")
	defer span.End()
	// The advisor affirmed the consent in the request (Create checked it).
	res, err := s.profiles.Resolve(ctx, customerID, true)
	if err != nil {
		if errors.Is(err, profiling.ErrConsentRequired) {
			return domain.ProfileResolution{}, ErrConsentRequired
		}
		span.RecordError(err)
		span.SetStatus(codes.Error, "profiling failed")
		return domain.ProfileResolution{}, err
	}
	span.SetAttributes(attribute.String("profile.mode", string(res.Mode)), attribute.String("profile.source", string(res.Source)))
	return res, nil
}

func (s *AssistedQuoteService) rate(ctx context.Context, c domain.Customer, req domain.QuoteRequest,
	profile domain.RiskProfile, now time.Time) (domain.Rating, error) {
	ctx, span := observability.Tracer(instrumentation).Start(ctx, "assisted_quote.rate")
	defer span.End()
	r, err := s.rater.Rate(ctx, ports.RatingInput{
		Age: domain.AgeAt(c.BirthDate, now), InsuredAmount: req.InsuredAmount, TermMonths: req.TermMonths, Profile: profile,
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "rating failed")
		return domain.Rating{}, err
	}
	span.SetAttributes(attribute.String("rating.version", r.Version))
	return r, nil
}

func buildQuote(id, advisorID string, c domain.Customer, req domain.QuoteRequest, res domain.ProfileResolution,
	r domain.Rating, now time.Time) domain.Quote {
	return domain.Quote{
		ID: id, CustomerID: c.ID, AdvisorID: advisorID, Applicant: c.Applicant(), BirthDate: c.BirthDate,
		InsuredAmount: req.InsuredAmount, TermMonths: req.TermMonths,
		Premium: r.Premium, Currency: r.Currency, Coverages: r.Coverages, RatingVersion: r.Version,
		Mode: res.Mode, ProfileSource: res.Source, ProfileScore: res.Profile.Score, ProfileCategory: res.Profile.Category,
		ProfileDate: res.Profile.CalculatedAt, UnavailableSource: res.UnavailableSource,
		CreatedAt: now, ExpiresAt: now.Add(domain.QuoteValidity),
	}
}

// fail marks the span as failed and wraps the error with what was being done.
func fail(span trace.Span, msg string, err error) error {
	if !errors.Is(err, ErrConsentRequired) {
		span.RecordError(err)
		span.SetStatus(codes.Error, msg)
	}
	return fmt.Errorf("%s: %w", msg, err)
}

// newID returns a random UUID (version 4).
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("could not generate an id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
