package httpadapter_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/wallet"
)

// HU-W1.6-02: where the premium stands in the wallet, and the detail of one
// policy (screens A1, A2 and A2b).

var (
	installmentRules = domain.DefaultInstallmentRules
	cancelledOn      = time.Date(2026, 9, 12, 5, 0, 0, 0, time.UTC)
)

// cancelledPolicy is a policy whose second month was declined three times
// and cancelled it.
func cancelledPolicy() (domain.Policy, domain.PolicyBilling) {
	p := renewalPolicy()
	p.CoverageStart = time.Date(2026, 7, 2, 15, 0, 0, 0, time.UTC)
	i := domain.NewInstallment(p, 2, installmentRules, p.CoverageStart.AddDate(0, 1, 0))
	i.Status, i.Attempts, i.NextAttemptAt = domain.InstallmentUnpaid, 3, time.Time{}
	for n := 1; n <= 3; n++ {
		at, _ := installmentRules.AttemptAt(i.DueAt, n)
		i.History = append(i.History, domain.InstallmentAttempt{Attempt: n, Outcome: domain.AttemptDeclined,
			DeclineCode: "51", AttemptedAt: at})
	}
	c := &domain.PolicyCancellation{PolicyID: p.ID, Reason: domain.CancellationNonPayment, Installment: i,
		CancelledAt: installmentRules.CancelsAt(i.DueAt), EmailSentAt: installmentRules.CancelsAt(i.DueAt).Add(time.Minute)}
	return p, domain.BillingOf(p, installmentRules, []domain.Installment{i}, c, cancelledOn)
}

func callDetail(t *testing.T, svc *fakeWallet, id, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/policies/"+id, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithObservability("origination"),
		httpadapter.WithPolicyWallet(svc, tokenVerifier{})).ServeHTTP(rec, req)
	return rec
}

func TestTheWalletSaysAPolicyIsCancelledForNonPaymentWithNoRefund(t *testing.T) {
	p, b := cancelledPolicy()
	view := renewal.PolicyView{Policy: p}
	item := wallet.Item{Policy: domain.WalletPolicyOf(p, cancelledOn), Renewal: &view, Billing: &b}
	item.Policy.Status = domain.PolicyCancelled
	svc := &fakeWallet{wallet: wallet.Wallet{Items: []wallet.Item{item, {Policy: corePolicy()}}}}

	rec := callWallet(t, svc, "insured:sub-1")

	require.Equal(t, http.StatusOK, rec.Code)
	items := decodeWallet(t, rec).Items
	assert.Equal(t, "cancelled", items[0]["status"])
	assert.Equal(t, map[string]any{"status": "cancelled", "next_due_at": nil, "pending": nil}, items[0]["payment"])
	c := items[0]["cancellation"].(map[string]any)
	assert.Equal(t, "non_payment", c["reason"])
	assert.Equal(t, "not_applicable", c["refund"])
	assert.Equal(t, 2.0, c["month"])
	assert.Equal(t, 3.0, c["attempts"])
	assert.Equal(t, 142_300.0, c["amount"])
	assert.NotNil(t, c["email_sent_at"])
	// HU-W1.6-01: a cancellation for non-payment keeps its members and has
	// none of the ones of a cancellation at the request of the insured.
	assert.Equal(t, c["cancelled_at"], c["effective_at"], "it takes effect when it is made")
	assert.Nil(t, c["refund_detail"])
	assert.Nil(t, c["insured_reason"])
	assert.Nil(t, items[1]["payment"], "the policy core keeps no premiums")
	assert.Nil(t, items[1]["cancellation"])
}

