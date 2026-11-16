package installments_test

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/installments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// HU-W1.6-02: the sweep that bills, charges and cancels for non-payment.

var (
	rules  = domain.DefaultInstallmentRules
	issued = time.Date(2026, 9, 16, 19, 6, 0, 0, time.UTC)
)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

// store is an InstallmentStore in memory, with the rules of PostgreSQL that
// the sweep relies on.
type store struct {
	since        time.Time
	policies     map[string]domain.Policy
	installments map[string]*domain.Installment
	cancelled    map[string]*domain.PolicyCancellation
	emailFails   map[string]int
	failList     error
}

func newStore(policies ...domain.Policy) *store {
	s := &store{policies: map[string]domain.Policy{}, installments: map[string]*domain.Installment{},
		cancelled: map[string]*domain.PolicyCancellation{}, emailFails: map[string]int{}}
	for _, p := range policies {
		s.policies[p.ID] = p
	}
	return s
}

func key(id string, seq int) string { return fmt.Sprintf("%s/%d", id, seq) }

func (s *store) BillingStart(context.Context) (time.Time, error) { return s.since, nil }

func (s *store) ListBillable(_ context.Context, now time.Time) ([]domain.Policy, error) {
	if s.failList != nil {
		return nil, s.failList
	}
	var out []domain.Policy
	for _, p := range s.policies {
		if s.cancelled[p.ID] == nil && !p.CoverageStart.After(now) && p.CoverageEnd.After(now) && !s.pending(p.ID) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].ID < out[b].ID })
	return out, nil
}

func (s *store) pending(policyID string) bool {
	for _, i := range s.installments {
		if i.PolicyID == policyID && i.Status == domain.InstallmentPending {
			return true
		}
	}
	return false
}

func (s *store) CreateInstallment(_ context.Context, i domain.Installment) (bool, error) {
	if s.installments[key(i.PolicyID, i.Seq)] != nil {
		return false, nil
	}
	s.installments[key(i.PolicyID, i.Seq)] = &i
	return true, nil
}

func (s *store) sorted() []*domain.Installment {
	var out []*domain.Installment
	for _, i := range s.installments {
		out = append(out, i)
	}
	sort.Slice(out, func(a, b int) bool { return key(out[a].PolicyID, out[a].Seq) < key(out[b].PolicyID, out[b].Seq) })
	return out
}

func (s *store) ClaimDueAttempts(_ context.Context, now time.Time, _ time.Duration, _ int) ([]ports.PolicyInstallment, error) {
	var out []ports.PolicyInstallment
	for _, i := range s.sorted() {
		if i.Status == domain.InstallmentPending && !i.NextAttemptAt.IsZero() && !i.NextAttemptAt.After(now) {
			out = append(out, ports.PolicyInstallment{Policy: s.policies[i.PolicyID], Installment: *i})
		}
	}
	return out, nil
}

func (s *store) RecordApproved(_ context.Context, i domain.Installment, a domain.InstallmentAttempt) error {
	got := s.installments[key(i.PolicyID, i.Seq)]
	got.Status, got.PaidAt, got.NextAttemptAt = domain.InstallmentPaid, a.AttemptedAt, time.Time{}
	got.History = append(got.History, a)
	return nil
}

func (s *store) RecordDeclined(_ context.Context, i domain.Installment, a domain.InstallmentAttempt, next time.Time) error {
	got := s.installments[key(i.PolicyID, i.Seq)]
	got.Attempts++
	got.NextAttemptAt = next
	got.History = append(got.History, a)
	return nil
}

func (s *store) RescheduleUnknown(_ context.Context, i domain.Installment, next time.Time) error {
	s.installments[key(i.PolicyID, i.Seq)].NextAttemptAt = next
	return nil
}

func (s *store) ListOverdue(_ context.Context, now time.Time, _ int) ([]ports.PolicyInstallment, error) {
	var out []ports.PolicyInstallment
	for _, i := range s.sorted() {
		if i.Status == domain.InstallmentPending && !i.CancelsAt.After(now) {
			out = append(out, ports.PolicyInstallment{Policy: s.policies[i.PolicyID], Installment: *i})
		}
	}
	return out, nil
}

