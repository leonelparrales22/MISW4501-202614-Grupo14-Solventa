// Package claims is the adapter that asks the claims service, inside the VPC,
// how many claims of a policy are in progress (HU-W1.4-02, decision D-6 a),
// before an operations analyst applies an endorsement that a claim in
// progress blocks. Like the verifier of audit it bounds every call with a
// short timeout and a circuit breaker; the caller treats any failure as an
// unverifiable validation and changes nothing.
package claims

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
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// DefaultTimeout is the cut of each query (CLAIMS_TIMEOUT_MS).
const DefaultTimeout = 300 * time.Millisecond

const maxBodyBytes = 1 << 12

// ErrUnavailable means claims gave no usable answer: timeout, connection
// error, a status other than 200, a malformed body or an open circuit.
var ErrUnavailable = errors.New("the claims in progress cannot be verified")

// Config configures the client.
type Config struct {
	// BaseURL is the claims service (CLAIMS_URL); the path is added here.
	BaseURL string
	// Timeout bounds each query. Zero means DefaultTimeout.
	Timeout time.Duration
	Breaker resilience.Config
	// HTTPClient carries the calls; a client with Timeout is created when nil.
	HTTPClient *http.Client
}

// OpenClaims implements ports.OpenClaimsChecker over HTTP.
type OpenClaims struct {
	http    *http.Client
	baseURL string
	timeout time.Duration
	breaker *resilience.Breaker
}

var _ ports.OpenClaimsChecker = (*OpenClaims)(nil)

// NewOpenClaims builds the client.
func NewOpenClaims(cfg Config) *OpenClaims {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &OpenClaims{http: client, baseURL: strings.TrimRight(cfg.BaseURL, "/"), timeout: timeout,
		breaker: resilience.NewBreaker("claims-open-claims", cfg.Breaker)}
}

// BreakerState exposes the circuit state ("closed", "open", "half-open").
func (c *OpenClaims) BreakerState() string { return c.breaker.State() }

type openClaimsBody struct {
	PolicyID   string `json:"policy_id"`
	OpenClaims *int   `json:"open_claims"`
}

// OpenClaims returns the claims in progress of the policy. Every failure is
// ErrUnavailable.
func (c *OpenClaims) OpenClaims(ctx context.Context, policyID string) (int, error) {
	n, err := resilience.Execute(c.breaker, func() (int, error) { return c.fetch(ctx, policyID) })
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return n, nil
}

func (c *OpenClaims) fetch(ctx context.Context, policyID string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	endpoint := c.baseURL + "/internal/policies/" + url.PathEscape(policyID) + "/open-claims"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	var body openClaimsBody
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&body); err != nil {
		return 0, fmt.Errorf("decode response: %w", err)
	}
	if body.OpenClaims == nil || *body.OpenClaims < 0 || body.PolicyID != policyID {
		return 0, errors.New("the answer is not the count of the policy")
	}
	return *body.OpenClaims, nil
}
