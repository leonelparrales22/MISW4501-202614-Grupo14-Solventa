package cancellation_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/cancellation"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// HU-W1.6-01: who can quote and cancel a policy, and what is written.

var (
	colombia = domain.ColombiaTime
	start    = time.Date(2026, 11, 18, 10, 0, 0, 0, colombia)
	// now is the day of the purchase, five hours later.
	now = start.Add(5 * time.Hour)
)

type clock struct{ t time.Time }

func (c clock) Now() time.Time { return c.t }

// store is a ports.CancellationStore over one policy's facts.
type store struct {
	facts   ports.CancellationFacts
	written []domain.InsuredCancellation
}

func (s *store) Facts(_ context.Context, policyID string, _ time.Time) (ports.CancellationFacts, error) {
	if policyID != s.facts.Policy.ID {
		return ports.CancellationFacts{}, ports.ErrPolicyNotFound
	}
	return s.facts, nil
}

func (s *store) Cancel(_ context.Context, policyID string, _ time.Time,
	decide ports.CancellationDecision) (domain.PolicyCancellation, error) {
	if policyID != s.facts.Policy.ID {
		return domain.PolicyCancellation{}, ports.ErrPolicyNotFound
	}
	c, err := decide(s.facts)
	if err != nil {
		return domain.PolicyCancellation{}, err
	}
	s.written = append(s.written, c)
	r := c.Refund
	if r.AmountMinor > 0 {
		r.Reference = "REF-2026-00001"
	}
	return domain.PolicyCancellation{PolicyID: policyID, Reason: domain.CancellationInsuredRequest,
		CancelledAt: c.CancelledAt, EffectiveAt: c.EffectiveAt, InsuredReason: c.Reason, Refund: &r}, nil
}

type waker struct{ woken int }

func (w *waker) Notify() { w.woken++ }

func facts() ports.CancellationFacts {
	return ports.CancellationFacts{
		Policy: domain.Policy{ID: "VH-2026-000101", AcceptanceID: "acc-1", CustomerID: "sub-carolina", Premium: 142_300,
			Currency: "COP", CoverageStart: start.UTC(), CoverageEnd: start.AddDate(1, 0, 0).UTC()},
		Issuance: &domain.Charge{AcceptanceID: "acc-1", Status: domain.ChargeConfirmed, AmountMinor: 14_230_000,
			Currency: "COP", ProviderReference: "ch-1",
			PaymentMethod: domain.PaymentMethod{Kind: domain.PaymentCard, Token: "tok_1", Brand: "visa", Last4: "1111"}},
	}
}

func newService(t *testing.T, f ports.CancellationFacts, at time.Time) (*cancellation.Service, *store, *waker) {
	t.Helper()
	s, w := &store{facts: f}, &waker{}
	svc, err := cancellation.NewService(cancellation.Config{Store: s, Rules: domain.DefaultInstallmentRules,
		Clock: clock{at}, RetryWindow: 24 * time.Hour, Refunds: w})
	require.NoError(t, err)
	return svc, s, w
}

func as(sub string, role security.Role) context.Context {
	return security.WithClaims(context.Background(), security.Claims{Subject: sub, Roles: []security.Role{role}})
}

func confirm(amount int64) domain.CancellationRequest {
	return domain.CancellationRequest{Reason: domain.InsuredReasonPropertySold, RefundAmountMinor: amount, AcceptTerms: true}
}

func TestTheOwnerSeesTheRefundBeforeConfirming(t *testing.T) {
	svc, _, _ := newService(t, facts(), now)

	q, err := svc.Quote(as("sub-carolina", security.RoleInsured), "VH-2026-000101")

	require.NoError(t, err)
	assert.Equal(t, int64(13_755_600), q.RefundAmountMinor, "CA-1: 142.300 x 29/30")
	assert.Equal(t, 29, q.UnusedDays)
	assert.Equal(t, 30, q.PeriodDays)
	assert.Equal(t, domain.PaidByIssuance, q.Paid.Kind)
	assert.Equal(t, domain.PaymentMethod{Kind: domain.PaymentCard, Brand: "visa", Last4: "1111"}, q.Method)
	assert.Equal(t, domain.EndOfDayColombia(now), q.EffectiveAt)
	assert.Equal(t, domain.PaymentUpToDate, q.PaymentStatus)
}

func TestOnlyTheOwnerWithTheInsuredRoleCanCancel(t *testing.T) {
	svc, s, _ := newService(t, facts(), now)

	_, err := svc.Quote(as("sub-otra", security.RoleInsured), "VH-2026-000101")
	assert.ErrorIs(t, err, ports.ErrPolicyNotFound, "another insured's policy is not said to exist")
	_, err = svc.Quote(as("sub-carolina", security.RoleInsured), "AU-2026-000001")
	assert.ErrorIs(t, err, ports.ErrPolicyNotFound, "a policy of the core or an unknown one")
	_, err = svc.Quote(as("sub-carolina", security.RoleAdvisor), "VH-2026-000101")
	assert.ErrorIs(t, err, security.ErrForbidden)
	_, err = svc.Cancel(as("sub-otra", security.RoleInsured), "VH-2026-000101", confirm(13_755_600))
	assert.ErrorIs(t, err, ports.ErrPolicyNotFound)
	_, err = svc.Cancel(context.Background(), "VH-2026-000101", confirm(13_755_600))
	assert.ErrorIs(t, err, security.ErrUnauthenticated)
	assert.Empty(t, s.written)
}