func (s *store) Cancel(_ context.Context, p domain.Policy, i domain.Installment, now time.Time) (bool, error) {
	if s.cancelled[p.ID] != nil {
		return false, nil
	}
	s.installments[key(i.PolicyID, i.Seq)].Status = domain.InstallmentUnpaid
	s.cancelled[p.ID] = &domain.PolicyCancellation{PolicyID: p.ID, Reason: domain.CancellationNonPayment,
		Installment: i, CancelledAt: now, EventID: domain.PolicyCancelledForNonPaymentID(p.ID)}
	return true, nil
}

func (s *store) ListUnnotified(_ context.Context, maxAttempts, _ int) ([]ports.CancellationNotice, error) {
	var out []ports.CancellationNotice
	for id, c := range s.cancelled {
		if c.EmailSentAt.IsZero() && s.emailFails[id] < maxAttempts {
			out = append(out, ports.CancellationNotice{Policy: s.policies[id], Cancellation: *c})
		}
	}
	return out, nil
}

func (s *store) MarkEmailed(_ context.Context, id string, at time.Time) error {
	s.cancelled[id].EmailSentAt = at
	return nil
}

func (s *store) CountEmailFailure(_ context.Context, id string) error {
	s.emailFails[id]++
	return nil
}

func (s *store) Billing(context.Context, []string) (map[string]ports.BillingRecord, error) {
	return nil, nil
}

type methods map[string]domain.PaymentMethod

func (m methods) PaymentMethodOf(_ context.Context, acceptanceID string) (domain.PaymentMethod, error) {
	return m[acceptanceID], nil
}

// gateway answers each call with the next error of its script (nil approves).
type gateway struct {
	script []error
	keys   []string
}

func (g *gateway) ChargeInstallment(_ context.Context, _ domain.Policy, _ domain.Installment, key string,
	_ domain.PaymentMethod) (string, error) {
	g.keys = append(g.keys, key)
	var err error
	if len(g.script) > 0 {
		err, g.script = g.script[0], g.script[1:]
	}
	if err != nil {
		return "", err
	}
	return "ch_" + key, nil
}

type notifier struct {
	sent []ports.CancellationNotice
	err  error
}

func (n *notifier) NotifyCancellation(_ context.Context, in ports.CancellationNotice) error {
	if n.err != nil {
		return n.err
	}
	n.sent = append(n.sent, in)
	return nil
}

func policy() domain.Policy {
	return domain.Policy{ID: "VH-2026-004512", AcceptanceID: "acc-1", CustomerID: "sub-carolina", Premium: 68_500,
		Currency: "COP", CoverageStart: issued, CoverageEnd: issued.AddDate(1, 0, 0),
		Holder: domain.Applicant{FullName: "Carolina Pérez", Email: "carolina@example.com"}}
}

var card = domain.PaymentMethod{Kind: domain.PaymentCard, Token: "tok_1", Brand: "visa", Last4: "0010"}

type fixture struct {
	clock    *clock
	store    *store
	gateway  *gateway
	notifier *notifier
	sweeper  *installments.Sweeper
}

func newFixture(t *testing.T, m methods) fixture {
	t.Helper()
	f := fixture{clock: &clock{now: issued}, store: newStore(policy()), gateway: &gateway{}, notifier: &notifier{}}
	f.store.since = issued.Add(-time.Hour)
	var err error
	f.sweeper, err = installments.New(installments.Config{Store: f.store, Methods: m, Gateway: f.gateway,
		Notifier: f.notifier, Rules: rules, Clock: f.clock})
	require.NoError(t, err)
	return f
}

func (f fixture) sweepAt(t *testing.T, at time.Time) installments.Result {
	t.Helper()
	f.clock.now = at
	r, err := f.sweeper.Sweep(context.Background())
	require.NoError(t, err)
	return r
}

func month2() time.Time { return rules.DueAt(policy(), 2) }

func attempt(n int) time.Time {
	at, _ := rules.AttemptAt(month2(), n)
	return at
}

func declined(code string) error {
	return &payments.RejectedError{StatusCode: 402, Code: "payment_declined", DeclineCode: code, Reference: "ch_x"}
}

