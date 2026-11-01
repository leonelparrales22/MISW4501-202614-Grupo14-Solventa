// Package payments is the adapter that isolates the internal model from the
// payment gateway (HA-D02). Every charge carries an Idempotency-Key, so the
// gateway never charges twice for the same key; the client enforces a hard
// per-call timeout and protects the caller with a circuit breaker. It
// classifies each answer the way the idempotent retries need it (EC-D02 §7):
// a success, an explicit rejection (final), an ambiguous answer (the charge
// may have happened: retry with the same key) or a call that was never sent.
// Indemnity payouts (HU-W2.2-03) go through the same rules.
package payments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

// DefaultTimeout is the hard cut of one charge, the same 700 ms of every
// external adapter (decision D-9). A cut only makes the answer ambiguous, and
// an ambiguous charge is retried with the same key.
const DefaultTimeout = 700 * time.Millisecond

const maxBodyBytes = 1 << 16

// The errors wrap the classification of pkg/idempotency, so a caller can use
// either: errors.Is(err, payments.ErrAmbiguous) and errors.Is(err,
// idempotency.ErrAmbiguous) both hold for an ambiguous answer.
var (
	// ErrRejected means the gateway answered and declined the charge. It is
	// final (see RejectedError for the details).
	ErrRejected = fmt.Errorf("payment gateway rejected the charge: %w", idempotency.ErrRejected)
	// ErrAmbiguous means the outcome is unknown: timeout, connection refused
	// or cut, 5xx, or an unreadable success. The gateway may have charged.
	ErrAmbiguous = fmt.Errorf("payment gateway outcome unknown: %w", idempotency.ErrAmbiguous)
	// ErrUnavailable means the charge was not sent because the circuit
	// breaker is isolating the gateway.
	ErrUnavailable = fmt.Errorf("payment gateway isolated by its circuit breaker: %w", idempotency.ErrNotSent)
)

// ErrInvalidCharge is returned for a charge without idempotency key,
// reference, customer, positive amount or currency. It is a caller error and
// never reaches the gateway nor counts against the circuit breaker.
var ErrInvalidCharge = errors.New("idempotency key, reference, customer id, a positive amount and currency are required")

// ChargeRequest is one charge. AmountMinor is in minor units of Currency
// (cents: 89 500 COP is 8 950 000).
type ChargeRequest struct {
	// IdempotencyKey identifies the charge: the gateway charges a key once.
	IdempotencyKey string `json:"-"`
	// Reference is the caller's reference, e.g. the offer id.
	Reference   string `json:"reference"`
	CustomerID  string `json:"customer_id"`
	AmountMinor int64  `json:"amount_minor"`
	Currency    string `json:"currency"`
	// PaymentToken identifies the card at the gateway (HU-W1.3-02). The card
	// number is never here: the browser tokenises it against the gateway and
	// only the token reaches us. Empty is omitted, so a caller that does not
	// use a payment method sends exactly what it sent before.
	PaymentToken string `json:"payment_token,omitempty"`
}

// Charge is the gateway's record of a successful charge.
type Charge struct {
	ID             string `json:"charge_id"`
	Status         string `json:"status"`
	IdempotencyKey string `json:"idempotency_key"`
	AmountMinor    int64  `json:"amount_minor"`
	Currency       string `json:"currency"`
}

// ErrInvalidPayout is returned for a payout without idempotency key,
// reference, beneficiary, positive amount, currency or destination account.
// Like ErrInvalidCharge, it never reaches the gateway.
var ErrInvalidPayout = errors.New("idempotency key, reference, beneficiary id, a positive amount, currency, bank code and account number are required")

// RejectedError is the explicit rejection of a charge or a payout. It matches
// ErrRejected.
type RejectedError struct {
	// StatusCode is the HTTP status of the answer (402 or 422, usually).
	StatusCode int
	// Code is the gateway's error code, e.g. payment_declined or
	// invalid_destination_account.
	Code string
	// DeclineCode is the issuer's reason, when the gateway gives one.
	DeclineCode string
	// Reference is the gateway's id for the declined charge or payout, when it
	// gives one. It is what the customer quotes to the issuer (screen E4c).
	Reference string
	// Operation is what was rejected: "charge" (the default) or "payout".
	Operation string
}

func (e *RejectedError) Error() string {
	operation := e.Operation
	if operation == "" {
		operation = "charge"
	}
	msg := fmt.Sprintf("payment gateway rejected the %s (status %d", operation, e.StatusCode)
	if e.Code != "" {
		msg += ", code " + e.Code
	}
	if e.DeclineCode != "" {
		msg += ", decline code " + e.DeclineCode
	}
	return msg + ")"
}

// Is lets errors.Is match ErrRejected and idempotency.ErrRejected.
func (e *RejectedError) Is(target error) bool {
	return target == ErrRejected || target == idempotency.ErrRejected
}

// Config configures the client.
type Config struct {
	BaseURL string
	Timeout time.Duration
	Breaker resilience.Config
	// HTTPClient, when set, carries the calls; Timeout still bounds every call.
	HTTPClient *http.Client
}

// Client charges through the payment gateway behind a circuit breaker.
type Client struct {
	http    *http.Client
	baseURL string
	timeout time.Duration
	breaker *resilience.Breaker
}

// NewClient builds a client. A zero Timeout falls back to DefaultTimeout.
func NewClient(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}
	return &Client{
		http:    httpClient,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		timeout: timeout,
		breaker: resilience.NewBreaker("payment-gateway", cfg.Breaker),
	}
}

