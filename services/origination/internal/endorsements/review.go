package endorsements

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// HU-W1.4-02: the operations analyst reviews the change requests the insured
// filed and applies, rejects or returns them. Only the operations role does
// any of it. Applying runs the validations of the type of the change
// (decision D-4 a), calling KYC and claims before any transaction (EC-D02):
// a validation that fails records the attempt and leaves the policy as it
// was; a validation that cannot be verified changes nothing; when all hold,
// the endorsement is stored with the terms that result, which every channel
// reads from then on (decision D-3 a).

// Errors of the review.
var (
	// ErrValidationUnverifiable: KYC, claims or the original offer did not
	// answer, so the endorsement could not be decided and nothing changed.
	ErrValidationUnverifiable = errors.New("the validations of the endorsement could not be verified")
)

// UnverifiableError names the validations that could not be verified.
type UnverifiableError struct {
	Codes []domain.ValidationCode
}

func (e *UnverifiableError) Error() string {
	return fmt.Sprintf("%s: %v", ErrValidationUnverifiable, e.Codes)
}

// Unwrap makes the error match ErrValidationUnverifiable.
func (e *UnverifiableError) Unwrap() error { return ErrValidationUnverifiable }

// DecidedError tells that the request is no longer where the action needs it
// (another analyst decided first), with the request as it is now.
type DecidedError struct {
	Request domain.ChangeRequest
	// Cause is ports.ErrRequestAlreadyDecided or ports.ErrNotReturnable.
	Cause error
}

func (e *DecidedError) Error() string {
	return fmt.Sprintf("%s: %s is %s", e.Cause, e.Request.ID, e.Request.Status)
}

// Unwrap makes the error match its cause.
func (e *DecidedError) Unwrap() error { return e.Cause }

// RenewalReader reads the renewal of a period of a policy (HU-W1.5-02).
type RenewalReader interface {
	Get(ctx context.Context, policyID string, periodEnd time.Time) (domain.Renewal, error)
}

// Pricer prices a new insured value of a policy with the rating engine and the
// inputs of its issuance (decision D-7 a): the risk profile kept with its
// original offer, its term and the age at the quote. The rating engine is the
// only one that prices: this module never computes a premium itself.
type Pricer struct {
	Origins Origins
	Offers  ports.OfferReader
	Rater   ports.Rater
}

// Price returns the rating of the policy with the new insured value. priced is
// false, with no error, when the original offer cannot be found or the engine
// cannot price it: then the change cannot be applied (unverifiable).
func (p Pricer) Price(ctx context.Context, pol domain.Policy, amount float64) (rated domain.Rating, priced bool, err error) {
	if p.Origins == nil || p.Offers == nil || p.Rater == nil {
		return domain.Rating{}, false, nil
	}
	origin, err := p.Origins.Find(ctx, pol)
	if err != nil {
		return domain.Rating{}, false, fmt.Errorf("find the origin of policy %s: %w", pol.ID, err)
	}
	if origin.OfferID == "" {
		return domain.Rating{}, false, nil
	}
	offer, err := p.Offers.Get(ctx, origin.OfferID)
	if errors.Is(err, ports.ErrOfferNotFound) {
		return domain.Rating{}, false, nil
	}
	if err != nil {
		return domain.Rating{}, false, fmt.Errorf("read the original offer of policy %s: %w", pol.ID, err)
	}
	age := offer.AgeAtQuote
	if age == 0 && !offer.BirthDate.IsZero() {
		age = domain.AgeAt(offer.BirthDate, offer.CreatedAt)
	}
	in := ports.RatingInput{Age: age, InsuredAmount: amount, TermMonths: offer.Terms.TermMonths,
		Profile: domain.RiskProfile{CustomerID: offer.CustomerID, Score: offer.Profile.Score, Category: offer.Profile.Category,
			CalculatedAt: offer.Profile.CalculatedAt, Partial: offer.Profile.Partial}}
	rated, err = p.Rater.Rate(ctx, in)
	if err != nil || rated.Premium <= 0 {
		slog.WarnContext(ctx, "the new insured value of an endorsement cannot be priced", "policy_id", pol.ID, "error", err)
		return domain.Rating{}, false, nil
	}
	if rated.Currency == "" {
		rated.Currency = pol.Currency
	}
	return rated, true, nil
}

