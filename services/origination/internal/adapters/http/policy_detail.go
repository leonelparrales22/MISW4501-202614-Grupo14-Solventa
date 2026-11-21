package httpadapter

import (
	"net/http"
	"sort"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/wallet"
)

// HU-W1.6-02: where the premium of a policy stands, its cancellation for
// non-payment and the detail of one policy (screens A1, A2 and A2b).
// HU-W1.6-01: the cancellation at the request of the insured, with its refund
// (screen A5b).

// paymentBody is where the premium of a policy origination issued stands.
type paymentBody struct {
	// Status is up_to_date, payment_pending (an instalment fell due unpaid:
	// the policy is in its grace period and keeps its coverage) or cancelled.
	Status string `json:"status"`
	// NextDueAt is when the next month falls due, while up to date.
	NextDueAt *time.Time   `json:"next_due_at"`
	Pending   *pendingBody `json:"pending"`
}

// pendingBody is the instalment that fell due unpaid.
type pendingBody struct {
	Month         int        `json:"month"`
	DueAt         time.Time  `json:"due_at"`
	GraceEndsAt   time.Time  `json:"grace_ends_at"`
	CancelsAt     time.Time  `json:"cancels_at"`
	Amount        float64    `json:"amount"`
	Currency      string     `json:"currency"`
	Attempts      int        `json:"attempts"`
	NextAttemptAt *time.Time `json:"next_attempt_at"`
}

// cancellationBody is the cancellation of a policy: for non-payment
// (HU-W1.6-02), with the instalment that was not paid and no refund, or at the
// request of the insured (HU-W1.6-01), with the refund of the part of the
// current month not used. The members of the other reason are null.
type cancellationBody struct {
	// Reason is non_payment or insured_request.
	Reason      string    `json:"reason"`
	CancelledAt time.Time `json:"cancelled_at"`
	// EffectiveAt is when the coverage ends: the cancellation itself for
	// non-payment, the end of the day for one at the request of the insured.
	EffectiveAt time.Time `json:"effective_at"`
	// Refund is not_applicable (non-payment, or nothing to give back),
	// in_process, completed or failed.
	Refund string `json:"refund"`
	// Month is the instalment that was not paid, with its due date, the end
	// of its grace period, its amount and how many times it was charged
	// (non-payment only).
	Month       *int       `json:"month"`
	DueAt       *time.Time `json:"due_at"`
	GraceEndsAt *time.Time `json:"grace_ends_at"`
	Amount      *float64   `json:"amount"`
	Currency    string     `json:"currency"`
	Attempts    *int       `json:"attempts"`
	// EmailSentAt is when the e-mail to the insured went out; null until
	// then, and always for a cancellation at the request of the insured. The
	// push goes out from the event.
	EmailSentAt *time.Time `json:"email_sent_at"`
	// RefundDetail and InsuredReason are the refund and the reason of a
	// cancellation at the request of the insured (screen A5b).
	RefundDetail  *refundDetailBody  `json:"refund_detail"`
	InsuredReason *insuredReasonBody `json:"insured_reason"`
}

// paidPeriodBody is the month of the premium a refund is calculated on.
type paidPeriodBody struct {
	Month int `json:"month"`
	// Kind is issuance (month 1) or installment.
	Kind       string    `json:"kind"`
	Start      time.Time `json:"start"`
	End        time.Time `json:"end"`
	PaidAmount float64   `json:"paid_amount"`
	Currency   string    `json:"currency"`
}

// refundMethodBody is where a refund goes back to: the payment method of the
// issuance, without its token.
type refundMethodBody struct {
	Method   string  `json:"method"`
	Brand    *string `json:"brand"`
	Last4    *string `json:"last4"`
	BankCode *string `json:"bank_code"`
}

func toRefundMethodBody(m domain.PaymentMethod) *refundMethodBody {
	if !m.Captured() {
		return nil
	}
	return &refundMethodBody{Method: string(m.Kind), Brand: optionalString(m.Brand), Last4: optionalString(m.Last4),
		BankCode: optionalString(m.BankCode)}
}

// refundDetailBody is the refund registered with a cancellation (CA-2): its
// reference, amount, date and payment method, and the calculation behind it.
type refundDetailBody struct {
	// Reference is REF-<year>-<number>; null when nothing is refunded.
	Reference      *string           `json:"reference"`
	Amount         float64           `json:"amount"`
	Currency       string            `json:"currency"`
	Method         *refundMethodBody `json:"method"`
	RegisteredAt   time.Time         `json:"registered_at"`
	CompletedAt    *time.Time        `json:"completed_at"`
	PaidPeriod     paidPeriodBody    `json:"paid_period"`
	UnusedDays     int               `json:"unused_days"`
	PeriodDays     int               `json:"period_days"`
	ProratePercent float64           `json:"prorate_percent"`
}

// insuredReasonBody is why the insured cancelled.
type insuredReasonBody struct {
	Code     string  `json:"code"`
	Comments *string `json:"comments"`
}

func timePtr(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	u := t.UTC()
	return &u
}

func toPaymentBody(b *domain.PolicyBilling) *paymentBody {
	if b == nil {
		return nil
	}
	out := &paymentBody{Status: string(b.Status), NextDueAt: timePtr(b.NextDueAt)}
	if p := b.Pending; p != nil {
		out.Pending = &pendingBody{Month: p.Seq, DueAt: p.DueAt.UTC(), GraceEndsAt: p.GraceEndsAt.UTC(),
			CancelsAt: p.CancelsAt.UTC(), Amount: domain.FromMinorUnits(p.AmountMinor), Currency: p.Currency,
			Attempts: p.Attempts, NextAttemptAt: timePtr(p.NextAttemptAt)}
	}
	return out
}

