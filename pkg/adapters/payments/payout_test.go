package payments_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/payments"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
)

func payoutRequest() payments.PayoutRequest {
	return payments.PayoutRequest{
		IdempotencyKey: "indemnizacion-clm-1", Reference: "SIN-2026-01655", BeneficiaryID: "sub-1",
		AmountMinor: 413_250_000, Currency: "COP",
		Destination: payments.PayoutDestination{BankCode: "1007", AccountType: "savings", AccountNumber: "0123-4567-12"},
	}
}

func TestPayoutSendsTheKeyAndTheDestination(t *testing.T) {
	var got map[string]any
	var key, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key, path = r.Header.Get("Idempotency-Key"), r.URL.Path
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"payout_id":"po-1","status":"paid","idempotency_key":"indemnizacion-clm-1",
			"amount_minor":413250000,"currency":"COP"}`))
	}))
	defer srv.Close()

	p, err := client(srv.URL).Payout(context.Background(), payoutRequest())

	require.NoError(t, err)
	assert.Equal(t, payments.Payout{ID: "po-1", Status: "paid", IdempotencyKey: "indemnizacion-clm-1",
		AmountMinor: 413_250_000, Currency: "COP"}, p)
	assert.Equal(t, "/payouts", path)
	assert.Equal(t, "indemnizacion-clm-1", key)
	assert.Equal(t, map[string]any{"bank_code": "1007", "account_type": "savings", "account_number": "0123-4567-12"},
		got["destination"])
	assert.NotContains(t, got, "IdempotencyKey", "the key travels in the header only")
}

func TestAnInvalidDestinationIsAnExplicitRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"error":"invalid_destination_account","payout_id":"po-7"}`))
	}))
	defer srv.Close()
	c := client(srv.URL)

	for i := 0; i < 3; i++ {
		_, err := c.Payout(context.Background(), payoutRequest())

		var rejected *payments.RejectedError
		require.True(t, errors.As(err, &rejected))
		assert.Equal(t, "invalid_destination_account", rejected.Code)
		assert.Equal(t, "po-7", rejected.Reference)
		assert.ErrorIs(t, err, idempotency.ErrRejected)
		assert.Equal(t, "payment gateway rejected the payout (status 422, code invalid_destination_account)", err.Error())
	}
	assert.Equal(t, "closed", c.BreakerState(), "a rejection is an answer, not a failure")
}

func TestAPayoutWithAnUnknownOutcomeIsAmbiguous(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"5xx": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		"cut body": func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"payout_id":`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(handler)
			defer srv.Close()

			_, err := client(srv.URL).Payout(context.Background(), payoutRequest())

			assert.ErrorIs(t, err, payments.ErrAmbiguous)
			assert.ErrorIs(t, err, idempotency.ErrAmbiguous)
		})
	}
}

func TestAnInvalidPayoutNeverReachesTheGateway(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	c := client(srv.URL)

	for name, mutate := range map[string]func(*payments.PayoutRequest){
		"no key":         func(r *payments.PayoutRequest) { r.IdempotencyKey = "" },
		"no reference":   func(r *payments.PayoutRequest) { r.Reference = "" },
		"no beneficiary": func(r *payments.PayoutRequest) { r.BeneficiaryID = "" },
		"zero amount":    func(r *payments.PayoutRequest) { r.AmountMinor = 0 },
		"no currency":    func(r *payments.PayoutRequest) { r.Currency = "" },
		"no bank":        func(r *payments.PayoutRequest) { r.Destination.BankCode = "" },
		"no account":     func(r *payments.PayoutRequest) { r.Destination.AccountNumber = "" },
	} {
		req := payoutRequest()
		mutate(&req)
		_, err := c.Payout(context.Background(), req)
		assert.ErrorIs(t, err, payments.ErrInvalidPayout, name)
	}
	assert.Zero(t, calls.Load())
}