// Review is the use case of the operations analyst.
type Review struct {
	policies  ports.PolicyRepository
	requests  ports.ChangeRequestRepository
	store     ports.EndorsementStore
	terms     *Terms
	documents ports.IdentityDocumentChecker
	claims    ports.OpenClaimsChecker
	renewals  RenewalReader
	pricer    Pricer
	clock     ports.Clock
	wake      func()
}

// ReviewConfig is what the review needs. Documents and Claims are optional:
// without them the validations that need them are unverifiable, so no
// endorsement of beneficiaries nor of insured value is applied (fail closed).
type ReviewConfig struct {
	Policies  ports.PolicyRepository
	Requests  ports.ChangeRequestRepository
	Store     ports.EndorsementStore
	Terms     *Terms
	Documents ports.IdentityDocumentChecker
	Claims    ports.OpenClaimsChecker
	Renewals  RenewalReader
	Pricer    Pricer
	Clock     ports.Clock
	// Wake asks the worker of the e-mails to send the new one now; optional.
	Wake func()
}

// NewReview builds a Review.
func NewReview(c ReviewConfig) (*Review, error) {
	if c.Policies == nil || c.Requests == nil || c.Store == nil || c.Terms == nil || c.Renewals == nil || c.Clock == nil {
		return nil, errors.New("endorsements: policies, requests, store, terms, renewals and clock are required")
	}
	wake := c.Wake
	if wake == nil {
		wake = func() {}
	}
	return &Review{policies: c.Policies, requests: c.Requests, store: c.Store, terms: c.Terms, documents: c.Documents,
		claims: c.Claims, renewals: c.Renewals, pricer: c.Pricer, clock: c.Clock, wake: wake}, nil
}

// actor is the analyst of the token: the role is checked first.
func actor(ctx context.Context) (domain.Actor, error) {
	if err := security.RequireAnyRole(ctx, security.RoleOperations); err != nil {
		return domain.Actor{}, err
	}
	c, _ := security.ClaimsFromContext(ctx)
	return domain.Actor{Subject: c.Subject, Name: c.Name, Email: c.Email}, nil
}

// Inbox returns the page of the requests that pass the filter (screen O2), with
// the indicators.
func (s *Review) Inbox(ctx context.Context, f domain.EndorsementInboxFilter) (domain.EndorsementInboxPage, error) {
	if _, err := actor(ctx); err != nil {
		return domain.EndorsementInboxPage{}, err
	}
	f = f.WithDefaults()
	f.Now = s.clock.Now().UTC()
	if err := f.Validate(); err != nil {
		return domain.EndorsementInboxPage{}, err
	}
	return s.store.Inbox(ctx, f)
}

// ValidationsSource tells where the validations of a detail come from.
type ValidationsSource string

const (
	// SourcePreview: computed now, without writing anything (a request the
	// analyst can still apply).
	SourcePreview ValidationsSource = "preview"
	// SourceAttempt: the ones of the last attempt to apply it.
	SourceAttempt ValidationsSource = "attempt"
	// SourceNone: it was rejected before anybody tried to apply it.
	SourceNone ValidationsSource = "none"
)

// PremiumChange is the premium before and after a change of insured value.
type PremiumChange struct {
	Previous float64
	// New is the premium the engine prices for the new insured value; zero
	// when Priced is false.
	New      float64
	Priced   bool
	Currency string
	Version  string
}

// Detail is what screen O3 shows of a request: the request, the policy with its
// current terms and endorsements, the validations and what was done.
type Detail struct {
	Request      domain.ChangeRequest
	Policy       domain.Policy
	InForce      bool
	Terms        domain.PolicyTerms
	Endorsements ports.PolicyEndorsements
	// Premium is set for a change of insured value.
	Premium     *PremiumChange
	Validations domain.Validations
	Source      ValidationsSource
	Record      domain.RequestRecord
}

// request reads a request by id: an id that is not one is not found.
func (s *Review) request(ctx context.Context, requestID string) (domain.ChangeRequest, error) {
	if _, _, ok := domain.ParseChangeRequestID(requestID); !ok {
		return domain.ChangeRequest{}, ports.ErrChangeRequestNotFound
	}
	return s.requests.Get(ctx, requestID)
}