// BreakerState exposes the circuit state ("closed", "open", "half-open").
func (c *Client) BreakerState() string { return c.breaker.State() }

// PayoutDestination is the bank account an indemnity is paid to.
type PayoutDestination struct {
	BankCode      string `json:"bank_code"`
	AccountType   string `json:"account_type"`
	AccountNumber string `json:"account_number"`
}

// PayoutRequest is one indemnity payout (HU-W2.2-03). AmountMinor is in minor
// units of Currency and is what reaches the beneficiary, after the deductible.
type PayoutRequest struct {
	// IdempotencyKey identifies the payout: the gateway pays a key once. A
	// rejected payout moved no money and does not bind the key, so the same
	// key is retried after correcting the destination.
	IdempotencyKey string            `json:"-"`
	Reference      string            `json:"reference"`
	BeneficiaryID  string            `json:"beneficiary_id"`
	AmountMinor    int64             `json:"amount_minor"`
	Currency       string            `json:"currency"`
	Destination    PayoutDestination `json:"destination"`
}

// Payout is the gateway's record of a payout made.
type Payout struct {
	ID             string `json:"payout_id"`
	Status         string `json:"status"`
	IdempotencyKey string `json:"idempotency_key"`
	AmountMinor    int64  `json:"amount_minor"`
	Currency       string `json:"currency"`
}

// answer is what one call produced. A rejection is a valid answer of a
// healthy gateway, so it does not count against the circuit breaker.
type answer[T any] struct {
	value    T
	rejected *RejectedError
}

// Charge asks the gateway to charge the request. It returns the charge on
// success, a *RejectedError (ErrRejected) when the gateway declines it,
// ErrAmbiguous when the outcome is unknown and ErrUnavailable when the call
// was not sent. Retrying with the same IdempotencyKey is always safe.
func (c *Client) Charge(ctx context.Context, req ChargeRequest) (Charge, error) {
	if req.IdempotencyKey == "" || req.Reference == "" || req.CustomerID == "" || req.AmountMinor <= 0 || req.Currency == "" {
		return Charge{}, ErrInvalidCharge
	}
	return call[Charge](ctx, c, "/charges", "charge", req.IdempotencyKey, req)
}

// Payout asks the gateway to pay the request, with the same classification
// as Charge: the payout on success, a *RejectedError (ErrRejected) when the
// gateway rejects it (an invalid destination account, for one), ErrAmbiguous
// when the outcome is unknown and ErrUnavailable when the call was not sent.
// Retrying with the same IdempotencyKey never pays twice.
func (c *Client) Payout(ctx context.Context, req PayoutRequest) (Payout, error) {
	d := req.Destination
	if req.IdempotencyKey == "" || req.Reference == "" || req.BeneficiaryID == "" || req.AmountMinor <= 0 ||
		req.Currency == "" || d.BankCode == "" || d.AccountNumber == "" {
		return Payout{}, ErrInvalidPayout
	}
	return call[Payout](ctx, c, "/payouts", "payout", req.IdempotencyKey, req)
}

// call sends one idempotent request through the circuit breaker and maps the
// outcome to the errors of this package.
func call[T any](ctx context.Context, c *Client, path, operation, key string, payload any) (T, error) {
	var zero T
	ans, err := resilience.Execute(c.breaker, func() (answer[T], error) {
		return send[T](ctx, c, path, operation, key, payload)
	})
	switch {
	case errors.Is(err, resilience.ErrOpen):
		return zero, ErrUnavailable
	case err != nil:
		return zero, fmt.Errorf("%w: %w", ErrAmbiguous, err)
	case ans.rejected != nil:
		return zero, ans.rejected
	}
	return ans.value, nil
}

type errorBody struct {
	Error       string `json:"error"`
	DeclineCode string `json:"decline_code"`
	ChargeID    string `json:"charge_id"`
	PayoutID    string `json:"payout_id"`
}

func send[T any](ctx context.Context, c *Client, path, operation, key string, payload any) (answer[T], error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	raw, err := json.Marshal(payload)
	if err != nil {
		return answer[T]{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(raw))
	if err != nil {
		return answer[T]{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Idempotency-Key", key)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		// Refused, cut or timed out: the gateway may have processed it.
		return answer[T]{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body := io.LimitReader(resp.Body, maxBodyBytes)

	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
		// 201 = processed now, 200 = already processed with this key.
		var value T
		if err := json.NewDecoder(body).Decode(&value); err != nil {
			// The answer was cut: the operation probably happened, and a
			// retry with the same key returns it without repeating it.
			return answer[T]{}, fmt.Errorf("decode the %s: %w", operation, err)
		}
		return answer[T]{value: value}, nil
	case retryableStatus(resp.StatusCode):
		return answer[T]{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	default:
		var eb errorBody
		_ = json.NewDecoder(body).Decode(&eb)
		reference := eb.ChargeID
		if reference == "" {
			reference = eb.PayoutID
		}
		rejected := &RejectedError{StatusCode: resp.StatusCode, Code: eb.Error, DeclineCode: eb.DeclineCode,
			Reference: reference}
		if operation != "charge" {
			rejected.Operation = operation
		}
		return answer[T]{rejected: rejected}, nil
	}
}

// retryableStatus tells whether an answer leaves the outcome unknown: a
// server error, or a 4xx that says "not now" (timeout, a request with the
// same key still in progress, too early or too many requests). Any other 4xx
// is an explicit rejection (402 declined, 422 key reused with another amount).
func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	}
	return code < 400 || code >= 500
}
