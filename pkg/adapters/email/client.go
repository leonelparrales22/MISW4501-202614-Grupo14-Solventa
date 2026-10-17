// Package email is the adapter that isolates the internal model from the
// e-mail delivery provider. It enforces a hard per-call timeout and protects
// the caller with a circuit breaker, so a slow or failing provider is cut off
// instead of holding the issuance back. Sending is idempotent by key: the
// provider drops a message it already accepted.
package email

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

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

// DefaultTimeout is the hard cut of one send. E-mail is not on the latency
// path of a quote, so it is wider than the 700 ms of the profile sources.
const DefaultTimeout = 2 * time.Second

const maxBodyBytes = 1 << 16

// ErrUnavailable means the provider did not accept the message: timeout,
// connection error, unexpected status or open circuit.
var ErrUnavailable = errors.New("e-mail provider is unavailable")

// ErrInvalidMessage is returned for a message without recipient, subject, body
// or idempotency key. It is a caller error and never counts against the
// circuit breaker.
var ErrInvalidMessage = errors.New("recipient, subject, body and idempotency key are required")

// Message is one e-mail.
type Message struct {
	To      string `json:"to"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	// IdempotencyKey identifies the message: sending the same key twice
	// delivers it once.
	IdempotencyKey string `json:"-"`
}

// Config configures the client.
type Config struct {
	BaseURL string
	Timeout time.Duration
	Breaker resilience.Config
	// HTTPClient, when set, carries the calls; Timeout still bounds every call.
	HTTPClient *http.Client
}

// Client sends messages through the provider behind a circuit breaker.
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
		breaker: resilience.NewBreaker("email", cfg.Breaker),
	}
}

// BreakerState exposes the circuit state ("closed", "open", "half-open").
func (c *Client) BreakerState() string { return c.breaker.State() }

// Send delivers the message. Every failure of the provider is reported as
// ErrUnavailable so the caller can retry later with the same key.
func (c *Client) Send(ctx context.Context, m Message) error {
	if m.To == "" || m.Subject == "" || m.Body == "" || m.IdempotencyKey == "" {
		return ErrInvalidMessage
	}
	_, err := resilience.Execute(c.breaker, func() (struct{}, error) {
		return struct{}{}, c.send(ctx, m)
	})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return nil
}

func (c *Client) send(ctx context.Context, m Message) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	payload, err := json.Marshal(m)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/messages", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", m.IdempotencyKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))

	// 201 = accepted now, 200 = already accepted with the same key.
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}
