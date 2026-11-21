package httpadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/cancellation"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/wallet"
)

// HU-W1.6-01: the quote and the confirmation of the cancellation (screens A5
// and A5b) over HTTP.

var cancelledAt = time.Date(2026, 11, 18, 20, 0, 0, 0, time.UTC)

func cancellationQuote() domain.CancellationQuote {
	return domain.CancellationQuote{
		PolicyID: "VH-2026-000101", EffectiveAt: domain.EndOfDayColombia(cancelledAt),
		Paid:        domain.PaidMonth{Seq: 1, Kind: domain.PaidByIssuance, AmountMinor: 14_230_000, Currency: "COP"},
		PeriodStart: time.Date(2026, 11, 18, 15, 0, 0, 0, time.UTC), PeriodEnd: time.Date(2026, 12, 18, 15, 0, 0, 0, time.UTC),
		PeriodDays: 30, UnusedDays: 29, RefundAmountMinor: 13_755_600, Currency: "COP",
		Method:        domain.PaymentMethod{Kind: domain.PaymentCard, Brand: "visa", Last4: "1111"},
		PaymentStatus: domain.PaymentUpToDate,
	}
}

type fakeCancellations struct {
	quote domain.CancellationQuote
	err   error
	got   domain.CancellationRequest
	calls int
}

func (f *fakeCancellations) Quote(_ context.Context, _ string) (domain.CancellationQuote, error) {
	return f.quote, f.err
}

func (f *fakeCancellations) Cancel(_ context.Context, id string, r domain.CancellationRequest) (domain.PolicyCancellation, error) {
	f.calls++
	f.got = r
	if f.err != nil {
		return domain.PolicyCancellation{}, f.err
	}
	refund := domain.NewRefund(f.quote, cancelledAt, cancelledAt.Add(24*time.Hour))
	refund.Reference = "REF-2026-00001"
	return domain.PolicyCancellation{PolicyID: id, Reason: domain.CancellationInsuredRequest, CancelledAt: cancelledAt,
		EffectiveAt: f.quote.EffectiveAt, InsuredReason: r.Reason, Comments: r.Comments, Refund: &refund}, nil
}

func callCancellation(t *testing.T, svc *fakeCancellations, method, path, token, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithObservability("origination"),
		httpadapter.WithCancellations(svc, tokenVerifier{})).ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
	}
	return rec, out
}

const quotePath = "/v1/policies/VH-2026-000101/cancellation/quote"
const cancelPath = "/v1/policies/VH-2026-000101/cancellation"

func TestTheQuoteShowsTheCalculationBeforeConfirming(t *testing.T) {
	svc := &fakeCancellations{quote: cancellationQuote()}

	rec, body := callCancellation(t, svc, http.MethodGet, quotePath, "insured:sub-1", "")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"), "the refund changes from one day to the next")
	assert.Equal(t, 13_755_600/100.0, body["refund_amount"])
	assert.Equal(t, 96.7, body["prorate_percent"])
	assert.Equal(t, 29.0, body["unused_days"])
	assert.Equal(t, 30.0, body["period_days"])
	assert.Equal(t, "2026-11-19T04:59:59Z", body["effective_at"], "the end of the day in Colombia")
	assert.Equal(t, map[string]any{"month": 1.0, "kind": "issuance", "start": "2026-11-18T15:00:00Z",
		"end": "2026-12-18T15:00:00Z", "paid_amount": 142_300.0, "currency": "COP"}, body["paid_period"])
	assert.Equal(t, map[string]any{"method": "card", "brand": "visa", "last4": "1111", "bank_code": nil}, body["refund_method"])
	assert.Equal(t, "up_to_date", body["payment_status"])
}

