package billing_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/billing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

func as(role security.Role, subject string) context.Context {
	return security.WithClaims(context.Background(), security.Claims{Subject: subject, Roles: []security.Role{role}})
}

// failedCharge es un cobro al que se le acabo la ventana tras nueve intentos,
// asi que necesariamente tenia medio de pago: sin el no se habria llamado a la
// pasarela ni una vez (HU-W1.3-02).
func failedCharge(id string) domain.Charge {
	c := domain.NewCharge(acceptance(id), offer(id), domain.CoverageTerms{TermMonths: 120}, t0, t0.Add(time.Hour))
	c.Status, c.FailureReason, c.Attempts, c.LastFailureAt = domain.ChargeFailed, domain.ChargeRetryWindowExpired, 9, t0.Add(time.Hour)
	c.PaymentMethod = domain.PaymentMethod{
		Kind: domain.PaymentCard, Token: "tok_7b3c1f9a", Brand: "visa", Last4: "1111", Installments: 1, CapturedAt: t0,
	}
	c.PaymentAttempt = 1
	return c
}

// expiredWithoutPaymentMethod es el cobro que nunca llego a tener tarjeta: la
// ventana se acabo esperandola y el barrido lo dejo como excepcion.
func expiredWithoutPaymentMethod(id string) domain.Charge {
	c := domain.NewCharge(acceptance(id), offer(id), domain.CoverageTerms{TermMonths: 120}, t0, t0.Add(time.Hour))
	c.Status, c.FailureReason, c.LastFailureAt = domain.ChargeFailed, domain.ChargePaymentMethodMissing, t0.Add(time.Hour)
	return c
}

func TestGetIsForTheOwnerAnAdvisorOrOperations(t *testing.T) {
	repo := newMemRepo()
	repo.put(failedCharge("acc-1"))
	svc := billing.NewService(repo, &clock{now: t0}, resilience.DefaultRetryPolicy(), nil)

	for _, ctx := range []context.Context{
		as(security.RoleInsured, "cust-1"), as(security.RoleAdvisor, "adv-1"), as(security.RoleOperations, "ops-1"),
	} {
		c, err := svc.Get(ctx, "acc-1")
		require.NoError(t, err)
		assert.Equal(t, "acc-1", c.AcceptanceID)
	}

	_, err := svc.Get(as(security.RoleInsured, "someone-else"), "acc-1")
	assert.ErrorIs(t, err, security.ErrForbidden)
	_, err = svc.Get(context.Background(), "acc-1")
	assert.ErrorIs(t, err, security.ErrUnauthenticated)
	_, err = svc.Get(as(security.RoleInsured, "cust-1"), "acc-404")
	assert.ErrorIs(t, err, ports.ErrChargeNotFound)
}

func TestListExceptionsIsForOperations(t *testing.T) {
	repo := newMemRepo()
	repo.put(failedCharge("acc-1"))
	repo.put(domain.NewCharge(acceptance("acc-2"), offer("acc-2"), domain.CoverageTerms{}, t0, time.Time{}))
	svc := billing.NewService(repo, &clock{now: t0}, resilience.DefaultRetryPolicy(), nil)

	got, err := svc.ListExceptions(as(security.RoleOperations, "ops-1"), 50)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "acc-1", got[0].AcceptanceID)

	for _, role := range []security.Role{security.RoleInsured, security.RoleAdvisor} {
		_, err := svc.ListExceptions(as(role, "x"), 50)
		assert.ErrorIs(t, err, security.ErrForbidden, role)
	}
	for _, limit := range []int{0, 201, -1} {
		_, err := svc.ListExceptions(as(security.RoleOperations, "ops-1"), limit)
		assert.ErrorIs(t, err, billing.ErrInvalidLimit)
	}
}

