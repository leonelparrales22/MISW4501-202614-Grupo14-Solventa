// Package policies is the adapter that isolates the internal model from the
// policy core, the system of record of the insured's policies (HU-W2.1-01).
// It enforces the hard per-dependency timeout (700 ms) and protects the caller
// with a circuit breaker, so a slow or failing core is cut off instead of
// holding the filing of a claim.
package policies

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

// DefaultTimeout is the hard cut for the policy core dependency.
const DefaultTimeout = 700 * time.Millisecond

const maxBodyBytes = 1 << 20

// ErrSourceUnavailable means the policy core did not give a usable answer:
// timeout, connection error, unexpected status, malformed body or open
// circuit.
var ErrSourceUnavailable = errors.New("the policy core is unavailable")

// ErrInvalidHolder is returned for an empty holder identifier. It is a caller
// error and never counts against the circuit breaker.
var ErrInvalidHolder = errors.New("holder id is required")

// Policy is one policy of a holder, as the core describes it. Line, Status and
// CoveredIncidents keep the core's own codes; the caller maps them.
type Policy struct {
	PolicyID         string    `json:"policy_id"`
	Line             string    `json:"line"`
	Product          string    `json:"product"`
	InsuredObject    string    `json:"insured_object"`
	Status           string    `json:"status"`
	ValidFrom        time.Time `json:"valid_from"`
	ValidTo          time.Time `json:"valid_to"`
	CoveredIncidents []string  `json:"covered_incidents"`
}

// Config configures the client.
type Config struct {
	BaseURL string
	Timeout time.Duration
	Breaker resilience.Config
	// HTTPClient, when set, carries the calls so several adapters can share one
	// connection pool. Timeout above still bounds every call. When nil, a
	// client with Timeout is created.
	HTTPClient *http.Client
}

// Client calls the policy core through a circuit breaker.
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
		breaker: resilience.NewBreaker("policy-core", cfg.Breaker),
	}
}

// BreakerState exposes the circuit state ("closed", "open", "half-open").
func (c *Client) BreakerState() string {
	return c.breaker.State()
}

// HolderPolicies returns every policy of the holder, whatever its status. A
// holder the core does not know has no policies: the answer is an empty list,
// not an error. Every failure of the core is reported as ErrSourceUnavailable.
func (c *Client) HolderPolicies(ctx context.Context, holderID string) ([]Policy, error) {
	if holderID == "" {
		return nil, ErrInvalidHolder
	}
	policies, err := resilience.Execute(c.breaker, func() ([]Policy, error) {
		return c.fetch(ctx, holderID)
	})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSourceUnavailable, err)
	}
	return policies, nil
}

type policiesBody struct {
	Policies []Policy `json:"policies"`
}

func (c *Client) fetch(ctx context.Context, holderID string) ([]Policy, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	endpoint := c.baseURL + "/holders/" + url.PathEscape(holderID) + "/policies"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return []Policy{}, nil
	default:
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	var body policiesBody
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if body.Policies == nil {
		return nil, errors.New("decode response: the policies list is missing")
	}
	return body.Policies, nil
}