func toCancellationBody(b *domain.PolicyBilling) *cancellationBody {
	if b == nil || b.Cancellation == nil {
		return nil
	}
	return cancellationBodyOf(*b.Cancellation)
}

// cancellationBodyOf is the body of a cancellation, by its reason.
func cancellationBodyOf(c domain.PolicyCancellation) *cancellationBody {
	out := &cancellationBody{Reason: string(c.Reason), CancelledAt: c.CancelledAt.UTC(), EffectiveAt: c.EffectiveAt.UTC(),
		Refund: domain.RefundNotApplicable, EmailSentAt: timePtr(c.EmailSentAt)}
	if c.EffectiveAt.IsZero() {
		out.EffectiveAt = c.CancelledAt.UTC()
	}
	if c.Reason != domain.CancellationInsuredRequest {
		i := c.Installment
		month, attempts, amount := i.Seq, i.Attempts, domain.FromMinorUnits(i.AmountMinor)
		out.Month, out.DueAt, out.GraceEndsAt = &month, timePtr(i.DueAt), timePtr(i.GraceEndsAt)
		out.Amount, out.Currency, out.Attempts = &amount, i.Currency, &attempts
		return out
	}
	out.InsuredReason = &insuredReasonBody{Code: string(c.InsuredReason), Comments: optionalString(c.Comments)}
	if r := c.Refund; r != nil {
		out.Refund, out.Currency = string(r.Status), r.Currency
		out.RefundDetail = &refundDetailBody{
			Reference: optionalString(r.Reference), Amount: domain.FromMinorUnits(r.AmountMinor), Currency: r.Currency,
			Method: toRefundMethodBody(r.Method), RegisteredAt: r.RegisteredAt.UTC(), CompletedAt: timePtr(r.CompletedAt),
			PaidPeriod: paidPeriodBody{Month: r.Paid.Seq, Kind: string(r.Paid.Kind), Start: r.PeriodStart.UTC(),
				End: r.PeriodEnd.UTC(), PaidAmount: domain.FromMinorUnits(r.Paid.AmountMinor), Currency: r.Currency},
			UnusedDays: r.UnusedDays, PeriodDays: r.PeriodDays, ProratePercent: r.ProratePercent(),
		}
	}
	return out
}

// attemptBody is one answer of the gateway to an instalment.
type attemptBody struct {
	Attempt     int       `json:"attempt"`
	Outcome     string    `json:"outcome"`
	DeclineCode *string   `json:"decline_code"`
	AttemptedAt time.Time `json:"attempted_at"`
}

// paymentRecordBody is one month of the premium: the issuance (month 1) or an
// instalment.
type paymentRecordBody struct {
	Month int `json:"month"`
	// Kind is issuance or installment.
	Kind string `json:"kind"`
	// Status is paid, pending or unpaid.
	Status   string        `json:"status"`
	DueAt    time.Time     `json:"due_at"`
	Amount   float64       `json:"amount"`
	Currency string        `json:"currency"`
	PaidAt   *time.Time    `json:"paid_at"`
	Attempts []attemptBody `json:"attempts"`
}

// policyDetailResponse is GET /v1/policies/{policy_id}: the policy as the
// wallet lists it, the card it is charged to and every month billed, the
// newest first (screens A2 and A2b). For a policy of the policy core,
// payment_method is null and payments is empty: the core keeps no premiums.
type policyDetailResponse struct {
	walletItemBody
	PaymentMethod *paymentMethodBody  `json:"payment_method"`
	Payments      []paymentRecordBody `json:"payments"`
}

func toPolicyDetail(d wallet.Detail) policyDetailResponse {
	out := policyDetailResponse{walletItemBody: toWalletItem(d.Item), PaymentMethod: methodOf(d.PaymentMethod),
		Payments: []paymentRecordBody{}}
	if c := d.Issuance; c != nil {
		out.Payments = append(out.Payments, paymentRecordBody{Month: 1, Kind: "issuance",
			Status: string(domain.InstallmentPaid), DueAt: d.Policy.CoverageStart.UTC(),
			Amount: domain.FromMinorUnits(c.AmountMinor), Currency: c.Currency, PaidAt: timePtr(c.ConfirmedAt),
			Attempts: []attemptBody{}})
	}
	if b := d.Billing; b != nil {
		for _, i := range b.Installments {
			rec := paymentRecordBody{Month: i.Seq, Kind: "installment", Status: string(i.Status), DueAt: i.DueAt.UTC(),
				Amount: domain.FromMinorUnits(i.AmountMinor), Currency: i.Currency, PaidAt: timePtr(i.PaidAt),
				Attempts: make([]attemptBody, 0, len(i.History))}
			for _, a := range i.History {
				rec.Attempts = append(rec.Attempts, attemptBody{Attempt: a.Attempt, Outcome: string(a.Outcome),
					DeclineCode: optionalString(a.DeclineCode), AttemptedAt: a.AttemptedAt.UTC()})
			}
			out.Payments = append(out.Payments, rec)
		}
	}
	sort.SliceStable(out.Payments, func(a, b int) bool { return out.Payments[a].Month > out.Payments[b].Month })
	return out
}

func getPolicyDetail(svc WalletService) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id, ok := pathID(req, "policy_id")
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_request", "policy_id (max 128 chars) is required")
			return
		}
		d, err := svc.Get(req.Context(), id)
		if err != nil {
			respondRenewalError(w, req, err)
			return
		}
		writeJSON(w, http.StatusOK, toPolicyDetail(d))
	}
}