// Reanudar un cobro que nunca tuvo medio de pago no puede dejarlo listo para
// cobrarse: el worker llamaria a la pasarela sin token. Vuelve a E4, que es
// de lo que se trata darle otra ventana al cliente (HU-W1.3-02).
func TestResumeWithoutAPaymentMethodSendsItBackToE4(t *testing.T) {
	repo := newMemRepo()
	repo.put(expiredWithoutPaymentMethod("acc-1"))
	now := t0.Add(2 * time.Hour)
	svc := billing.NewService(repo, &clock{now: now}, resilience.DefaultRetryPolicy(), func() {})

	c, err := svc.Resume(as(security.RoleOperations, "ops-7"), "acc-1", "el cliente pide otra ventana")

	require.NoError(t, err)
	assert.Equal(t, domain.ChargePending, c.Status)
	assert.True(t, c.NextAttemptAt.IsZero(), "esperando la tarjeta, no vencido")
	assert.Equal(t, now.Add(24*time.Hour), c.WindowEndsAt)
	assert.True(t, c.TakesPaymentMethod(), "y el cliente puede pagar desde E4")
}

func TestResumeMovesAFailedChargeBackWithTheSameKeyAndANewWindow(t *testing.T) {
	repo := newMemRepo()
	repo.put(failedCharge("acc-1"))
	now := t0.Add(2 * time.Hour)
	woken := 0
	svc := billing.NewService(repo, &clock{now: now}, resilience.DefaultRetryPolicy(), func() { woken++ })

	c, err := svc.Resume(as(security.RoleOperations, "ops-7"), "acc-1", "  Pasarela restablecida, se reanuda  ")

	require.NoError(t, err)
	assert.Equal(t, domain.ChargePending, c.Status)
	assert.Equal(t, "cobro-acc-1", c.IdempotencyKey, "the same key")
	assert.Equal(t, 0, c.Attempts, "a new window starts")
	assert.Equal(t, now, c.WindowStartedAt)
	assert.Equal(t, now.Add(24*time.Hour), c.WindowEndsAt)
	assert.Equal(t, now, c.NextAttemptAt, "due at once")
	assert.Equal(t, 1, c.ResumeCount)
	assert.Equal(t, "ops-7", c.LastResumedBy, "the author is the subject of the token")
	assert.Equal(t, "Pasarela restablecida, se reanuda", c.ResumeNote)
	assert.Equal(t, 1, woken)

	_, err = svc.Resume(as(security.RoleOperations, "ops-7"), "acc-1", "otra vez")
	assert.ErrorIs(t, err, ports.ErrChargeNotResumable, "only a failed charge resumes")
}

func TestResumeRules(t *testing.T) {
	repo := newMemRepo()
	repo.put(failedCharge("acc-1"))
	svc := billing.NewService(repo, &clock{now: t0}, resilience.DefaultRetryPolicy(), nil)
	ops := as(security.RoleOperations, "ops-1")

	for _, note := range []string{"", "   ", strings.Repeat("a", 501)} {
		_, err := svc.Resume(ops, "acc-1", note)
		assert.ErrorIs(t, err, billing.ErrInvalidNote)
	}
	_, err := svc.Resume(ops, "acc-1", strings.Repeat("ñ", 500))
	assert.NoError(t, err, "500 characters, not bytes")

	repo.put(failedCharge("acc-2"))
	for _, role := range []security.Role{security.RoleInsured, security.RoleAdvisor} {
		_, err := svc.Resume(as(role, "cust-1"), "acc-2", "nota")
		assert.ErrorIs(t, err, security.ErrForbidden, role)
	}
	_, err = svc.Resume(ops, "acc-404", "nota")
	assert.ErrorIs(t, err, ports.ErrChargeNotFound)

	boom := errors.New("database down")
	repo.writeErr = boom
	_, err = svc.Resume(ops, "acc-2", "nota")
	assert.ErrorIs(t, err, boom)
}

// --- Payment method (HU-W1.3-02) -------------------------------------------

// pendingChargeService builds a service whose only charge is pending and
// still waiting for its payment method.
func pendingChargeService() (*memRepo, *billing.Service) {
	repo := newMemRepo()
	repo.put(domain.NewCharge(acceptance("acc-1"), offer("acc-1"),
		domain.CoverageTerms{TermMonths: 120}, t0, t0.Add(24*time.Hour)))
	return repo, billing.NewService(repo, &clock{now: t0}, resilience.DefaultRetryPolicy(), nil)
}