func TestAMonthThatFallsDueIsBilledAndChargedOnDayOneOfTheGrace(t *testing.T) {
	f := newFixture(t, methods{"acc-1": card})

	assert.Zero(t, f.sweepAt(t, month2().Add(-time.Minute)).Billed, "not due yet")
	r := f.sweepAt(t, month2())
	assert.Equal(t, 1, r.Billed)
	assert.Zero(t, r.Paid, "the first charge is on day 1 of the grace")
	assert.Zero(t, f.sweepAt(t, month2().Add(time.Hour)).Billed, "billed once")

	r = f.sweepAt(t, attempt(1))

	assert.Equal(t, 1, r.Paid)
	assert.Equal(t, []string{"cuota-VH-2026-004512-2-1"}, f.gateway.keys)
	got := f.store.installments[key("VH-2026-004512", 2)]
	assert.Equal(t, domain.InstallmentPaid, got.Status)
	assert.Equal(t, int64(6_850_000), got.AmountMinor)
}

func TestThreeDeclinesAndTheEndOfTheGraceCancelThePolicyAndEmailTheInsured(t *testing.T) {
	f := newFixture(t, methods{"acc-1": card})
	f.gateway.script = []error{declined("51"), declined("51"), declined("51")}
	f.sweepAt(t, month2())

	for n := 1; n <= 3; n++ {
		assert.Equal(t, 1, f.sweepAt(t, attempt(n)).Declined, "attempt %d", n)
	}
	assert.Equal(t, []string{"cuota-VH-2026-004512-2-1", "cuota-VH-2026-004512-2-2", "cuota-VH-2026-004512-2-3"},
		f.gateway.keys, "a key per attempt: the gateway replays a declined key")
	got := f.store.installments[key("VH-2026-004512", 2)]
	require.Len(t, got.History, 3)
	assert.Equal(t, "51", got.History[2].DeclineCode)
	assert.True(t, got.NextAttemptAt.IsZero())

	assert.Zero(t, f.sweepAt(t, rules.GraceEndsAt(month2())).Cancelled, "the policy keeps its coverage during the grace")
	r := f.sweepAt(t, rules.CancelsAt(month2()))

	assert.Equal(t, 1, r.Cancelled)
	assert.Equal(t, 1, r.Emailed)
	require.Len(t, f.notifier.sent, 1)
	assert.Equal(t, "policy-cancelled-non-payment-VH-2026-004512", f.notifier.sent[0].Cancellation.EventID)
	assert.Zero(t, f.sweepAt(t, rules.CancelsAt(month2()).Add(time.Hour)).Emailed, "e-mailed once")
	assert.Zero(t, f.sweepAt(t, rules.DueAt(policy(), 3)).Billed, "a cancelled policy is not billed again")
}

func TestAPaymentOnTheSecondAttemptStopsTheCancellation(t *testing.T) {
	f := newFixture(t, methods{"acc-1": card})
	f.gateway.script = []error{declined("51"), nil}
	f.sweepAt(t, month2())
	f.sweepAt(t, attempt(1))

	assert.Equal(t, 1, f.sweepAt(t, attempt(2)).Paid)
	assert.Zero(t, f.sweepAt(t, rules.CancelsAt(month2())).Cancelled)
	assert.Len(t, f.gateway.keys, 2)
}

func TestAnUnknownOutcomeIsTriedAgainWithTheSameKeyAndNotCounted(t *testing.T) {
	f := newFixture(t, methods{"acc-1": card})
	f.gateway.script = []error{fmt.Errorf("timeout: %w", idempotency.ErrAmbiguous), nil}
	f.sweepAt(t, month2())

	assert.Equal(t, 1, f.sweepAt(t, attempt(1)).Unknown)
	assert.Zero(t, f.sweepAt(t, attempt(1).Add(time.Minute)).Paid, "it waits the retry of an unknown outcome")
	assert.Equal(t, 1, f.sweepAt(t, attempt(1).Add(rules.RetryUnknownAfter)).Paid)

	assert.Equal(t, []string{"cuota-VH-2026-004512-2-1", "cuota-VH-2026-004512-2-1"}, f.gateway.keys)
	assert.Zero(t, f.store.installments[key("VH-2026-004512", 2)].Attempts)
}

