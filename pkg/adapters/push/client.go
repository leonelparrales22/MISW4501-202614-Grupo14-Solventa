// Package push is the adapter that isolates the internal model from the push
// notification provider (Firebase Cloud Messaging in the architecture; the
// stubs/push simulator in this project, HU-M3.1-01). It enforces a hard
// per-call timeout and protects the caller with a circuit breaker, and it
// classifies every answer with the errors of pkg/idempotency, so a sender can
// retry with the same key without delivering a message twice.
package push

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

// DefaultTimeout is the hard cut of one send. A push is asynchronous to the
// payment it reports, so it is wider than the 700 ms of the profile sources,
// and short enough for several attempts inside the 60 s of HU-M3.1-01.
const DefaultTimeout = 2 * time.Second

const maxBodyBytes = 1 << 16

// Errors of a send. Each wraps the matching error of pkg/idempotency, so
// errors.Is(err, idempotency.ErrAmbiguous) holds for an ambiguous answer.
var (
	// ErrRejected means the provider answered and refused the message (the
	// device is no longer registered, the message is invalid). Retrying gets
	// the same answer.
	ErrRejected = fmt.Errorf("push provider rejected the message: %w", idempotency.ErrRejected)
	// ErrAmbiguous means the outcome is unknown: timeout, connection error,
	// 5xx. The provider may have taken it; a retry reuses the same key.
	ErrAmbiguous = fmt.Errorf("push provider outcome unknown: %w", idempotency.ErrAmbiguous)
	// ErrUnavailable means the call was not sent: the circuit breaker is
	// isolating the provider.
	ErrUnavailable = fmt.Errorf("push provider isolated by its circuit breaker: %w", idempotency.ErrNotSent)
)

// ErrInvalidMessage is returned for a message without token, title, body or
// idempotency key. It is a caller error: it never reaches the provider nor
// counts against the circuit breaker.
var ErrInvalidMessage = errors.New("token, title, body and idempotency key are required")

// Message is one push notification to one device.
type Message struct {
	// Token addresses the device (the registration token of the provider).
	Token string `json:"token"`
	Title string `json:"title"`
	Body  string `json:"body"`
	// Group is the thread the device stacks the message in. Every message
	// keeps its own entry: nothing is collapsed or overwritten.
	Group string `json:"group,omitempty"`
	// Data travels with the message to the app: what to open when the user
	// taps it.
	Data map[string]string `json:"data,omitempty"`
	// IdempotencyKey identifies the message: the same key is delivered once.
	IdempotencyKey string `json:"-"`
}

// Receipt is the provider's record of an accepted message.
type Receipt struct {
	ID string `json:"id"`
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
		http: httpClient, baseURL: strings.TrimRight(cfg.BaseURL, "/"), timeout: timeout,
		breaker: resilience.NewBreaker("push-provider", cfg.Breaker),
	}
}

// BreakerState exposes the circuit state ("closed", "open", "half-open").
func (c *Client) BreakerState() string { return c.breaker.State() }

// answer is what one call produced. A rejection is a valid answer of a
// healthy provider, so it does not count against the circuit breaker.
type answer struct {
	receipt  Receipt
	rejected error
}

// Send delivers the message. It returns the receipt on success, ErrRejected
// when the provider refuses it, ErrAmbiguous when the outcome is unknown and
// ErrUnavailable when the call was not sent. Retrying with the same
// IdempotencyKey is always safe.
func (c *Client) Send(ctx context.Context, m Message) (Receipt, error) {
	if m.Token == "" || m.Title == "" || m.Body == "" || m.IdempotencyKey == "" {
		return Receipt{}, ErrInvalidMessage
	}
	ans, err := resilience.Execute(c.breaker, func() (answer, error) { return c.send(ctx, m) })
	switch {
	case errors.Is(err, resilience.ErrOpen):
		return Receipt{}, ErrUnavailable
	case err != nil:
		return Receipt{}, fmt.Errorf("%w: %w", ErrAmbiguous, err)
	case ans.rejected != nil:
		return Receipt{}, ans.rejected
	}
	return ans.receipt, nil
}

func (c *Client) send(ctx context.Context, m Message) (answer, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	raw, err := json.Marshal(m)
	if err != nil {
		return answer{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/messages", bytes.NewReader(raw))
	if err != nil {
		return answer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", m.IdempotencyKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return answer{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body := io.LimitReader(resp.Body, maxBodyBytes)

	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
		// 201 = accepted now, 200 = already accepted with this key.
		var r Receipt
		if err := json.NewDecoder(body).Decode(&r); err != nil {
			return answer{}, fmt.Errorf("decode the receipt: %w", err)
		}
		return answer{receipt: r}, nil
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout ||
		resp.StatusCode == http.StatusTooManyRequests:
		return answer{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	default:
		var eb struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(body).Decode(&eb)
		return answer{rejected: fmt.Errorf("%w: status %d %s", ErrRejected, resp.StatusCode, eb.Error)}, nil
	}
}
