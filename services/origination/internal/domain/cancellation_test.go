package domain_test

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// HU-W1.6-01: the rule of the refund when the insured cancels (CA-1).

const premiumMinor = 14_230_000 // 142.300 COP

func issuance(amount int64) *domain.Charge {
	return &domain.Charge{AcceptanceID: "acc-1", Status: domain.ChargeConfirmed, AmountMinor: amount, Currency: "COP",
		ProviderReference: "ch-1"}
}

func paidInstallment(seq int, amount int64) domain.Installment {
	return domain.Installment{PolicyID: "VH-2026-004512", Seq: seq, AmountMinor: amount, Currency: "COP",
		Status: domain.InstallmentPaid, ProviderReference: "ch-" + strconv.Itoa(seq)}
}

func quote(t *testing.T, p domain.Policy, now time.Time, issued *domain.Charge, installments ...domain.Installment) domain.CancellationQuote {
	t.Helper()
	r := domain.DefaultInstallmentRules
	seq, _, _ := r.CurrentMonth(p, now)
	paid := domain.PaidMonthOf(seq, issued, installments)
	return domain.QuoteCancellation(p, r, paid, domain.PaymentMethod{Kind: domain.PaymentCard, Token: "tok_1", Brand: "visa",
		Last4: "1111"}, domain.PaymentUpToDate, now)
}

func TestTheRefundIsThePaidPartOfTheCurrentMonthNotUsed(t *testing.T) {
	start := colombia(2026, 11, 18, 10, 0)
	for name, tc := range map[string]struct {
		policy                 domain.Policy
		now                    time.Time
		installments           []domain.Installment
		month, period, unused  int
		refund                 int64
		percent                float64
		periodStart, periodEnd time.Time
	}{
		"the day of the purchase (plan §5.1.1)": {policy: lifePolicy(start, 12), now: colombia(2026, 11, 18, 15, 0),
			month: 1, period: 30, unused: 29, refund: 13_755_600, percent: 96.7,
			periodStart: start.UTC(), periodEnd: colombia(2026, 12, 18, 10, 0).UTC()},
		"half way (plan §5.1.1)": {policy: lifePolicy(start, 12), now: colombia(2026, 12, 3, 9, 0),
			month: 1, period: 30, unused: 14, refund: 6_640_600, percent: 46.7},
		"the last day of the month": {policy: lifePolicy(start, 12), now: colombia(2026, 12, 17, 22, 0),
			month: 1, period: 30, unused: 0, refund: 0, percent: 0},
		"a month of 31 days paid by an instalment": {policy: lifePolicy(start, 12), now: colombia(2026, 12, 18, 11, 0),
			installments: []domain.Installment{paidInstallment(2, premiumMinor)}, month: 2, period: 31, unused: 30,
			refund: 13_770_900, percent: 96.8},
		"February of 28 days": {policy: lifePolicy(colombia(2027, 1, 10, 8, 0), 12), now: colombia(2027, 2, 10, 9, 0),
			installments: []domain.Installment{paidInstallment(2, premiumMinor)}, month: 2, period: 28, unused: 27,
			refund: 13_721_700, percent: 96.4},
		"February of 29 days": {policy: lifePolicy(colombia(2028, 1, 10, 8, 0), 12), now: colombia(2028, 2, 24, 9, 0),
			installments: []domain.Installment{paidInstallment(2, premiumMinor)}, month: 2, period: 29, unused: 14,
			refund: 6_869_600, percent: 48.3},
		"the last month ends with the coverage": {
			policy: domain.Policy{ID: "VH-2026-004512", AcceptanceID: "acc-1", Premium: 142_300, Currency: "COP",
				CoverageStart: start.UTC(), CoverageEnd: colombia(2027, 11, 5, 10, 0).UTC()},
			now:          colombia(2027, 10, 20, 9, 0),
			installments: []domain.Installment{paidInstallment(12, premiumMinor)}, month: 12, period: 18, unused: 15,
			refund: 11_858_300, percent: 83.3},
		"an endorsed premium does not change what was paid": {policy: lifePolicy(start, 12),
			now:          colombia(2026, 12, 18, 11, 0),
			installments: []domain.Installment{paidInstallment(2, 9_000_000)}, month: 2, period: 31, unused: 30,
			refund: 8_709_600, percent: 96.8},
		"the current month is in its grace period (D-6 a)": {policy: lifePolicy(start, 12), now: colombia(2026, 12, 20, 9, 0),
			installments: []domain.Installment{{PolicyID: "VH-2026-004512", Seq: 2, AmountMinor: premiumMinor, Currency: "COP",
				Status: domain.InstallmentPending}}, month: 2, period: 31, unused: 28, refund: 0, percent: 90.3},
		"the current month is due and not billed yet": {policy: lifePolicy(start, 12), now: colombia(2026, 12, 18, 11, 0),
			month: 2, period: 31, unused: 30, refund: 0, percent: 96.8},
	} {
		t.Run(name, func(t *testing.T) {
			q := quote(t, tc.policy, tc.now, issuance(premiumMinor), tc.installments...)

			assert.Equal(t, tc.month, q.Paid.Seq)
			assert.Equal(t, tc.period, q.PeriodDays)
			assert.Equal(t, tc.unused, q.UnusedDays)
			assert.Equal(t, tc.refund, q.RefundAmountMinor)
			assert.InDelta(t, tc.percent, q.ProratePercent(), 0.0001)
			assert.Zero(t, q.RefundAmountMinor%100, "truncated to the peso")
			if !tc.periodStart.IsZero() {
				assert.Equal(t, tc.periodStart, q.PeriodStart)
				assert.Equal(t, tc.periodEnd, q.PeriodEnd)
			}
			if tc.refund == 0 {
				assert.Equal(t, domain.RefundStatusNotApplicable, q.RefundStatus())
			} else {
				assert.Equal(t, domain.RefundInProcess, q.RefundStatus())
			}
		})
	}
}

