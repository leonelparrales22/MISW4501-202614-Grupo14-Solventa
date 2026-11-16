// Package installments bills the monthly premium of the policies origination
// issued and cancels for non-payment the ones that do not pay it
// (HU-W1.6-02).
//
// A sweep does four things, each idempotent, so several tasks of origination
// can sweep at once and a sweep that stops half way is finished by the next:
//
//  1. bills the months that fell due (one instalment per policy and month);
//  2. charges the instalments whose attempt is due, on days 1, 8 and 15 of
//     the grace period, with the card of the issuance and a key per attempt;
//  3. cancels, with no refund, the policies whose grace period ended with an
//     instalment unpaid, together with their PolicyCancelledForNonPayment
//     event (the push to the insured is the notifications service's);
//  4. sends the e-mail of the cancellations that have not got theirs.
package installments

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Defaults of the sweep.
const (
	DefaultInterval = time.Hour
	// DefaultLease is how long a task holds an instalment it is charging:
	// longer than a call to the gateway.
	DefaultLease = 2 * time.Minute
	// batch is how many instalments one step handles per sweep.
	batch = 100
	// maxEmailAttempts is how many times the e-mail of a cancellation is
	// tried before it is given up (and logged).
	maxEmailAttempts = 5
)

// DeclineNoPaymentMethod is the decline code of an attempt made without a
// card to charge: the issuance was paid by PSE, which cannot be debited again
// without the insured.
const DeclineNoPaymentMethod = "no_payment_method"

// Config is what the sweeper needs. Notifier may be nil: then the e-mails
// wait until one is configured.
type Config struct {
	Store    ports.InstallmentStore
	Methods  ports.PaymentMethodReader
	Gateway  ports.InstallmentGateway
	Notifier ports.CancellationNotifier
	Rules    domain.InstallmentRules
	Clock    ports.Clock
	Lease    time.Duration
}

// Sweeper bills, charges and cancels.
type Sweeper struct {
	cfg Config
}

// New builds a Sweeper.
func New(cfg Config) (*Sweeper, error) {
	switch {
	case cfg.Store == nil || cfg.Methods == nil || cfg.Gateway == nil || cfg.Clock == nil:
		return nil, errors.New("installments: a store, a payment method reader, a gateway and a clock are required")
	}
	if err := cfg.Rules.Validate(); err != nil {
		return nil, err
	}
	if cfg.Lease <= 0 {
		cfg.Lease = DefaultLease
	}
	return &Sweeper{cfg: cfg}, nil
}

// Result counts what a sweep did.
type Result struct {
	Billed, Paid, Declined, Unknown, Cancelled, Emailed int
}

// Sweep runs the four steps. A failure of one step is returned after the
// others ran: the next sweep tries again.
func (s *Sweeper) Sweep(ctx context.Context) (Result, error) {
	var r Result
	billed, errBill := s.bill(ctx)
	r.Billed = billed
	errCharge := s.charge(ctx, &r)
	cancelled, errCancel := s.cancel(ctx)
	r.Cancelled = cancelled
	emailed, errEmail := s.email(ctx)
	r.Emailed = emailed
	return r, errors.Join(errBill, errCharge, errCancel, errEmail)
}

// bill creates the instalments that fell due since the billing started. A
// policy with a month pending is not billed the next one until it is paid:
// its grace period is about that month.
func (s *Sweeper) bill(ctx context.Context) (int, error) {
	now := s.cfg.Clock.Now().UTC()
	since, err := s.cfg.Store.BillingStart(ctx)
	if err != nil {
		return 0, err
	}
	policies, err := s.cfg.Store.ListBillable(ctx, now)
	if err != nil {
		return 0, err
	}
	billed := 0
	var errs []error
	for _, p := range policies {
		for _, seq := range s.cfg.Rules.DueInstallments(p, since, now) {
			created, err := s.cfg.Store.CreateInstallment(ctx, domain.NewInstallment(p, seq, s.cfg.Rules, now))
			if err != nil {
				errs = append(errs, fmt.Errorf("bill month %d of policy %s: %w", seq, p.ID, err))
				continue
			}
			if created {
				billed++
				slog.InfoContext(ctx, "instalment billed", "policy_id", p.ID, "seq", seq)
			}
		}
	}
	return billed, errors.Join(errs...)
}

