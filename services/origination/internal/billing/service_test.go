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

func failedCharge(id string) domain.Charge {
	c := domain.NewCharge(acceptance(id), offer(id), domain.CoverageTerms{TermMonths: 120}, t0, t0.Add(time.Hour))
	c.Status, c.FailureReason, c.Attempts, c.LastFailureAt = domain.ChargeFailed, domain.ChargeRetryWindowExpired, 9, t0.Add(time.Hour)
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
		domain.PaymentMethod{Kind: domain.PaymentPSE, BankCode: "1007", Installments: 1})

	require.NoError(t, err)
	assert.Equal(t, domain.PaymentPSE, c.PaymentMethod.Kind)
}

func TestAChargeThatTakesNoPaymentMethodIsRefused(t *testing.T) {
	repo, svc := pendingChargeService()
	confirmed, _ := repo.Get(context.Background(), "acc-1")
	confirmed.Status = domain.ChargeConfirmed
	repo.rows["acc-1"] = confirmed

	_, err := svc.AttachPaymentMethod(as(security.RoleInsured, "cust-1"), "acc-1", cardMethod())

	assert.ErrorIs(t, err, ports.ErrChargeNotPayable)
}