func cardMethod() domain.PaymentMethod {
	return domain.PaymentMethod{
		Kind: domain.PaymentCard, Token: "tok_7b3c1f9a",
		Brand: "visa", Last4: "1111", Installments: 1,
	}
}

func TestTheOwnerHandsOverThePaymentMethodAndTheChargeBecomesDue(t *testing.T) {
	repo, svc := pendingChargeService()
	now := t0

	c, err := svc.AttachPaymentMethod(as(security.RoleInsured, "cust-1"), "acc-1", cardMethod())

	require.NoError(t, err)
	assert.Equal(t, now, c.NextAttemptAt, "the worker can pick it up now")
	assert.Equal(t, 1, c.PaymentAttempt)
	assert.Equal(t, "1111", c.PaymentMethod.Last4)
	stored, _ := repo.Get(context.Background(), "acc-1")
	assert.True(t, stored.PaymentMethod.Captured())
}

// Paying is not something an advisor or an analyst does for someone else.
func TestOnlyTheOwnerHandsOverThePaymentMethod(t *testing.T) {
	_, svc := pendingChargeService()

	for _, ctx := range []context.Context{
		as(security.RoleInsured, "someone-else"),
		as(security.RoleAdvisor, "adv-1"),
		as(security.RoleOperations, "ops-1"),
	} {
		_, err := svc.AttachPaymentMethod(ctx, "acc-1", cardMethod())
		assert.ErrorIs(t, err, security.ErrForbidden)
	}
}

// A client that sends the card number instead of the token must fail loudly:
// the number must never reach this service.
func TestACardNumberInsteadOfATokenIsRefused(t *testing.T) {
	_, svc := pendingChargeService()
	m := cardMethod()
	m.Token = "4111111111111111"

	_, err := svc.AttachPaymentMethod(as(security.RoleInsured, "cust-1"), "acc-1", m)

	assert.ErrorIs(t, err, billing.ErrInvalidPaymentMethod)
	assert.Contains(t, err.Error(), "looks like a card number")
}

func TestThePaymentMethodIsValidated(t *testing.T) {
	_, svc := pendingChargeService()
	ctx := as(security.RoleInsured, "cust-1")

	for name, m := range map[string]domain.PaymentMethod{
		"a card with no token": {Kind: domain.PaymentCard, Installments: 1},
		"pse with no bank":     {Kind: domain.PaymentPSE, Installments: 1},
		"an unknown method":    {Kind: "bitcoin", Installments: 1},
		"too many instalments": {Kind: domain.PaymentCard, Token: "tok_7b3c1f9a", Installments: 48},
		"last4 that is not":    {Kind: domain.PaymentCard, Token: "tok_7b3c1f9a", Last4: "11", Installments: 1},
	} {
		_, err := svc.AttachPaymentMethod(ctx, "acc-1", m)
		assert.ErrorIs(t, err, billing.ErrInvalidPaymentMethod, name)
	}
}

func TestPSENeedsNoToken(t *testing.T) {
	_, svc := pendingChargeService()

	c, err := svc.AttachPaymentMethod(as(security.RoleInsured, "cust-1"), "acc-1",
		domain.PaymentMethod{Kind: domain.PaymentPSE, BankCode: "1007", Installments: 1,
			PayerType: domain.PayerNatural})

	require.NoError(t, err)
	assert.Equal(t, domain.PaymentPSE, c.PaymentMethod.Kind)
	assert.Equal(t, domain.PayerNatural, c.PaymentMethod.PayerType)
}

// PSE asks the bank whether the payer is a person or a company before it can
// take the debit, so the screen has to say which (HU-W1.3-02).
func TestPSENeedsThePayerType(t *testing.T) {
	for _, tc := range []struct {
		name  string
		payer domain.PayerType
	}{
		{"missing", ""},
		{"not one of the two", "cooperativa"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, svc := pendingChargeService()

			_, err := svc.AttachPaymentMethod(as(security.RoleInsured, "cust-1"), "acc-1",
				domain.PaymentMethod{Kind: domain.PaymentPSE, BankCode: "1007", Installments: 1, PayerType: tc.payer})

			require.ErrorIs(t, err, billing.ErrInvalidPaymentMethod)
		})
	}
}