func TestWithoutACardTheAttemptIsDeclinedWithoutCallingTheGateway(t *testing.T) {
	f := newFixture(t, methods{"acc-1": {Kind: domain.PaymentPSE, BankCode: "1007"}})
	f.sweepAt(t, month2())

	assert.Equal(t, 1, f.sweepAt(t, attempt(1)).Declined)

	assert.Empty(t, f.gateway.keys, "PSE cannot be debited again without the insured")
	got := f.store.installments[key("VH-2026-004512", 2)]
	assert.Equal(t, installments.DeclineNoPaymentMethod, got.History[0].DeclineCode)
}

func TestAFailedEmailIsCountedAndTriedAgain(t *testing.T) {
	f := newFixture(t, methods{"acc-1": card})
	f.gateway.script = []error{declined("51"), declined("51"), declined("51")}
	f.notifier.err = errors.New("provider down")
	f.sweepAt(t, month2())
	for n := 1; n <= 3; n++ {
		f.sweepAt(t, attempt(n))
	}

	f.clock.now = rules.CancelsAt(month2())
	r, err := f.sweeper.Sweep(context.Background())

	require.NoError(t, err, "a failed e-mail is counted, not an error of the sweep")
	assert.Equal(t, 1, r.Cancelled)
	assert.Zero(t, r.Emailed)
	assert.Equal(t, 1, f.store.emailFails["VH-2026-004512"])
	f.notifier.err = nil
	assert.Equal(t, 1, f.sweepAt(t, rules.CancelsAt(month2()).Add(time.Hour)).Emailed)
}

func TestAFailureToListThePoliciesDoesNotStopTheOtherSteps(t *testing.T) {
	f := newFixture(t, methods{"acc-1": card})
	f.sweepAt(t, month2())
	f.store.failList = errors.New("database down")
	f.clock.now = attempt(1)

	r, err := f.sweeper.Sweep(context.Background())

	assert.Error(t, err)
	assert.Equal(t, 1, r.Paid, "the instalments already billed are still charged")
}

func TestTheSweeperNeedsItsDependenciesAndValidRules(t *testing.T) {
	_, err := installments.New(installments.Config{})
	assert.Error(t, err)
	bad := rules
	bad.GraceDays = 0
	_, err = installments.New(installments.Config{Store: newStore(), Methods: methods{}, Gateway: &gateway{},
		Clock: &clock{}, Rules: bad})
	assert.ErrorIs(t, err, domain.ErrInvalidInstallmentRules)
	_, err = installments.New(installments.Config{Store: newStore(), Methods: methods{}, Gateway: &gateway{},
		Clock: &clock{}, Rules: rules})
	assert.NoError(t, err, "without a notifier the e-mails wait")
}

func TestAMonthPendingHoldsTheNextOneWhileItsGraceLasts(t *testing.T) {
	f := newFixture(t, methods{"acc-1": card})
	f.gateway.script = []error{declined("51"), declined("51"), declined("51")}
	f.sweepAt(t, month2())
	for n := 1; n <= 3; n++ {
		f.sweepAt(t, attempt(n))
	}
	month3 := rules.DueAt(policy(), 3)
	require.False(t, month3.After(rules.CancelsAt(month2())), "month 3 falls due before month 2 cancels the policy")

	assert.Zero(t, f.sweepAt(t, month3).Billed, "the grace period is about month 2: month 3 is not charged too")
	assert.Nil(t, f.store.installments[key("VH-2026-004512", 3)])
	f.sweepAt(t, rules.CancelsAt(month2()))
	assert.NotNil(t, f.store.cancelled["VH-2026-004512"])
	assert.Nil(t, f.store.installments[key("VH-2026-004512", 3)], "nor after the cancellation")
}

func TestAMonthPaidLateLetsTheNextOneBeBilled(t *testing.T) {
	f := newFixture(t, methods{"acc-1": card})
	f.gateway.script = []error{declined("51"), declined("51"), nil}
	f.sweepAt(t, month2())
	f.sweepAt(t, attempt(1))
	f.sweepAt(t, attempt(2))
	assert.Equal(t, 1, f.sweepAt(t, attempt(3)).Paid, "month 2 paid on the third attempt")

	assert.Equal(t, 1, f.sweepAt(t, rules.DueAt(policy(), 3)).Billed, "then month 3 is billed when it falls due")
}