func TestOnlyAPolicyInForceWithNoRenewalUnderWayCanBeCancelled(t *testing.T) {
	insured := as("sub-carolina", security.RoleInsured)
	for name, tc := range map[string]struct {
		at     time.Time
		mutate func(*ports.CancellationFacts)
		want   error
	}{
		"ended":             {at: start.AddDate(1, 0, 1), want: domain.ErrPolicyNotInForce},
		"not started yet":   {at: start.Add(-time.Hour), want: domain.ErrPolicyNotInForce},
		"already cancelled": {at: now, mutate: func(f *ports.CancellationFacts) { f.Cancellation = &domain.PolicyCancellation{} }, want: ports.ErrPolicyAlreadyCancelled},
		"renewal accepted":  {at: now, mutate: func(f *ports.CancellationFacts) { f.Renewal = domain.RenewalAccepted }, want: ports.ErrRenewalInProgress},
		"renewed":           {at: now, mutate: func(f *ports.CancellationFacts) { f.Renewal = domain.RenewalRenewed }, want: ports.ErrRenewalInProgress},
		"not renewed":       {at: now, mutate: func(f *ports.CancellationFacts) { f.Renewal = domain.RenewalNotRenewed }},
	} {
		t.Run(name, func(t *testing.T) {
			f := facts()
			if tc.mutate != nil {
				tc.mutate(&f)
			}
			svc, s, _ := newService(t, f, tc.at)

			_, quoteErr := svc.Quote(insured, f.Policy.ID)
			q, _ := svc.Quote(insured, f.Policy.ID)
			_, cancelErr := svc.Cancel(insured, f.Policy.ID, confirm(q.RefundAmountMinor))

			if tc.want == nil {
				assert.NoError(t, quoteErr)
				assert.NoError(t, cancelErr)
				return
			}
			assert.ErrorIs(t, quoteErr, tc.want)
			assert.ErrorIs(t, cancelErr, tc.want)
			assert.Empty(t, s.written)
		})
	}
}

func TestTheConfirmedRefundIsTheOneRegistered(t *testing.T) {
	insured := as("sub-carolina", security.RoleInsured)
	svc, s, w := newService(t, facts(), now)

	c, err := svc.Cancel(insured, "VH-2026-000101", domain.CancellationRequest{Reason: domain.InsuredReasonMortgagePaid,
		Comments: "pagué el crédito", RefundAmountMinor: 13_755_600, AcceptTerms: true})

	require.NoError(t, err)
	require.Len(t, s.written, 1)
	got := s.written[0]
	assert.Equal(t, "sub-carolina", got.RequestedBy)
	assert.Equal(t, domain.InsuredReasonMortgagePaid, got.Reason)
	assert.Equal(t, now.UTC(), got.CancelledAt)
	assert.Equal(t, int64(13_755_600), got.Refund.AmountMinor)
	assert.Equal(t, domain.RefundInProcess, got.Refund.Status)
	assert.Equal(t, now.Add(24*time.Hour).UTC(), got.Refund.RetryUntil, "retried within the window of the charge")
	assert.Equal(t, "REF-2026-00001", c.Refund.Reference)
	assert.Equal(t, 1, w.woken, "the refund is sent at once")
}

func TestARefundThatChangedIsNotRegisteredAndComesBack(t *testing.T) {
	insured := as("sub-carolina", security.RoleInsured)
	// The insured saw the quote of the day of the purchase and confirms the
	// next day, when one more day was used.
	svc, s, _ := newService(t, facts(), now.Add(24*time.Hour))

	_, err := svc.Cancel(insured, "VH-2026-000101", confirm(13_755_600))

	var changed *cancellation.RefundChangedError
	require.ErrorAs(t, err, &changed)
	assert.ErrorIs(t, err, domain.ErrRefundChanged)
	assert.Equal(t, int64(13_281_300), changed.Quote.RefundAmountMinor, "142.300 x 28/30, the new amount to show")
	assert.Empty(t, s.written)
}

func TestNothingIsWrittenWhileTheSweepChargesTheMonthOrForAnInvalidRequest(t *testing.T) {
	insured := as("sub-carolina", security.RoleInsured)
	f := facts()
	f.InstallmentHeld = true
	svc, s, _ := newService(t, f, now)

	_, err := svc.Cancel(insured, f.Policy.ID, confirm(13_755_600))
	assert.ErrorIs(t, err, ports.ErrPaymentInProgress)

	svc, s2, _ := newService(t, facts(), now)
	_, err = svc.Cancel(insured, f.Policy.ID, domain.CancellationRequest{Reason: domain.InsuredReasonOther,
		RefundAmountMinor: 13_755_600})
	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.Equal(t, []domain.FieldError{{Field: "accept_terms", Code: "required"}}, invalid.Fields)
	assert.Empty(t, s.written)
	assert.Empty(t, s2.written)
}

func TestAMonthInItsGraceIsCancelledWithNothingToRefundAndNothingToSend(t *testing.T) {
	insured := as("sub-carolina", security.RoleInsured)
	f := facts()
	f.Installments = []domain.Installment{{PolicyID: f.Policy.ID, Seq: 2, AmountMinor: 14_230_000, Currency: "COP",
		Status: domain.InstallmentPending}}
	svc, s, w := newService(t, f, start.AddDate(0, 1, 3))

	q, err := svc.Quote(insured, f.Policy.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.PaymentPending, q.PaymentStatus)
	assert.Zero(t, q.RefundAmountMinor, "decision D-6 a")

	c, err := svc.Cancel(insured, f.Policy.ID, confirm(0))

	require.NoError(t, err)
	assert.Equal(t, domain.RefundStatusNotApplicable, c.Refund.Status)
	assert.Len(t, s.written, 1)
	assert.Zero(t, w.woken, "nothing goes to the gateway")
}