func TestTheConfirmationRegistersTheRefundAndAnswersThePolicyCancelled(t *testing.T) {
	svc := &fakeCancellations{quote: cancellationQuote()}

	rec, body := callCancellation(t, svc, http.MethodPost, cancelPath, "insured:sub-1",
		`{"reason":"property_sold","comments":"vendí el inmueble","refund_amount":137556,"accept_terms":true}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "/v1/policies/VH-2026-000101", rec.Header().Get("Location"))
	assert.Equal(t, domain.CancellationRequest{Reason: domain.InsuredReasonPropertySold, Comments: "vendí el inmueble",
		RefundAmountMinor: 13_755_600, AcceptTerms: true}, svc.got)
	assert.Equal(t, "cancelled", body["status"])
	c := body["cancellation"].(map[string]any)
	assert.Equal(t, "insured_request", c["reason"])
	assert.Equal(t, "in_process", c["refund"])
	assert.Nil(t, c["month"], "the members of a cancellation for non-payment are null")
	assert.Nil(t, c["attempts"])
	assert.Nil(t, c["email_sent_at"])
	assert.Equal(t, map[string]any{"code": "property_sold", "comments": "vendí el inmueble"}, c["insured_reason"])
	d := c["refund_detail"].(map[string]any)
	assert.Equal(t, "REF-2026-00001", d["reference"])
	assert.Equal(t, 137_556.0, d["amount"], "CA-2: the amount")
	assert.Equal(t, "2026-11-18T20:00:00Z", d["registered_at"], "the date")
	assert.Equal(t, "card", d["method"].(map[string]any)["method"], "and the payment method")
	assert.Nil(t, d["completed_at"])
}

func TestTheCancellationRoutesAnswerEachFailure(t *testing.T) {
	changed := cancellationQuote()
	changed.UnusedDays, changed.RefundAmountMinor = 28, 13_281_300
	for name, tc := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"another's, the core's or unknown": {ports.ErrPolicyNotFound, http.StatusNotFound, "policy_not_found"},
		"not in force":                     {domain.ErrPolicyNotInForce, http.StatusConflict, "policy_not_in_force"},
		"already cancelled":                {ports.ErrPolicyAlreadyCancelled, http.StatusConflict, "policy_already_cancelled"},
		"renewal under way":                {ports.ErrRenewalInProgress, http.StatusConflict, "renewal_in_progress"},
		"instalment being charged":         {ports.ErrPaymentInProgress, http.StatusConflict, "payment_in_progress"},
		"refund changed":                   {&cancellation.RefundChangedError{Quote: changed}, http.StatusConflict, "refund_changed"},
		"store down":                       {errors.New("connection refused"), http.StatusServiceUnavailable, "cancellation_unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			svc := &fakeCancellations{quote: cancellationQuote(), err: tc.err}

			rec, body := callCancellation(t, svc, http.MethodPost, cancelPath, "insured:sub-1",
				`{"reason":"other","refund_amount":137556,"accept_terms":true}`)

			assert.Equal(t, tc.status, rec.Code)
			assert.Equal(t, tc.code, body["error"])
			if tc.code == "refund_changed" {
				assert.Equal(t, 132_813.0, body["quote"].(map[string]any)["refund_amount"], "the new amount to confirm")
			}
		})
	}
}

func TestOnlyTheInsuredWithAWellFormedConfirmationReachesTheService(t *testing.T) {
	svc := &fakeCancellations{quote: cancellationQuote()}

	rec, _ := callCancellation(t, svc, http.MethodGet, quotePath, "", "")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	rec, _ = callCancellation(t, svc, http.MethodGet, quotePath, "advisor:sub-2", "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	rec, _ = callCancellation(t, svc, http.MethodPost, cancelPath, "operations:sub-3", `{}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)

	rec, _ = callCancellation(t, svc, http.MethodPost, cancelPath, "insured:sub-1",
		`{"reason":"other","refund_amount":1,"accept_terms":true,"refund_to":"another card"}`)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "nothing the contract does not name")
	rec, body := callCancellation(t, svc, http.MethodPost, cancelPath, "insured:sub-1", `{"reason":"other"}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "invalid_request", body["error"])
	assert.Len(t, body["fields"], 2, "refund_amount and accept_terms are required")
	for _, id := range []string{strings.Repeat("x", 129), "%00"} {
		rec, _ = callCancellation(t, svc, http.MethodGet, "/v1/policies/"+id+"/cancellation/quote", "insured:sub-1", "")
		assert.Equal(t, http.StatusBadRequest, rec.Code, "a NUL never reaches the database")
	}
	assert.Zero(t, svc.calls)

	svc.err = &domain.ValidationError{Fields: []domain.FieldError{{Field: "accept_terms", Code: "required"}}}
	rec, body = callCancellation(t, svc, http.MethodPost, cancelPath, "insured:sub-1",
		`{"reason":"other","refund_amount":1,"accept_terms":false}`)
	assert.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	assert.Equal(t, "invalid_request", body["error"])
}

func TestTheDetailShowsTheRefundOfACancellationAtTheRequestOfTheInsured(t *testing.T) {
	q := cancellationQuote()
	r := domain.NewRefund(q, cancelledAt, cancelledAt.Add(24*time.Hour))
	r.Reference, r.Status, r.CompletedAt = "REF-2026-00001", domain.RefundCompleted, cancelledAt.Add(time.Minute)
	p := renewalPolicy()
	c := &domain.PolicyCancellation{PolicyID: p.ID, Reason: domain.CancellationInsuredRequest, CancelledAt: cancelledAt,
		EffectiveAt: q.EffectiveAt, InsuredReason: domain.InsuredReasonBetterOffer, Refund: &r}
	b := domain.BillingOf(p, installmentRules, nil, c, cancelledAt)

	body := cancelledDetail(t, p, &b)

	got := body["cancellation"].(map[string]any)
	assert.Equal(t, "completed", got["refund"], "the gateway confirmed it")
	assert.Equal(t, "2026-11-19T04:59:59Z", got["effective_at"])
	assert.Equal(t, map[string]any{"code": "better_offer", "comments": nil}, got["insured_reason"])
	d := got["refund_detail"].(map[string]any)
	assert.Equal(t, "2026-11-18T20:01:00Z", d["completed_at"])
	assert.Equal(t, 96.7, d["prorate_percent"])
}

// cancelledDetail reads GET /v1/policies/{id} of a policy origination issued.
func cancelledDetail(t *testing.T, p domain.Policy, b *domain.PolicyBilling) map[string]any {
	t.Helper()
	view := renewal.PolicyView{Policy: p}
	item := wallet.Item{Policy: domain.WalletPolicyOf(p, cancelledAt), Renewal: &view, Billing: b}
	item.Policy.Status = domain.PolicyCancelled
	rec := callDetail(t, &fakeWallet{detail: wallet.Detail{Item: item}}, p.ID, "insured:sub-1")
	require.Equal(t, http.StatusOK, rec.Code)
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}