// A card has no payer type and must not be asked for one.
func TestACardTakesNoPayerType(t *testing.T) {
	_, svc := pendingChargeService()

	c, err := svc.AttachPaymentMethod(as(security.RoleInsured, "cust-1"), "acc-1",
		domain.PaymentMethod{Kind: domain.PaymentCard, Token: "tok_visa_1111", Last4: "1111", Installments: 1})

	require.NoError(t, err)
	assert.Empty(t, c.PaymentMethod.PayerType)
}

func TestAChargeThatTakesNoPaymentMethodIsRefused(t *testing.T) {
	repo, svc := pendingChargeService()
	confirmed, _ := repo.Get(context.Background(), "acc-1")
	confirmed.Status = domain.ChargeConfirmed
	repo.rows["acc-1"] = confirmed

	_, err := svc.AttachPaymentMethod(as(security.RoleInsured, "cust-1"), "acc-1", cardMethod())

	assert.ErrorIs(t, err, ports.ErrChargeNotPayable)
}

func TestOverviewFiltersFlagsTheUnattendedCountsAndIsForOperations(t *testing.T) {
	repo := newMemRepo()
	stale := failedCharge("acc-1") // failed at t0+1h
	repo.put(stale)
	fresh := failedCharge("acc-2")
	fresh.LastFailureAt = t0.Add(time.Hour + 45*time.Minute)
	repo.put(fresh)
	retrying := domain.NewCharge(acceptance("acc-3"), offer("acc-3"), domain.CoverageTerms{}, t0, time.Time{})
	retrying.Attempts = 2
	repo.put(retrying)
	svc := billing.NewService(repo, &clock{now: t0.Add(2 * time.Hour)}, resilience.DefaultRetryPolicy(), nil)
	ops := as(security.RoleOperations, "ops-1")

	all, err := svc.Overview(ops, domain.IssuanceFilter{}, t0)
	require.NoError(t, err)
	assert.Len(t, all.Items, 3)
	assert.Equal(t, domain.IssuanceCounts{Charging: 1, ChargingWithRetries: 1, Exceptions: 2, Unattended: 1}, all.Counts)

	got, err := svc.Overview(ops, domain.IssuanceFilter{Status: domain.IssuanceException}, t0)
	require.NoError(t, err)
	byID := map[string]domain.Issuance{}
	for _, i := range got.Items {
		byID[i.AcceptanceID] = i
	}
	require.Len(t, byID, 2)
	assert.True(t, byID["acc-1"].Unattended, "an hour without an analyst")
	assert.False(t, byID["acc-2"].Unattended, "15 minutes is within the 30 of the escalation")
	assert.Equal(t, 3, all.Counts.Charging+all.Counts.Exceptions, "the counts do not depend on the filter")
	assert.Equal(t, domain.IssuanceCounts{Charging: 1, ChargingWithRetries: 1, Exceptions: 2, Unattended: 1}, got.Counts)

	none, err := svc.Overview(ops, domain.IssuanceFilter{Status: domain.IssuanceActive}, t0)
	require.NoError(t, err)
	assert.Empty(t, none.Items)
	assert.NotNil(t, none.Items, "an empty list, not nil")

	for _, role := range []security.Role{security.RoleInsured, security.RoleAdvisor} {
		_, err := svc.Overview(as(role, "x"), domain.IssuanceFilter{}, t0)
		assert.ErrorIs(t, err, security.ErrForbidden, role)
	}
}

