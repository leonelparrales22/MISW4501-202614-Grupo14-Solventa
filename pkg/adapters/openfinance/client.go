// Package openfinance is the adapter that isolates the internal model from
// the Open Finance provider. It enforces the hard per-dependency timeout
// (700 ms) and protects the caller with a circuit breaker, so a slow or
// failing provider is cut off instead of dragging the whole journey down.
//
// Besides reading profiles, it tells the ecosystem that a consent was revoked
// (HU-M1.3-02): every notice carries an Idempotency-Key and its answer is
// classified the way pkg/idempotency needs it, like adapters/payments.
package openfinance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

// DefaultTimeout is the hard cut for the Open Finance dependency.
const DefaultTimeout = 700 * time.Millisecond

const maxBodyBytes = 1 << 20

// ErrSourceUnavailable means the provider did not give a usable answer:
// timeout, connection error, non-200 status, malformed body or open circuit.
var ErrSourceUnavailable = errors.New("open finance is unavailable")

// ErrInvalidCustomerID is returned for an empty customer identifier. It is a
// caller error and never counts against the circuit breaker.
var ErrInvalidCustomerID = errors.New("customer id is required")

// Profile is the provider-neutral risk signal returned by Open Finance.
type Profile struct {
	CustomerID string    `json:"customer_id"`
	Score      float64   `json:"score"`
	Category   string    `json:"category"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Config configures the client.
type Config struct {
	BaseURL string
	Timeout time.Duration
	Breaker resilience.Config
	// HTTPClient, when set, carries the calls so several adapters can share one
	// connection pool (EXP-L01 used 200 connections per host). Timeout above
	// still bounds every call. When nil, a client with Timeout is created.
	HTTPClient *http.Client
}

// Client calls the Open Finance provider through a circuit breaker.
type Client struct {
	http    *http.Client
	baseURL string
	timeout time.Duration
	breaker *resilience.Breaker
	// revocations has its own breaker: the notices of revocations and the
	// profile reads fail independently.
	revocations *resilience.Breaker
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
		http:        httpClient,
		baseURL:     strings.TrimRight(cfg.BaseURL, "/"),
		timeout:     timeout,
		breaker:     resilience.NewBreaker("open-finance", cfg.Breaker),
		revocations: resilience.NewBreaker("open-finance-revocations", cfg.Breaker),
	}
}

// BreakerState exposes the circuit state ("closed", "open", "half-open").
func (c *Client) BreakerState() string {
	return c.breaker.State()
}

// FetchProfile returns the customer's risk signal. Every failure of the
// provider is reported as ErrSourceUnavailable so callers can degrade.
func (c *Client) FetchProfile(ctx context.Context, customerID string) (Profile, error) {
	if customerID == "" {
		return Profile{}, ErrInvalidCustomerID
	}
	p, err := resilience.Execute(c.breaker, func() (Profile, error) {
		return c.fetch(ctx, customerID)
	})
	if err != nil {
		return Profile{}, fmt.Errorf("%w: %w", ErrSourceUnavailable, err)
	}
	return p, nil
}

func (c *Client) fetch(ctx context.Context, customerID string) (Profile, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	endpoint := c.baseURL + "/profiles/" + url.PathEscape(customerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return Profile{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Profile{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return Profile{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	var p Profile
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&p); err != nil {
		return Profile{}, fmt.Errorf("decode response: %w", err)
	}
	return p, nil
}

// The errors of RevokeConsent wrap the classification of pkg/idempotency, so
// a caller can use either: errors.Is(err, openfinance.ErrRevocationAmbiguous)
// and errors.Is(err, idempotency.ErrAmbiguous) both hold.
var (
	// ErrRevocationRejected means the provider answered and refused the
	// notice (a 4xx other than 408, 409, 425 and 429). It is final.
	ErrRevocationRejected = fmt.Errorf("open finance rejected the revocation notice: %w", idempotency.ErrRejected)
	// ErrRevocationAmbiguous means the outcome is unknown: timeout,
	// connection refused or cut, 5xx, a "not now" 4xx or an unreadable
	// success. The provider may have recorded it, so retry with the same key.
	ErrRevocationAmbiguous = fmt.Errorf("open finance revocation outcome unknown: %w", idempotency.ErrAmbiguous)
	// ErrRevocationNotSent means the notice was not sent because the circuit
	// breaker of the revocations is open.
	ErrRevocationNotSent = fmt.Errorf("open finance revocations isolated by their circuit breaker: %w", idempotency.ErrNotSent)
)

// ErrInvalidRevocation is returned for a notice without idempotency key,
// consent, entity, customer or effective time. It never reaches the provider
// nor counts against the circuit breaker.
var ErrInvalidRevocation = errors.New("idempotency key, consent id, entity code, customer id and effective time are required")

// RevocationNotice tells one entity of the ecosystem that a consent was
// revoked (HU-M1.3-02). It carries only what the entity needs to stop using
// the data: never the document nor the name of the holder.
type RevocationNotice struct {
	ConsentID   string    `json:"consent_id"`
	EntityCode  string    `json:"entity_code"`
	CustomerID  string    `json:"customer_id"`
	EffectiveAt time.Time `json:"effective_at"`
}

// RevocationConfirmation is the provider's answer to an accepted notice.
type RevocationConfirmation struct {
	Reference   string    `json:"revocation_reference"`
	Status      string    `json:"status"`
	ConfirmedAt time.Time `json:"confirmed_at"`
}

// revocationAnswer is what one call produced. A rejection is a valid answer
// of a healthy provider, so it does not count against the circuit breaker.
type revocationAnswer struct {
	confirmation RevocationConfirmation
	rejectedWith int
}

// RevokeConsent sends the notice with POST /consent-revocations and the
// Idempotency-Key key, under the hard timeout and the circuit breaker of the
// revocations. It returns the confirmation on a 2xx (201 new, 200 the same
// key again), ErrRevocationRejected on a final refusal, ErrRevocationAmbiguous
// when the outcome is unknown and ErrRevocationNotSent when the circuit is
// open. Retrying with the same key is always safe.
func (c *Client) RevokeConsent(ctx context.Context, n RevocationNotice, key string) (RevocationConfirmation, error) {
	if key == "" || n.ConsentID == "" || n.EntityCode == "" || n.CustomerID == "" || n.EffectiveAt.IsZero() {
		return RevocationConfirmation{}, ErrInvalidRevocation
	}
	ans, err := resilience.Execute(c.revocations, func() (revocationAnswer, error) { return c.revoke(ctx, n, key) })
	switch {
	case errors.Is(err, resilience.ErrOpen):
		return RevocationConfirmation{}, ErrRevocationNotSent
	case err != nil:
		return RevocationConfirmation{}, fmt.Errorf("%w: %w", ErrRevocationAmbiguous, err)
	case ans.rejectedWith != 0:
		return RevocationConfirmation{}, fmt.Errorf("%w (status %d)", ErrRevocationRejected, ans.rejectedWith)
	}
	return ans.confirmation, nil
}

// RevocationsBreakerState exposes the circuit state of the revocations.
func (c *Client) RevocationsBreakerState() string { return c.revocations.State() }

func (c *Client) revoke(ctx context.Context, n RevocationNotice, key string) (revocationAnswer, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	payload, err := json.Marshal(n)
	if err != nil {
		return revocationAnswer{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/consent-revocations", bytes.NewReader(payload))
	if err != nil {
		return revocationAnswer{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	resp, err := c.http.Do(req)
	if err != nil {
		// Refused, cut or timed out: the provider may have recorded it.
		return revocationAnswer{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated:
		var conf RevocationConfirmation
		if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&conf); err != nil {
			// The answer was cut: a retry with the same key returns it.
			return revocationAnswer{}, fmt.Errorf("decode the revocation confirmation: %w", err)
		}
		return revocationAnswer{confirmation: conf}, nil
	case retryableStatus(resp.StatusCode):
		return revocationAnswer{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	default:
		return revocationAnswer{rejectedWith: resp.StatusCode}, nil
	}
}

// retryableStatus tells whether an answer leaves the outcome unknown: a
// server error, or a 4xx that says "not now" (timeout, a request with the
// same key in progress, too early or too many requests). Any other 4xx is a
// final rejection. Same rule as adapters/payments.
func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly, http.StatusTooManyRequests:
		return true
	}
	return code < 400 || code >= 500
}
