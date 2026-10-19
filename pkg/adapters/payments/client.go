// Package payments is the adapter that isolates the internal model from the
// payment gateway (HA-D02). Every charge carries an Idempotency-Key, so the
// gateway never charges twice for the same key; the client enforces a hard
// per-call timeout and protects the caller with a circuit breaker. It
// classifies each answer the way the idempotent retries need it (EC-D02 §7):
// a success, an explicit rejection (final), an ambiguous answer (the charge
// may have happened: retry with the same key) or a call that was never sent.
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

// RejectedError is the explicit rejection of a charge. It matches ErrRejected.
type RejectedError struct {
	// StatusCode is the HTTP status of the answer (402 or 422, usually).
	StatusCode int
	// Code is the gateway's error code, e.g. payment_declined.
	Code string
	// DeclineCode is the issuer's reason, when the gateway gives one.
	DeclineCode string
}

func (e *RejectedError) Error() string {
	msg := fmt.Sprintf("payment gateway rejected the charge (status %d", e.StatusCode)
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

// answer is what one call produced. A rejection is a valid answer of a
// healthy gateway, so it does not count against the circuit breaker.
type answer struct {
	charge   Charge
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
	ans, err := resilience.Execute(c.breaker, func() (answer, error) { return c.charge(ctx, req) })
	switch {
	case errors.Is(err, resilience.ErrOpen):
		return Charge{}, ErrUnavailable
	case err != nil:
		return Charge{}, fmt.Errorf("%w: %w", ErrAmbiguous, err)
	case ans.rejected != nil:
		return Charge{}, ans.rejected
	}
	return ans.charge, nil
}

type errorBody struct {
	Error       string `json:"error"`
	DeclineCode string `json:"decline_code"`
}

func (c *Client) charge(ctx context.Context, req ChargeRequest) (answer, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	payload, err := json.Marshal(req)
	if err != nil {
		return answer{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/charges", bytes.NewReader(payload))
	if err != nil {
		return answer{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Idempotency-Key", req.IdempotencyKey)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		// Refused, cut or timed out: the gateway may have processed it.
		return answer{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body := io.LimitReader(resp.Body, maxBodyBytes)

	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
		// 201 = charged now, 200 = already charged with this key.
		var ch Charge
		if err := json.NewDecoder(body).Decode(&ch); err != nil {
			// The answer was cut: the charge probably happened, and a retry
			// with the same key returns it without charging again.
			return answer{}, fmt.Errorf("decode the charge: %w", err)
		}
		return answer{charge: ch}, nil
	case retryableStatus(resp.StatusCode):
		return answer{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	default:
		var eb errorBody
		_ = json.NewDecoder(body).Decode(&eb)
		return answer{rejected: &RejectedError{StatusCode: resp.StatusCode, Code: eb.Error, DeclineCode: eb.DeclineCode}}, nil
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
