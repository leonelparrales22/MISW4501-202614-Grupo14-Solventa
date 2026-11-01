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

// ErrInvalidPolicy is returned for an empty policy identifier. Like
// ErrInvalidHolder, it never reaches the core.
var ErrInvalidPolicy = errors.New("policy id is required")

// ErrPolicyNotFound means the core answered that the holder has no such
// policy: it does not exist or belongs to someone else, which the core does
// not tell apart on purpose. It is an answer, not a failure, so it does not
// count against the circuit breaker.
var ErrPolicyNotFound = errors.New("the holder has no such policy")

// ErrHolderNotFound means the core does not know the holder.
var ErrHolderNotFound = errors.New("the policy core does not know the holder")

// Policy is one policy of a holder, as the core describes it. Line, Status and
// CoveredIncidents keep the core's own codes; the caller maps them.
type Policy struct {
	PolicyID         string     `json:"policy_id"`
	Line             string     `json:"line"`
	Product          string     `json:"product"`
	InsuredObject    string     `json:"insured_object"`
	Status           string     `json:"status"`
	ValidFrom        time.Time  `json:"valid_from"`
	ValidTo          time.Time  `json:"valid_to"`
	CoveredIncidents []string   `json:"covered_incidents"`
	Coverages        []Coverage `json:"coverages"`
}

// Coverage is what the policy pays for one covered incident (HU-W2.2-03).
// InsuredValueMinor is in minor units of Currency; DeductibleBasisPoints is the
// share of the approved amount that is not paid (500 is 5 %).
type Coverage struct {
	Incident              string `json:"incident"`
	InsuredValueMinor     int64  `json:"insured_value_minor"`
	Currency              string `json:"currency"`
	DeductibleBasisPoints int    `json:"deductible_basis_points"`
}

// Coverage returns the coverage of the incident, or false when the policy
// does not cover it.
func (p Policy) Coverage(incident string) (Coverage, bool) {
	for _, c := range p.Coverages {
		if c.Incident == incident {
			return c, true
		}
	}
	return Coverage{}, false
}

// Holder is the profile of a policyholder: who the insured is, where they are
// notified and the account an indemnity is paid to (HU-W2.2-03).
type Holder struct {
	HolderID      string        `json:"holder_id"`
	FullName      string        `json:"full_name"`
	DocumentType  string        `json:"document_type"`
	DocumentID    string        `json:"document_id"`
	Email         string        `json:"email"`
	Phone         string        `json:"phone"`
	PayoutAccount PayoutAccount `json:"payout_account"`
}

// PayoutAccount is the bank account registered to receive indemnities.
type PayoutAccount struct {
	BankCode      string `json:"bank_code"`
	BankName      string `json:"bank_name"`
	AccountType   string `json:"account_type"`
	AccountNumber string `json:"account_number"`
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

// HolderPolicy returns one policy of the holder, with the terms of each
// coverage. ErrPolicyNotFound means the holder has no such policy; every
// failure of the core is reported as ErrSourceUnavailable.
func (c *Client) HolderPolicy(ctx context.Context, holderID, policyID string) (Policy, error) {
	switch {
	case holderID == "":
		return Policy{}, ErrInvalidHolder
	case policyID == "":
		return Policy{}, ErrInvalidPolicy
	}
	path := "/holders/" + url.PathEscape(holderID) + "/policies/" + url.PathEscape(policyID)
	got, err := resilience.Execute(c.breaker, func() (lookup[Policy], error) { return getOne[Policy](ctx, c, path) })
	switch {
	case err != nil:
		return Policy{}, fmt.Errorf("%w: %w", ErrSourceUnavailable, err)
	case !got.found:
		return Policy{}, ErrPolicyNotFound
	}
	return got.value, nil
}

// Holder returns the profile of the holder. ErrHolderNotFound means the core
// does not know them; every failure of the core is ErrSourceUnavailable.
func (c *Client) Holder(ctx context.Context, holderID string) (Holder, error) {
	if holderID == "" {
		return Holder{}, ErrInvalidHolder
	}
	path := "/holders/" + url.PathEscape(holderID)
	got, err := resilience.Execute(c.breaker, func() (lookup[Holder], error) { return getOne[Holder](ctx, c, path) })
	switch {
	case err != nil:
		return Holder{}, fmt.Errorf("%w: %w", ErrSourceUnavailable, err)
	case !got.found:
		return Holder{}, ErrHolderNotFound
	}
	return got.value, nil
}

type policiesBody struct {
	Policies []Policy `json:"policies"`
}

func (c *Client) fetch(ctx context.Context, holderID string) ([]Policy, error) {
	got, err := getOne[policiesBody](ctx, c, "/holders/"+url.PathEscape(holderID)+"/policies")
	switch {
	case err != nil:
		return nil, err
	case !got.found:
		return []Policy{}, nil
	case got.value.Policies == nil:
		return nil, errors.New("decode response: the policies list is missing")
	}
	return got.value.Policies, nil
}

// lookup is the answer of a GET: the value, or found == false for a 404. A
// 404 is a valid answer of a healthy core, so it is not an error and does not
// count against the circuit breaker.
type lookup[T any] struct {
	value T
	found bool
}

func getOne[T any](ctx context.Context, c *Client, path string) (lookup[T], error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return lookup[T]{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return lookup[T]{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return lookup[T]{}, nil
	default:
		return lookup[T]{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	var value T
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&value); err != nil {
		return lookup[T]{}, fmt.Errorf("decode response: %w", err)
	}
	return lookup[T]{value: value, found: true}, nil
}
