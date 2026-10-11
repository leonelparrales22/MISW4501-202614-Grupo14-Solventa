// Package kyc is the adapter that isolates the internal model from the KYC
// provider (identity, restrictive lists, AML). It enforces the hard
// per-dependency timeout (700 ms) and protects the caller with a circuit
// breaker, so a slow or failing provider is cut off instead of dragging the
// whole journey down.
package kyc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

// DefaultTimeout is the hard cut for the KYC dependency.
const DefaultTimeout = 700 * time.Millisecond

const maxBodyBytes = 1 << 20

// ErrSourceUnavailable means the provider did not give a usable answer:
// timeout, connection error, non-200 status, malformed body or open circuit.
var ErrSourceUnavailable = errors.New("kyc is unavailable")

// ErrInvalidCustomerID is returned for an empty customer identifier. It is a
// caller error and never counts against the circuit breaker.
var ErrInvalidCustomerID = errors.New("customer id is required")

// Profile is the provider-neutral risk signal returned by KYC.
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

// Client calls the KYC provider through a circuit breaker.
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
		breaker: resilience.NewBreaker("kyc", cfg.Breaker),
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
	p, err := resilience.ExecuteContext(ctx, c.breaker, func() (Profile, error) {
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