func TestOverviewRejectsABadFilter(t *testing.T) {
	svc := billing.NewService(newMemRepo(), &clock{now: t0}, resilience.DefaultRetryPolicy(), nil)
	ops := as(security.RoleOperations, "ops-1")

	_, err := svc.Overview(ops, domain.IssuanceFilter{Limit: 201}, t0)
	assert.ErrorIs(t, err, billing.ErrInvalidLimit)
	_, err = svc.Overview(ops, domain.IssuanceFilter{Status: "other"}, t0)
	assert.ErrorIs(t, err, billing.ErrInvalidFilter)
	_, err = svc.Overview(ops, domain.IssuanceFilter{Channel: "fax"}, t0)
	assert.ErrorIs(t, err, billing.ErrInvalidFilter)
	_, err = svc.Overview(ops, domain.IssuanceFilter{From: t0.Add(time.Hour), To: t0}, t0)
	assert.ErrorIs(t, err, billing.ErrInvalidFilter)
}

// contractRetrier is a ports.ContractRetrier that records what it was asked.
type contractRetrier struct {
	err    error
	got    domain.IssuanceRetry
	gotID  string
	woken  int
	called int
}

func (c *contractRetrier) ReopenContract(_ context.Context, id string, r domain.IssuanceRetry) (domain.ContractIssuance, error) {
	c.called++
	c.gotID, c.got = id, r
	return domain.ContractIssuance{Event: domain.CoverageActivated{PolicyID: "pol-" + id}}, c.err
}

func TestRetryIssuanceResumesAFailedChargeAndRecordsTheAction(t *testing.T) {
	repo := newMemRepo()
	repo.put(failedCharge("acc-1"))
	retrier := &contractRetrier{}
	svc := billing.NewService(repo, &clock{now: t0.Add(2 * time.Hour)}, resilience.DefaultRetryPolicy(), nil).
		WithContractRetry(retrier, func() { retrier.woken++ })

	action, err := svc.RetryIssuance(as(security.RoleOperations, "ops-1"), "acc-1", " Pasarela restablecida ")

	require.NoError(t, err)
	assert.Equal(t, domain.IssuanceActionResume, action)
	assert.Zero(t, retrier.called, "the charge was the failed step")
	assert.Equal(t, domain.ChargePending, repo.get("acc-1").Status)
	actions, _ := svc.ListActions(as(security.RoleOperations, "ops-1"), "acc-1")
	require.Len(t, actions, 1)
	assert.Equal(t, "Pasarela restablecida", actions[0].Note)
}

func TestRetryIssuanceRetriesTheContractWhenTheChargeIsDone(t *testing.T) {
	repo := newMemRepo()
	confirmed := failedCharge("acc-1")
	confirmed.Status = domain.ChargeConfirmed
	repo.put(confirmed)
	retrier := &contractRetrier{}
	svc := billing.NewService(repo, &clock{now: t0.Add(2 * time.Hour)}, resilience.DefaultRetryPolicy(), nil).
		WithContractRetry(retrier, func() { retrier.woken++ })

	action, err := svc.RetryIssuance(as(security.RoleOperations, "ops-7"), "acc-1", "El generador volvió")

	require.NoError(t, err)
	assert.Equal(t, domain.IssuanceActionRetry, action)
	assert.Equal(t, "acc-1", retrier.gotID)
	assert.Equal(t, domain.IssuanceRetry{By: "ops-7", Note: "El generador volvió", At: t0.Add(2 * time.Hour)}, retrier.got)
	assert.Equal(t, 1, retrier.woken, "the worker of the contractual issuance is woken")
}