func TestTheIssuanceAndAnInstalmentAreTheBaseOfTheirMonth(t *testing.T) {
	p := lifePolicy(colombia(2026, 11, 18, 10, 0), 12)

	first := quote(t, p, colombia(2026, 11, 20, 9, 0), issuance(premiumMinor))
	second := quote(t, p, colombia(2026, 12, 20, 9, 0), issuance(premiumMinor), paidInstallment(2, 15_000_000))

	assert.Equal(t, domain.PaidMonth{Seq: 1, Kind: domain.PaidByIssuance, AmountMinor: premiumMinor, Currency: "COP",
		ChargeReference: "ch-1"}, first.Paid)
	assert.Equal(t, domain.PaidByInstallment, second.Paid.Kind)
	assert.Equal(t, int64(15_000_000), second.Paid.AmountMinor)
	assert.Equal(t, "ch-2", second.Paid.ChargeReference, "the refund goes back from the charge of the month")
	assert.Empty(t, first.Method.Token, "the token never leaves the payment method of the issuance")
	assert.Equal(t, "1111", first.Method.Last4)

	unconfirmed := issuance(premiumMinor)
	unconfirmed.Status = domain.ChargePending
	assert.Zero(t, domain.PaidMonthOf(1, unconfirmed, nil).AmountMinor)
}

func TestTheDayIsTheCivilDayOfColombia(t *testing.T) {
	p := lifePolicy(colombia(2026, 11, 18, 10, 0), 12)

	lastMinute := quote(t, p, colombia(2026, 12, 3, 23, 59), issuance(premiumMinor))
	nextDay := quote(t, p, colombia(2026, 12, 4, 0, 0), issuance(premiumMinor))

	assert.Equal(t, 14, lastMinute.UnusedDays, "23:59 in Colombia is 04:59 UTC of the next day, still the 3rd here")
	assert.Equal(t, colombia(2026, 12, 3, 23, 59).Add(59*time.Second).UTC(), lastMinute.EffectiveAt,
		"the coverage ends at the end of the day")
	assert.Equal(t, 13, nextDay.UnusedDays, "after midnight one more day is used: the refund changes")
	assert.Equal(t, colombia(2026, 12, 4, 23, 59).Add(59*time.Second).UTC(), nextDay.EffectiveAt)
}