// Detail returns a request with its policy (screen O3). While the request can
// still be applied, its validations are a preview computed now (KYC and
// claims are asked, nothing is written).
func (s *Review) Detail(ctx context.Context, requestID string) (Detail, error) {
	if _, err := actor(ctx); err != nil {
		return Detail{}, err
	}
	r, err := s.request(ctx, requestID)
	if err != nil {
		return Detail{}, err
	}
	p, err := s.policies.Get(ctx, r.PolicyID)
	if err != nil {
		return Detail{}, err
	}
	current, _, err := s.terms.Current(ctx, p)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Request: r, Policy: p, InForce: p.InForceAt(s.clock.Now().UTC()), Terms: current}
	if d.Endorsements, err = s.store.Endorsements(ctx, p.ID); err != nil {
		return Detail{}, err
	}
	if d.Record, err = s.store.Record(ctx, r.ID); err != nil {
		return Detail{}, err
	}
	switch {
	case r.CanDecide():
		v, rated, priced, err := s.evaluate(ctx, r, p, current)
		if err != nil {
			return Detail{}, err
		}
		d.Validations, d.Source = v.Listed(), SourcePreview
		d.Premium = premiumOf(r, p, rated, priced)
	case d.Record.LastAttempt != nil:
		d.Validations, d.Source = d.Record.LastAttempt.Validations.Listed(), SourceAttempt
	default:
		d.Validations, d.Source = domain.Validations{}, SourceNone
	}
	if d.Premium == nil && r.Type == domain.ChangeInsuredAmount {
		if e := d.Record.Endorsement; e != nil {
			d.Premium = &PremiumChange{Previous: e.PreviousPremium, New: e.Terms.Premium, Priced: true, Currency: e.Terms.Currency}
		} else {
			rated, priced, err := s.pricer.Price(ctx, p, r.Requested.InsuredAmount)
			if err != nil {
				return Detail{}, err
			}
			d.Premium = premiumOf(r, p, rated, priced)
		}
	}
	return d, nil
}

func premiumOf(r domain.ChangeRequest, p domain.Policy, rated domain.Rating, priced bool) *PremiumChange {
	if r.Type != domain.ChangeInsuredAmount {
		return nil
	}
	out := &PremiumChange{Previous: p.Premium, Priced: priced, Currency: p.Currency}
	if priced {
		out.New, out.Version = rated.Premium, rated.Version
	}
	return out
}

// evaluate gathers the facts of the validations (KYC, claims, the renewal of
// the period and the price of a new insured value) and runs them.
func (s *Review) evaluate(ctx context.Context, r domain.ChangeRequest, p domain.Policy, current domain.PolicyTerms) (
	domain.Validations, domain.Rating, bool, error) {
	f := domain.EndorsementFacts{Policy: p, Current: current, Now: s.clock.Now().UTC()}
	for _, b := range domain.BeneficiariesToCheck(r, current) {
		status := domain.DocumentUnverifiable
		if s.documents != nil {
			status = s.documents.CheckDocument(ctx, b)
		}
		f.Documents = append(f.Documents, domain.DocumentCheck{Beneficiary: b, Status: status})
	}
	if domain.NeedsOpenClaims(r.Type) && s.claims != nil {
		if n, err := s.claims.OpenClaims(ctx, p.ID); err == nil {
			f.OpenClaims = &n
		} else {
			slog.WarnContext(ctx, "the claims in progress of an endorsement are unverifiable", "request_id", r.ID, "error", err)
		}
	}
	var (
		rated  domain.Rating
		priced bool
	)
	if r.Type == domain.ChangeInsuredAmount {
		renewal, err := s.renewals.Get(ctx, p.ID, p.CoverageEnd)
		switch {
		case err == nil:
			f.RenewalInProgress = renewal.Status == domain.RenewalAccepted
		case !errors.Is(err, ports.ErrRenewalNotFound):
			return nil, domain.Rating{}, false, fmt.Errorf("read the renewal of policy %s: %w", p.ID, err)
		}
		if rated, priced, err = s.pricer.Price(ctx, p, r.Requested.InsuredAmount); err != nil {
			return nil, domain.Rating{}, false, err
		}
		f.PremiumPriced = priced
	}
	return domain.EvaluateEndorsement(r, f), rated, priced, nil
}

