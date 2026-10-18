// Package audit is the adapter that asks the audit service whether a customer
// has an active explicit consent (HU-W1.2-01). It bounds every call with a
// short timeout and a circuit breaker, and reports any failure as
// ports.ErrConsentUnverifiable so the caller fails closed.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// DefaultTimeout is the cut of each verification (AUDIT_TIMEOUT_MS).
const DefaultTimeout = 300 * time.Millisecond

const (
	verificationsPath = "/v1/audit/consent-verifications"
	maxBodyBytes      = 1 << 16
)

// Config configures the verifier.
type Config struct {
	// BaseURL is the audit service (AUDIT_URL); the path is added here.
	BaseURL string
	// Timeout bounds each verification. Zero means DefaultTimeout.
	Timeout time.Duration
	Breaker resilience.Config
	// HTTPClient carries the calls; a client with Timeout is created when nil.
	HTTPClient *http.Client
}

// Verifier implements ports.ConsentVerifier over HTTP.
type Verifier struct {
	http    *http.Client
	baseURL string
	timeout time.Duration
	breaker *resilience.Breaker
}

var _ ports.ConsentVerifier = (*Verifier)(nil)

// NewVerifier builds the verifier.
func NewVerifier(cfg Config) *Verifier {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &Verifier{
		http:    client,
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		timeout: timeout,
		breaker: resilience.NewBreaker("audit-consent", cfg.Breaker),
	}
}

// BreakerState exposes the circuit state ("closed", "open", "half-open").
func (v *Verifier) BreakerState() string { return v.breaker.State() }

type verificationBody struct {
	Active    bool       `json:"active"`
	ConsentID *string    `json:"consent_id"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// Verify asks audit about the customer. Every failure (timeout, connection,
// status other than 200, malformed answer, open circuit) is
// ports.ErrConsentUnverifiable.
func (v *Verifier) Verify(ctx context.Context, customerID string) (domain.ConsentStatus, error) {
	status, err := resilience.Execute(v.breaker, func() (domain.ConsentStatus, error) {
		return v.fetch(ctx, customerID)
	})
	if err != nil {
		return domain.ConsentStatus{}, fmt.Errorf("%w: %w", ports.ErrConsentUnverifiable, err)
	}
	return status, nil
}

func (v *Verifier) fetch(ctx context.Context, customerID string) (domain.ConsentStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, v.timeout)
	defer cancel()

	endpoint := v.baseURL + verificationsPath + "?customer_id=" + url.QueryEscape(customerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return domain.ConsentStatus{}, err
	}
	resp, err := v.http.Do(req)
	if err != nil {
		return domain.ConsentStatus{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return domain.ConsentStatus{}, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	var body verificationBody
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(&body); err != nil {
		return domain.ConsentStatus{}, fmt.Errorf("decode the verification: %w", err)
	}
	out := domain.ConsentStatus{Active: body.Active}
	if body.ConsentID != nil {
		out.ConsentID = *body.ConsentID
	}
	if body.ExpiresAt != nil {
		out.ExpiresAt = body.ExpiresAt.UTC()
	}
	return out, nil
}