func TestTheWalletSaysAPolicyIsInItsGracePeriod(t *testing.T) {
	p := renewalPolicy()
	p.CoverageStart = time.Date(2026, 7, 2, 15, 0, 0, 0, time.UTC)
	i := domain.NewInstallment(p, 2, installmentRules, p.CoverageStart.AddDate(0, 1, 0))
	b := domain.BillingOf(p, installmentRules, []domain.Installment{i}, nil, i.DueAt.Add(time.Hour))
	view := renewal.PolicyView{Policy: p}
	svc := &fakeWallet{wallet: wallet.Wallet{Items: []wallet.Item{
		{Policy: domain.WalletPolicyOf(p, i.DueAt), Renewal: &view, Billing: &b}}}}

	rec := callWallet(t, svc, "insured:sub-1")

	item := decodeWallet(t, rec).Items[0]
	assert.Equal(t, "active", item["status"], "the grace period keeps the coverage")
	pay := item["payment"].(map[string]any)
	assert.Equal(t, "payment_pending", pay["status"])
	pending := pay["pending"].(map[string]any)
	assert.Equal(t, 2.0, pending["month"])
	assert.Equal(t, "2026-08-02T15:00:00Z", pending["due_at"])
	assert.Equal(t, "2026-09-02T15:00:00Z", pending["cancels_at"])
	assert.Equal(t, "2026-08-03T15:00:00Z", pending["next_attempt_at"])
	assert.Nil(t, item["cancellation"])
}

func TestTheDetailOfAPolicyListsEveryMonthTheNewestFirst(t *testing.T) {
	p, b := cancelledPolicy()
	view := renewal.PolicyView{Policy: p}
	issued := domain.Charge{Status: domain.ChargeConfirmed, AmountMinor: 14_230_000, Currency: "COP",
		ConfirmedAt: p.CoverageStart}
	svc := &fakeWallet{detail: wallet.Detail{
		Item:          wallet.Item{Policy: domain.WalletPolicyOf(p, cancelledOn), Renewal: &view, Billing: &b},
		PaymentMethod: &domain.PaymentMethod{Kind: domain.PaymentCard, Brand: "visa", Last4: "0010", Installments: 1},
		Issuance:      &issued,
	}}

	rec := callDetail(t, svc, "pol-1", "insured:sub-1")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "pol-1", svc.asked)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, "pol-1", body["policy_id"])
	method := body["payment_method"].(map[string]any)
	assert.Equal(t, "0010", method["last4"])
	assert.NotContains(t, rec.Body.String(), "token")
	payments := body["payments"].([]any)
	require.Len(t, payments, 2)
	second := payments[0].(map[string]any)
	assert.Equal(t, 2.0, second["month"])
	assert.Equal(t, "installment", second["kind"])
	assert.Equal(t, "unpaid", second["status"])
	assert.Nil(t, second["paid_at"])
	attempts := second["attempts"].([]any)
	require.Len(t, attempts, 3)
	assert.Equal(t, map[string]any{"attempt": 1.0, "outcome": "declined", "decline_code": "51",
		"attempted_at": "2026-08-03T15:00:00Z"}, attempts[0])
	first := payments[1].(map[string]any)
	assert.Equal(t, "issuance", first["kind"])
	assert.Equal(t, "paid", first["status"])
	assert.Equal(t, 142_300.0, first["amount"])
	assert.Equal(t, "2026-07-02T15:00:00Z", first["paid_at"])
}

func TestTheDetailOfAPolicyOfTheCoreHasNoPayments(t *testing.T) {
	svc := &fakeWallet{detail: wallet.Detail{Item: wallet.Item{Policy: corePolicy()}}}

	rec := callDetail(t, svc, "AU-2025-118204", "insured:sub-1")

	require.Equal(t, http.StatusOK, rec.Code)
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Nil(t, body["payment_method"])
	assert.Equal(t, []any{}, body["payments"])
	assert.Nil(t, body["payment"])
}

func TestTheDetailOfAPolicyAnswersItsErrors(t *testing.T) {
	assert.Equal(t, http.StatusNotFound, callDetail(t, &fakeWallet{err: ports.ErrPolicyNotFound}, "pol-x", "insured:sub-1").Code)
	assert.Equal(t, http.StatusUnauthorized, callDetail(t, &fakeWallet{}, "pol-1", "").Code)
	assert.Equal(t, http.StatusForbidden, callDetail(t, &fakeWallet{}, "pol-1", "advisor:sub-2").Code,
		"only the insured reads their policy")
	long := make([]byte, 129)
	for i := range long {
		long[i] = 'a'
	}
	assert.Equal(t, http.StatusBadRequest, callDetail(t, &fakeWallet{}, string(long), "insured:sub-1").Code)
}