// Review takes a filed request (it moves to in review and the insured sees
// it so); taking it again changes nothing. Any other status is a
// *DecidedError.
func (s *Review) Review(ctx context.Context, requestID string) (domain.ChangeRequest, error) {
	by, err := actor(ctx)
	if err != nil {
		return domain.ChangeRequest{}, err
	}
	if _, err := s.request(ctx, requestID); err != nil {
		return domain.ChangeRequest{}, err
	}
	if _, err := s.store.StartReview(ctx, requestID, by, s.clock.Now().UTC()); err != nil {
		return domain.ChangeRequest{}, s.decided(ctx, requestID, err)
	}
	return s.requests.Get(ctx, requestID)
}

// decided turns the loss of a claim into a *DecidedError with the request as
// it is now; any other error is returned as it is.
func (s *Review) decided(ctx context.Context, requestID string, err error) error {
	if !errors.Is(err, ports.ErrRequestAlreadyDecided) && !errors.Is(err, ports.ErrNotReturnable) {
		return err
	}
	r, getErr := s.requests.Get(ctx, requestID)
	if getErr != nil {
		return getErr
	}
	return &DecidedError{Request: r, Cause: err}
}

// ApplyResult is the outcome of applying a request: the endorsement, or the
// attempt that was not applied with its validations.
type ApplyResult struct {
	Outcome     domain.EndorsementOutcome
	Request     domain.ChangeRequest
	Endorsement *domain.Endorsement
	Attempt     *domain.EndorsementAttempt
	Validations domain.Validations
}

// Apply applies a filed or in review request (CA-2, CA-3). It returns:
//   - outcome applied with the endorsement when every validation holds (and
//     the same endorsement when the request was already applied: a double
//     click never creates two);
//   - outcome not_applied with the attempt when a validation fails (and the
//     same attempt when it was already not applied and not returned);
//   - an *UnverifiableError when a validation cannot be verified: nothing
//     changes;
//   - ports.ErrPolicyChanged when another endorsement of the policy was
//     applied while it was validated: nothing changes;
//   - a *DecidedError when the request was rejected or returned.
func (s *Review) Apply(ctx context.Context, requestID string) (ApplyResult, error) {
	ctx, span := observability.Tracer(instrumentation).Start(ctx, "endorsement.apply")
	defer span.End()
	by, err := actor(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	r, err := s.request(ctx, requestID)
	if err != nil {
		return ApplyResult{}, err
	}
	if !r.CanDecide() {
		return s.alreadyDone(ctx, r)
	}
	p, err := s.policies.Get(ctx, r.PolicyID)
	if err != nil {
		return ApplyResult{}, err
	}
	current, version, err := s.terms.Current(ctx, p)
	if err != nil {
		return ApplyResult{}, err
	}
	// The calls to KYC and claims happen here, outside any transaction.
	v, rated, _, err := s.evaluate(ctx, r, p, current)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "could not validate the endorsement")
		return ApplyResult{}, err
	}
	at := s.clock.Now().UTC()
	span.SetAttributes(attribute.String("change_request.id", r.ID), attribute.String("change_request.type", string(r.Type)))
	switch {
	case v.Failed():
		attempt, err := s.store.RecordNotApplied(ctx, domain.EndorsementAttempt{RequestID: r.ID, PolicyID: r.PolicyID,
			Validations: v, By: by, At: at})
		if err != nil {
			return s.lost(ctx, r.ID, err)
		}
		// Never the names, documents or contact data the request carries.
		slog.InfoContext(ctx, "endorsement not applied", "request_id", r.ID, "policy_id", r.PolicyID,
			"change_type", string(r.Type), "failed", string(v.FirstFailed()))
		after, err := s.requests.Get(ctx, r.ID)
		if err != nil {
			return ApplyResult{}, err
		}
		return ApplyResult{Outcome: domain.OutcomeNotApplied, Request: after, Attempt: &attempt, Validations: v.Listed()}, nil
	case v.Unverifiable():
		unverifiable := v.Of(domain.ValidationUnverifiable)
		slog.WarnContext(ctx, "endorsement not decided: a validation is unverifiable", "request_id", r.ID, "policy_id", r.PolicyID,
			"change_type", string(r.Type))
		return ApplyResult{}, &UnverifiableError{Codes: unverifiable}
	}
	terms := domain.ApplyChange(domain.TermsOf(p, current), r, rated)
	e, err := s.store.Apply(ctx, ports.AppliedEndorsement{Request: r, BaseVersion: version, Terms: terms,
		PreviousPremium: p.Premium, Validations: v, By: by, At: at})
	if err != nil {
		if errors.Is(err, ports.ErrPolicyChanged) {
			return ApplyResult{}, err
		}
		return s.lost(ctx, r.ID, err)
	}
	s.wake()
	slog.InfoContext(ctx, "endorsement applied", "request_id", r.ID, "policy_id", r.PolicyID, "change_type", string(r.Type),
		"endorsement_id", e.ID, "version", e.Version)
	after, err := s.requests.Get(ctx, r.ID)
	if err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{Outcome: domain.OutcomeApplied, Request: after, Endorsement: &e, Validations: v.Listed()}, nil
}

