package httpadapter_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/decision"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// HU-W2.2-03: la decisión del analista de operaciones sobre un siniestro.

var decididoEn = time.Date(2026, 9, 16, 16, 40, 0, 0, time.UTC)

// aprobacionFallida es una aprobación cuyo pago rechazó la pasarela (O6b).
var aprobacionFallida = domain.Decision{
	ClaimID: claimID, FilingNumber: "SIN-2026-01655", Outcome: domain.OutcomeApproved, DecidedBy: "analista@solventa.example",
	DecidedAt: decididoEn, ApprovedAmountMinor: 435_000_000, InsuredValueMinor: 32_000_000_000, DeductibleBasisPoints: 500,
	Currency: "COP",
	Payout: &domain.Payout{
		Key: domain.PayoutKey(claimID), BeneficiaryID: "sub-carolina", AmountMinor: 413_250_000, Currency: "COP",
		Destination: domain.PayoutAccount{BankCode: "1007", BankName: "Banco Aliado", AccountType: "savings",
			AccountNumber: "0123-4567-89"},
		Status: idempotency.StatusFailed, Attempts: 1, Failure: domain.PayoutRejected,
		GatewayCode: "invalid_destination_account", LastFailureAt: decididoEn.Add(time.Second),
	},
}

type fakeDecider struct {
	decision domain.Decision
	err      error
	calls    int

	gotClaim, gotAnalyst, gotReason string
	gotAmount                       int64
	gotDestination                  *domain.PayoutAccount
}

func (f *fakeDecider) Approve(_ context.Context, id, analyst string, amount int64) (domain.Decision, error) {
	f.calls++
	f.gotClaim, f.gotAnalyst, f.gotAmount = id, analyst, amount
	return f.decision, f.err
}

func (f *fakeDecider) Reject(_ context.Context, id, analyst, reason string) (domain.Decision, error) {
	f.calls++
	f.gotClaim, f.gotAnalyst, f.gotReason = id, analyst, reason
	return f.decision, f.err
}

func (f *fakeDecider) RetryPayout(_ context.Context, id, analyst string, dst *domain.PayoutAccount) (domain.Decision, error) {
	f.calls++
	f.gotClaim, f.gotAnalyst, f.gotDestination = id, analyst, dst
	return f.decision, f.err
}

func decisionRouter(f *fakeDecider) http.Handler {
	return httpadapter.NewRouter(httpadapter.WithDecisions(f, callers))
}

func post(h http.Handler, path, caller, body string) (int, string) {
	rec := do(h, http.MethodPost, path, caller, strings.NewReader(body), map[string]string{"Content-Type": "application/json"})
	return rec.Code, rec.Body.String()
}

func TestOnlyOperationsDecides(t *testing.T) {
	for _, caller := range []string{"insured", "advisor", "partner", ""} {
		f := &fakeDecider{}
		h := decisionRouter(f)
		want := http.StatusForbidden
		if caller == "" {
			want = http.StatusUnauthorized
		}
		for path, body := range map[string]string{
			"/approval": `{"approved_amount_minor":100}`, "/rejection": `{"reason":"x"}`, "/payout-retry": `{}`,
		} {
			code, _ := post(h, "/v1/claims/"+claimID+path, caller, body)
			assert.Equal(t, want, code, "%s %s", caller, path)
		}
		assert.Zero(t, f.calls, "%s never reaches the service", caller)
	}
}

func TestTheApprovalIsAcceptedWithTheAmountAndTheAnalyst(t *testing.T) {
	d := aprobacionFallida
	p := *d.Payout
	p.Status, p.Attempts, p.Failure, p.GatewayCode, p.LastFailureAt = idempotency.StatusPending, 0, "", "", time.Time{}
	d.Payout = &p
	f := &fakeDecider{decision: d}

	code, body := post(decisionRouter(f), "/v1/claims/"+claimID+"/approval", "operations", `{"approved_amount_minor":435000000}`)

	require.Equal(t, http.StatusAccepted, code, body)
	assert.Equal(t, claimID, f.gotClaim)
	assert.Equal(t, "sub-ops", f.gotAnalyst, "without an e-mail in the token the analyst is their subject")
	assert.EqualValues(t, 435_000_000, f.gotAmount)
	assert.Contains(t, body, `"status":"approved"`)
	assert.Contains(t, body, `"deductible_minor":21750000`)
	assert.Contains(t, body, `"payout_amount_minor":413250000`)
	assert.Contains(t, body, `"payment_reference":"indemnizacion-`+claimID+`"`)
	assert.Contains(t, body, `"masked_number":"•••• 6789"`)
	assert.NotContains(t, body, "0123-4567-89")
	assert.Contains(t, body, `"rejection":null`)
}

