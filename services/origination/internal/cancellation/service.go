// Package cancellation lets the insured cancel one of their policies in force
// and get back the part of the current month they paid and will not use
// (HU-W1.6-01).
//
// Before confirming, the insured sees the calculation (CA-1): the month paid,
// the days not used, the percentage, the refund, where it goes back to and
// when the cancellation takes effect. When they confirm, the policy is
// cancelled and the refund is registered with its amount, date and payment
// method (CA-2), in one transaction; the refund is then sent to the gateway
// in the background (billing.RefundWorker, decision D-5 b).
//
// Only the owner of the policy, with the insured role, quotes and cancels it.
// Only policies origination issued can be cancelled here (decision D-2 a):
// the policy core keeps no premiums to prorate. The amount the insured
// confirms is the one that is registered: if the refund changed since they
// saw it, nothing is written and they get the new quote.
package cancellation

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

const instrumentation = "origination/cancellation"

// RefundChangedError is returned when the refund the insured confirmed is not
// the one of now. It carries the new quote, to show it. It matches
// domain.ErrRefundChanged.
type RefundChangedError struct {
	Quote domain.CancellationQuote
}

func (e *RefundChangedError) Error() string { return domain.ErrRefundChanged.Error() }

// Unwrap makes the error match domain.ErrRefundChanged.
func (e *RefundChangedError) Unwrap() error { return domain.ErrRefundChanged }

// Waker is woken when a refund is registered in process, so it is sent to the
// gateway at once instead of at its next poll (billing.RefundWorker).
type Waker interface {
	Notify()
}

// Config is what the service needs.
type Config struct {
	Store ports.CancellationStore
	// Rules are the deadlines of the instalments (HU-W1.6-02): they give the
	// months of the premium.
	Rules domain.InstallmentRules
	Clock ports.Clock
	// RetryWindow is how long a refund is retried against the gateway, the
	// window of the premium charge (RETRY_WINDOW_SECONDS); zero for no window.
	RetryWindow time.Duration
	// Refunds is woken after a cancellation with a refund in process;
	// optional.
	Refunds Waker
}

// Service is the use case behind the cancellation of screen A5.
type Service struct {
	cfg Config
}

// NewService builds a Service.
func NewService(c Config) (*Service, error) {
	if c.Store == nil || c.Clock == nil {
		return nil, errors.New("cancellation: a store and a clock are required")
	}
	if err := c.Rules.Validate(); err != nil {
		return nil, err
	}
	if c.RetryWindow < 0 {
		return nil, errors.New("cancellation: the retry window must not be negative")
	}
	return &Service{cfg: c}, nil
}

// insured returns the subject of an insured caller; any other role is
// forbidden before anything is read.
func insured(ctx context.Context) (string, error) {
	if err := security.RequireAnyRole(ctx, security.RoleInsured); err != nil {
		return "", err
	}
	claims, _ := security.ClaimsFromContext(ctx)
	return claims.Subject, nil
}

// Quote returns the calculation of the cancellation of the policy at now
// (CA-1). It returns ports.ErrPolicyNotFound for a policy that is not the
// caller's (another insured's, the policy core's or unknown),
// ports.ErrPolicyAlreadyCancelled, domain.ErrPolicyNotInForce (ended or not
// started) or ports.ErrRenewalInProgress.
func (s *Service) Quote(ctx context.Context, policyID string) (domain.CancellationQuote, error) {
	sub, err := insured(ctx)
	if err != nil {
		return domain.CancellationQuote{}, err
	}
	now := s.cfg.Clock.Now().UTC()
	f, err := s.cfg.Store.Facts(ctx, policyID, now)
	if err != nil {
		return domain.CancellationQuote{}, err
	}
	return s.quote(f, sub, now)
}

// quote checks that the policy can be cancelled by sub at now and applies the
// rule of the refund.
func (s *Service) quote(f ports.CancellationFacts, sub string, now time.Time) (domain.CancellationQuote, error) {
	switch {
	case f.Policy.CustomerID != sub:
		// The same answer as an unknown id: it does not say the policy exists.
		return domain.CancellationQuote{}, ports.ErrPolicyNotFound
	case f.Cancellation != nil:
		return domain.CancellationQuote{}, ports.ErrPolicyAlreadyCancelled
	case !f.Policy.InForceAt(now):
		return domain.CancellationQuote{}, domain.ErrPolicyNotInForce
	case f.Renewal == domain.RenewalAccepted || f.Renewal == domain.RenewalRenewed:
		return domain.CancellationQuote{}, ports.ErrRenewalInProgress
	}
	seq, _, _ := s.cfg.Rules.CurrentMonth(f.Policy, now)
	var method domain.PaymentMethod
	if f.Issuance != nil {
		method = f.Issuance.PaymentMethod
	}
	status := domain.PaymentUpToDate
	for _, i := range f.Installments {
		if i.Status == domain.InstallmentPending {
			status = domain.PaymentPending
		}
	}
	return domain.QuoteCancellation(f.Policy, s.cfg.Rules, domain.PaidMonthOf(seq, f.Issuance, f.Installments), method,
		status, now), nil
}

// Cancel cancels the policy for its owner with the refund they saw (CA-2).
// Besides the errors of Quote, it returns a *domain.ValidationError for an
// invalid request, ports.ErrPaymentInProgress while the sweep is charging an
// instalment of the policy and a *RefundChangedError when the refund is not
// the one the insured confirmed. In those cases nothing is written.
func (s *Service) Cancel(ctx context.Context, policyID string, req domain.CancellationRequest) (domain.PolicyCancellation, error) {
	ctx, span := observability.Tracer(instrumentation).Start(ctx, "cancellation.confirm")
	defer span.End()

	sub, err := insured(ctx)
	if err != nil {
		return domain.PolicyCancellation{}, err
	}
	if err := req.Validate(); err != nil {
		return domain.PolicyCancellation{}, err
	}
	now := s.cfg.Clock.Now().UTC()
	var retryUntil time.Time
	if s.cfg.RetryWindow > 0 {
		retryUntil = now.Add(s.cfg.RetryWindow)
	}
	c, err := s.cfg.Store.Cancel(ctx, policyID, now, func(f ports.CancellationFacts) (domain.InsuredCancellation, error) {
		q, err := s.quote(f, sub, now)
		if err != nil {
			return domain.InsuredCancellation{}, err
		}
		if f.InstallmentHeld {
			return domain.InsuredCancellation{}, ports.ErrPaymentInProgress
		}
		if q.RefundAmountMinor != req.RefundAmountMinor {
			return domain.InsuredCancellation{}, &RefundChangedError{Quote: q}
		}
		return domain.NewInsuredCancellation(q, req, sub, now, retryUntil), nil
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
		return domain.PolicyCancellation{}, err
	}
	status := domain.RefundStatusNotApplicable
	if c.Refund != nil {
		status = c.Refund.Status
		span.SetAttributes(attribute.String("refund.reference", c.Refund.Reference))
	}
	span.SetAttributes(attribute.String("policy_id", policyID), attribute.String("refund.status", string(status)))
	slog.InfoContext(ctx, "policy cancelled at the request of the insured", "policy_id", policyID,
		"reason", c.InsuredReason, "refund_status", status, "refund_reference", refundReference(c))
	if status == domain.RefundInProcess && s.cfg.Refunds != nil {
		s.cfg.Refunds.Notify()
	}
	return c, nil
}

func refundReference(c domain.PolicyCancellation) string {
	if c.Refund == nil {
		return ""
	}
	return c.Refund.Reference
}