// lost answers a claim that another analyst won while this one validated:
// the request as the winner left it.
func (s *Review) lost(ctx context.Context, requestID string, err error) (ApplyResult, error) {
	if !errors.Is(err, ports.ErrRequestAlreadyDecided) {
		return ApplyResult{}, err
	}
	r, getErr := s.requests.Get(ctx, requestID)
	if getErr != nil {
		return ApplyResult{}, getErr
	}
	return s.alreadyDone(ctx, r)
}

// alreadyDone answers an apply on a request that is no longer decidable: the
// endorsement or the attempt that decided it, or a *DecidedError.
func (s *Review) alreadyDone(ctx context.Context, r domain.ChangeRequest) (ApplyResult, error) {
	switch {
	case r.Status == domain.ChangeRequestApplied, r.CanReturn():
		rec, err := s.store.Record(ctx, r.ID)
		if err != nil {
			return ApplyResult{}, err
		}
		if r.Status == domain.ChangeRequestApplied && rec.Endorsement != nil {
			out := ApplyResult{Outcome: domain.OutcomeApplied, Request: r, Endorsement: rec.Endorsement}
			if rec.LastAttempt != nil {
				out.Validations = rec.LastAttempt.Validations.Listed()
			}
			return out, nil
		}
		if rec.LastAttempt != nil && r.CanReturn() {
			return ApplyResult{Outcome: domain.OutcomeNotApplied, Request: r, Attempt: rec.LastAttempt,
				Validations: rec.LastAttempt.Validations.Listed()}, nil
		}
	}
	return ApplyResult{}, &DecidedError{Request: r, Cause: ports.ErrRequestAlreadyDecided}
}

// Reject rejects a filed or in review request with observations the insured
// reads (10 to 500 characters).
func (s *Review) Reject(ctx context.Context, requestID, observations string) (domain.ChangeRequest, error) {
	return s.close(ctx, requestID, observations, "rejected", s.store.Reject)
}

// Return returns a not applied request to the insured with observations they
// read: they see it rejected with them and can file a new one.
func (s *Review) Return(ctx context.Context, requestID, observations string) (domain.ChangeRequest, error) {
	return s.close(ctx, requestID, observations, "returned", s.store.Return)
}

func (s *Review) close(ctx context.Context, requestID, observations, what string,
	write func(context.Context, string, string, domain.Actor, time.Time) error) (domain.ChangeRequest, error) {
	by, err := actor(ctx)
	if err != nil {
		return domain.ChangeRequest{}, err
	}
	obs, err := domain.NormalizeObservations(observations)
	if err != nil {
		return domain.ChangeRequest{}, err
	}
	r, err := s.request(ctx, requestID)
	if err != nil {
		return domain.ChangeRequest{}, err
	}
	if err := write(ctx, requestID, obs, by, s.clock.Now().UTC()); err != nil {
		return domain.ChangeRequest{}, s.decided(ctx, requestID, err)
	}
	s.wake()
	slog.InfoContext(ctx, "change request "+what, "request_id", r.ID, "policy_id", r.PolicyID, "change_type", string(r.Type))
	return s.requests.Get(ctx, requestID)
}

// History returns the endorsements and the attempts not applied of a policy,
// the newest first (screen O4). A policy without them has an empty history.
func (s *Review) History(ctx context.Context, policyID string) (domain.Policy, []domain.EndorsementHistoryEntry, error) {
	if _, err := actor(ctx); err != nil {
		return domain.Policy{}, nil, err
	}
	p, err := s.policies.Get(ctx, policyID)
	if err != nil {
		return domain.Policy{}, nil, err
	}
	entries, err := s.store.History(ctx, p.ID)
	if err != nil {
		return domain.Policy{}, nil, err
	}
	return p, entries, nil
}