func TestADemonstrationCountsItsOwnDays(t *testing.T) {
	r := domain.InstallmentRules{GraceDays: 30, AttemptDays: []int{1}, Day: time.Minute, RetryUnknownAfter: time.Minute}
	start := time.Date(2026, 11, 20, 15, 0, 0, 0, time.UTC)
	p := domain.Policy{ID: "VH-2026-000001", Premium: 142_300, Currency: "COP", CoverageStart: start,
		CoverageEnd: start.Add(360 * time.Minute)}
	now := start.Add(40*time.Minute + 30*time.Second)
	seq, _, _ := r.CurrentMonth(p, now)

	q := domain.QuoteCancellation(p, r, domain.PaidMonthOf(seq, nil, []domain.Installment{paidInstallment(2, premiumMinor)}),
		domain.PaymentMethod{}, domain.PaymentUpToDate, now)

	assert.Equal(t, 2, q.Paid.Seq, "a month of a demonstration lasts 30 minutes")
	assert.Equal(t, 30, q.PeriodDays)
	assert.Equal(t, 19, q.UnusedDays, "minute 10 of the month is its 11th day")
	assert.Equal(t, int64(9_012_300), q.RefundAmountMinor)
	assert.Equal(t, start.Add(41*time.Minute), q.EffectiveAt, "the end of the day of the demonstration")
}

func TestTheRequestNeedsAReasonOfTheListAndTheAcceptance(t *testing.T) {
	ok := domain.CancellationRequest{Reason: domain.InsuredReasonPropertySold, Comments: strings.Repeat("á", 500),
		RefundAmountMinor: 13_755_600, AcceptTerms: true}
	require.NoError(t, ok.Validate(), "500 characters, not bytes")
	for _, r := range domain.InsuredCancellationReasons {
		ok.Reason = r
		require.NoError(t, ok.Validate(), r)
	}

	bad := domain.CancellationRequest{Reason: "moved_abroad", Comments: strings.Repeat("a", 501), RefundAmountMinor: -1}
	var invalid *domain.ValidationError
	require.ErrorAs(t, bad.Validate(), &invalid)
	assert.Equal(t, []domain.FieldError{{Field: "reason", Code: "invalid"}, {Field: "comments", Code: "too_long"},
		{Field: "refund_amount", Code: "invalid"}, {Field: "accept_terms", Code: "required"}}, invalid.Fields)
	assert.Len(t, domain.InsuredCancellationReasons, 6, "decision D-9 a")
}

func TestTheRefundIsRegisteredWithItsKeyAndIsDueAtOnce(t *testing.T) {
	p := lifePolicy(colombia(2026, 11, 18, 10, 0), 12)
	now := colombia(2026, 11, 18, 15, 0)
	q := quote(t, p, now, issuance(premiumMinor))
	req := domain.CancellationRequest{Reason: domain.InsuredReasonPropertySold, Comments: "  vendí el inmueble ",
		RefundAmountMinor: q.RefundAmountMinor, AcceptTerms: true}

	c := domain.NewInsuredCancellation(q, req, "sub-carolina", now, now.Add(24*time.Hour))

	assert.Equal(t, "vendí el inmueble", c.Comments)
	assert.Equal(t, q.EffectiveAt, c.EffectiveAt)
	assert.Equal(t, domain.RefundInProcess, c.Refund.Status)
	assert.Equal(t, "reembolso-VH-2026-004512", c.Refund.IdempotencyKey, "never random (EC-D02)")
	assert.Equal(t, now.UTC(), c.Refund.NextAttemptAt)
	assert.Equal(t, idempotency.Attempt{ID: "VH-2026-004512", Key: "reembolso-VH-2026-004512",
		Status: idempotency.StatusPending, WindowEndsAt: now.Add(24 * time.Hour).UTC(), NextAttemptAt: now.UTC()},
		c.Refund.AttemptState())
	assert.Equal(t, "REF-2026-00042", domain.FormatRefundReference(domain.RefundYear(colombia(2026, 12, 31, 23, 0)), 42),
		"the year of Colombia, not of UTC")

	nothing := domain.NewRefund(domain.CancellationQuote{PolicyID: p.ID}, now, now.Add(time.Hour))
	assert.Equal(t, domain.RefundStatusNotApplicable, nothing.Status)
	assert.Empty(t, nothing.IdempotencyKey, "nothing is sent to the gateway")
}