// charge makes the attempts that are due.
func (s *Sweeper) charge(ctx context.Context, r *Result) error {
	now := s.cfg.Clock.Now().UTC()
	due, err := s.cfg.Store.ClaimDueAttempts(ctx, now, s.cfg.Lease, batch)
	if err != nil {
		return err
	}
	var errs []error
	for _, d := range due {
		outcome, err := s.attempt(ctx, d.Policy, d.Installment)
		switch outcome {
		case domain.AttemptApproved:
			r.Paid++
		case domain.AttemptDeclined:
			r.Declined++
		default:
			r.Unknown++
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// attempt charges one instalment the sweep holds and records the answer.
func (s *Sweeper) attempt(ctx context.Context, p domain.Policy, i domain.Installment) (domain.AttemptOutcome, error) {
	key := i.IdempotencyKey()
	method, err := s.cfg.Methods.PaymentMethodOf(ctx, p.AcceptanceID)
	if err != nil {
		return "", s.unknown(ctx, i, err)
	}
	if method.Token == "" {
		return domain.AttemptDeclined, s.declined(ctx, i, key, DeclineNoPaymentMethod, "")
	}
	ref, err := s.cfg.Gateway.ChargeInstallment(ctx, p, i, key, method)
	now := s.cfg.Clock.Now().UTC()
	switch {
	case err == nil:
		a := domain.InstallmentAttempt{Attempt: i.Attempts + 1, IdempotencyKey: key, Outcome: domain.AttemptApproved,
			ProviderReference: ref, AttemptedAt: now}
		if err := s.cfg.Store.RecordApproved(ctx, i, a); err != nil {
			return "", fmt.Errorf("record the payment of month %d of policy %s: %w", i.Seq, i.PolicyID, err)
		}
		slog.InfoContext(ctx, "instalment paid", "policy_id", i.PolicyID, "seq", i.Seq, "attempt", a.Attempt)
		return domain.AttemptApproved, nil
	case errors.Is(err, idempotency.ErrRejected):
		code, ref := "", ""
		var rejected *payments.RejectedError
		if errors.As(err, &rejected) {
			code, ref = rejected.DeclineCode, rejected.Reference
		}
		return domain.AttemptDeclined, s.declined(ctx, i, key, code, ref)
	default:
		return "", s.unknown(ctx, i, err)
	}
}

// declined records a declined attempt and schedules the next one, if any.
func (s *Sweeper) declined(ctx context.Context, i domain.Installment, key, code, ref string) error {
	now := s.cfg.Clock.Now().UTC()
	a := domain.InstallmentAttempt{Attempt: i.Attempts + 1, IdempotencyKey: key, Outcome: domain.AttemptDeclined,
		DeclineCode: code, ProviderReference: ref, AttemptedAt: now}
	next, _ := i.AfterDecline(s.cfg.Rules)
	if err := s.cfg.Store.RecordDeclined(ctx, i, a, next); err != nil {
		return fmt.Errorf("record the declined attempt %d of month %d of policy %s: %w", a.Attempt, i.Seq, i.PolicyID, err)
	}
	slog.WarnContext(ctx, "instalment declined", "policy_id", i.PolicyID, "seq", i.Seq, "attempt", a.Attempt,
		"decline_code", code, "last", next.IsZero())
	return nil
}

// unknown releases an instalment whose attempt has no known outcome, to try
// again with the same key.
func (s *Sweeper) unknown(ctx context.Context, i domain.Installment, cause error) error {
	next := s.cfg.Clock.Now().UTC().Add(s.cfg.Rules.RetryUnknownAfter)
	slog.WarnContext(ctx, "the outcome of an instalment attempt is unknown; it is tried again with the same key",
		"policy_id", i.PolicyID, "seq", i.Seq, "error", cause)
	if err := s.cfg.Store.RescheduleUnknown(ctx, i, next); err != nil {
		return fmt.Errorf("reschedule month %d of policy %s: %w", i.Seq, i.PolicyID, err)
	}
	return nil
}

// cancel cancels the policies whose grace period ended unpaid.
func (s *Sweeper) cancel(ctx context.Context) (int, error) {
	now := s.cfg.Clock.Now().UTC()
	overdue, err := s.cfg.Store.ListOverdue(ctx, now, batch)
	if err != nil {
		return 0, err
	}
	cancelled := 0
	var errs []error
	for _, o := range overdue {
		created, err := s.cfg.Store.Cancel(ctx, o.Policy, o.Installment, now)
		if err != nil {
			errs = append(errs, fmt.Errorf("cancel policy %s: %w", o.Policy.ID, err))
			continue
		}
		if created {
			cancelled++
			slog.WarnContext(ctx, "policy cancelled for non-payment, with no refund", "policy_id", o.Policy.ID,
				"seq", o.Installment.Seq, "attempts", o.Installment.Attempts)
		}
	}
	return cancelled, errors.Join(errs...)
}

// email sends the e-mail of the cancellations that have not got theirs.
func (s *Sweeper) email(ctx context.Context) (int, error) {
	if s.cfg.Notifier == nil {
		return 0, nil
	}
	pending, err := s.cfg.Store.ListUnnotified(ctx, maxEmailAttempts, batch)
	if err != nil {
		return 0, err
	}
	sent := 0
	var errs []error
	for _, n := range pending {
		if err := s.cfg.Notifier.NotifyCancellation(ctx, n); err != nil {
			slog.ErrorContext(ctx, "the e-mail of a cancellation for non-payment did not go out", "policy_id",
				n.Policy.ID, "error", err)
			errs = append(errs, s.cfg.Store.CountEmailFailure(ctx, n.Policy.ID))
			continue
		}
		if err := s.cfg.Store.MarkEmailed(ctx, n.Policy.ID, s.cfg.Clock.Now().UTC()); err != nil {
			errs = append(errs, err)
			continue
		}
		sent++
	}
	return sent, errors.Join(errs...)
}

// Run sweeps every interval until ctx ends.
func (s *Sweeper) Run(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = DefaultInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		if _, err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
			slog.ErrorContext(ctx, "instalment sweep failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