func TestARejectionAnswersTheReason(t *testing.T) {
	f := &fakeDecider{decision: domain.Decision{ClaimID: claimID, FilingNumber: "SIN-2026-01655",
		Outcome: domain.OutcomeRejected, DecidedBy: "sub-ops", DecidedAt: decididoEn, Reason: "Preexistente",
		Notice: &domain.Notice{To: "carolina@correo.example"}}}

	code, body := post(decisionRouter(f), "/v1/claims/"+claimID+"/rejection", "operations", `{"reason":"Preexistente"}`)

	require.Equal(t, http.StatusOK, code, body)
	assert.Equal(t, "Preexistente", f.gotReason)
	assert.Contains(t, body, `"status":"rejected"`)
	assert.Contains(t, body, `"rejection":{"reason":"Preexistente","notified_at":null}`)
	assert.Contains(t, body, `"approval":null`)
	assert.NotContains(t, body, "carolina@correo.example", "the decision does not echo the contact")
}

func TestTheRetryCarriesTheCorrectedDestination(t *testing.T) {
	f := &fakeDecider{decision: aprobacionFallida}
	h := decisionRouter(f)

	code, body := post(h, "/v1/claims/"+claimID+"/payout-retry", "operations",
		`{"destination":{"bank_code":"1007","bank_name":"Banco Aliado","account_type":"savings","account_number":"0123-4567-12"}}`)
	require.Equal(t, http.StatusAccepted, code, body)
	require.NotNil(t, f.gotDestination)
	assert.Equal(t, domain.PayoutAccount{BankCode: "1007", BankName: "Banco Aliado", AccountType: "savings",
		AccountNumber: "0123-4567-12"}, *f.gotDestination)

	code, _ = post(h, "/v1/claims/"+claimID+"/payout-retry", "operations", `{}`)
	require.Equal(t, http.StatusAccepted, code)
	assert.Nil(t, f.gotDestination, "without a destination the same one is retried")
}

func TestTheRequestsAreValidatedBeforeTheService(t *testing.T) {
	for name, tc := range map[string]struct{ path, body string }{
		"no amount":        {"/approval", `{}`},
		"zero amount":      {"/approval", `{"approved_amount_minor":0}`},
		"decimal amount":   {"/approval", `{"approved_amount_minor":1.5}`},
		"unknown field":    {"/approval", `{"approved_amount_minor":1,"x":1}`},
		"two objects":      {"/approval", `{"approved_amount_minor":1}{}`},
		"no reason":        {"/rejection", `{}`},
		"not json":         {"/rejection", `motivo`},
		"short account":    {"/payout-retry", `{"destination":{"bank_code":"1007","account_type":"savings","account_number":"12"}}`},
		"bad account type": {"/payout-retry", `{"destination":{"bank_code":"1007","account_type":"cdt","account_number":"0123456789"}}`},
		"no bank":          {"/payout-retry", `{"destination":{"account_type":"savings","account_number":"0123456789"}}`},
	} {
		f := &fakeDecider{}
		code, _ := post(decisionRouter(f), "/v1/claims/"+claimID+tc.path, "operations", tc.body)
		assert.Equal(t, http.StatusBadRequest, code, name)
		assert.Zero(t, f.calls, name)
	}
	code, _ := post(decisionRouter(&fakeDecider{}), "/v1/claims/no-es-un-id/approval", "operations", `{"approved_amount_minor":1}`)
	assert.Equal(t, http.StatusNotFound, code)
}

func TestTheErrorsOfTheDecisionAreAnswered(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want int
		code string
	}{
		{ports.ErrClaimNotFound, http.StatusNotFound, "claim_not_found"},
		{ports.ErrNotDecidable, http.StatusConflict, "claim_not_decidable"},
		{ports.ErrPayoutNotRetryable, http.StatusConflict, "payout_not_retryable"},
		{domain.ErrAmountExceedsInsuredValue, http.StatusUnprocessableEntity, "amount_exceeds_insured_value"},
		{domain.ErrNothingToPay, http.StatusUnprocessableEntity, "nothing_to_pay"},
		{domain.ErrNoPayoutAccount, http.StatusUnprocessableEntity, "no_payout_account"},
		{domain.ErrReasonRequired, http.StatusUnprocessableEntity, "reason_required"},
		{domain.ErrNoContact, http.StatusUnprocessableEntity, "no_contact"},
		{decision.ErrNotCovered, http.StatusUnprocessableEntity, "not_covered"},
		{decision.ErrPolicyUnavailable, http.StatusServiceUnavailable, "policy_unavailable"},
		{errors.New("se cayó"), http.StatusServiceUnavailable, "claims_unavailable"},
	} {
		code, body := post(decisionRouter(&fakeDecider{err: tc.err}), "/v1/claims/"+claimID+"/approval", "operations",
			`{"approved_amount_minor":1}`)
		assert.Equal(t, tc.want, code, tc.code)
		assert.Contains(t, body, `"error":"`+tc.code+`"`)
	}
}