func TestRetryIssuanceErrors(t *testing.T) {
	ops := as(security.RoleOperations, "ops-1")
	confirmed := failedCharge("acc-1")
	confirmed.Status = domain.ChargeConfirmed
	newSvc := func(retrier *contractRetrier) *billing.Service {
		repo := newMemRepo()
		repo.put(confirmed)
		svc := billing.NewService(repo, &clock{now: t0}, resilience.DefaultRetryPolicy(), nil)
		if retrier != nil {
			svc.WithContractRetry(retrier, nil)
		}
		return svc
	}

	_, err := newSvc(&contractRetrier{}).RetryIssuance(ops, "acc-1", "  ")
	assert.ErrorIs(t, err, billing.ErrInvalidNote, "the note is required")
	_, err = newSvc(&contractRetrier{}).RetryIssuance(as(security.RoleAdvisor, "adv-1"), "acc-1", "x")
	assert.ErrorIs(t, err, security.ErrForbidden)
	_, err = newSvc(&contractRetrier{}).RetryIssuance(ops, "acc-none", "x")
	assert.ErrorIs(t, err, billing.ErrIssuanceNotFound, "no charge at all")
	_, err = newSvc(nil).RetryIssuance(ops, "acc-1", "x")
	assert.ErrorIs(t, err, billing.ErrIssuanceNotRetryable, "no contract retry wired")
	for _, cause := range []error{ports.ErrContractNotFound, ports.ErrContractNotRetryable} {
		_, err = newSvc(&contractRetrier{err: cause}).RetryIssuance(ops, "acc-1", "x")
		assert.ErrorIs(t, err, billing.ErrIssuanceNotRetryable, cause.Error())
	}
	boom := errors.New("database down")
	_, err = newSvc(&contractRetrier{err: boom}).RetryIssuance(ops, "acc-1", "x")
	assert.ErrorIs(t, err, boom)
}

func TestEveryResumptionLeavesAnActionWithItsAuthorAndDate(t *testing.T) {
	repo := newMemRepo()
	repo.put(failedCharge("acc-1"))
	svc := billing.NewService(repo, &clock{now: t0.Add(2 * time.Hour)}, resilience.DefaultRetryPolicy(), nil)

	_, err := svc.Resume(as(security.RoleOperations, "ops-1"), "acc-1", "  the customer fixed the card ")
	require.NoError(t, err)

	got, err := svc.ListActions(as(security.RoleOperations, "ops-2"), "acc-1")
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, domain.IssuanceActionResume, got[0].Action)
	assert.Equal(t, "ops-1", got[0].Actor)
	assert.Equal(t, "the customer fixed the card", got[0].Note)
	assert.Equal(t, t0.Add(2*time.Hour), got[0].CreatedAt)

	_, err = svc.Resume(as(security.RoleOperations, "ops-2"), "acc-1", "again")
	require.ErrorIs(t, err, ports.ErrChargeNotResumable)
	got, _ = svc.ListActions(as(security.RoleOperations, "ops-2"), "acc-1")
	assert.Len(t, got, 1, "a resumption that was refused leaves no action")

	_, err = svc.ListActions(as(security.RoleAdvisor, "adv-1"), "acc-1")
	assert.ErrorIs(t, err, security.ErrForbidden)
}

// The insured of a renewal has until the deadline of the renewal to get the
// payment through (HU-W1.5-02): the charge window ends there, and a method
// handed over after it is refused. A charge of another channel keeps its rule.
func TestARenewalTakesNoPaymentMethodPastItsDeadline(t *testing.T) {
	repo := newMemRepo()
	deadline := t0.Add(72 * time.Hour)
	renewalCharge := domain.NewCharge(domain.Acceptance{ID: "ren-pol-1-20261002", Channel: domain.ChannelRenewal, AcceptedAt: t0},
		domain.Offer{ID: "ren-pol-1-20261002", CustomerID: "cust-1", Premium: 149_800, Currency: "COP"},
		domain.CoverageTerms{TermMonths: 12}, t0, deadline)
	repo.put(renewalCharge)
	clk := &clock{now: t0}
	svc := billing.NewService(repo, clk, resilience.DefaultRetryPolicy(), nil)
	owner := as(security.RoleInsured, "cust-1")

	clk.set(deadline.Add(-time.Minute))
	_, err := svc.AttachPaymentMethod(owner, "ren-pol-1-20261002", cardMethod())
	require.NoError(t, err, "inside the deadline")

	repo.put(renewalCharge)
	clk.set(deadline)
	_, err = svc.AttachPaymentMethod(owner, "ren-pol-1-20261002", cardMethod())
	assert.ErrorIs(t, err, ports.ErrChargeNotPayable, "at the deadline")
}
