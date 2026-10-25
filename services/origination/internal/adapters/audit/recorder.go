package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// decisionsPath is the internal route of audit that stores a decision
// (HU-W4.1-01).
const decisionsPath = "/v1/audit/decisions"

// Recorder implements ports.DecisionRecorder over HTTP. Like the Verifier it
// bounds every call with a timeout and a circuit breaker; audit answers
// idempotently by offer id, so a repeated call is harmless.
type Recorder struct {
	http    *http.Client
	baseURL string
	timeout time.Duration
	breaker *resilience.Breaker
}

var _ ports.DecisionRecorder = (*Recorder)(nil)

// NewRecorder builds the recorder from the same configuration as the Verifier.
func NewRecorder(cfg Config) *Recorder {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &Recorder{
		http: client, baseURL: strings.TrimRight(cfg.BaseURL, "/"), timeout: timeout,
		breaker: resilience.NewBreaker("audit-decisions", cfg.Breaker),
	}
}

type decisionSourceBody struct {
	Source      string     `json:"source"`
	Status      string     `json:"status"`
	LatencyMS   int64      `json:"latency_ms"`
	ConsultedAt *time.Time `json:"consulted_at"`
	Score       *float64   `json:"score"`
	Category    string     `json:"category"`
}

type decisionRuleBody struct {
	Order int     `json:"order"`
	Code  string  `json:"code"`
	Value float64 `json:"value"`
}

type decisionBody struct {
	OfferID       string               `json:"offer_id"`
	CustomerID    string               `json:"customer_id"`
	PartnerID     string               `json:"partner_id"`
	ApplicationID string               `json:"application_id"`
	ConsentID     string               `json:"consent_id"`
	Channel       string               `json:"channel"`
	RequestedAt   time.Time            `json:"requested_at"`
	RaterVersion  string               `json:"rater_version"`
	Sources       []decisionSourceBody `json:"sources"`
	Profile       struct {
		Score    float64 `json:"score"`
		Category string  `json:"category"`
		Partial  bool    `json:"partial"`
	} `json:"profile"`
	Rules          []decisionRuleBody `json:"rules"`
	Premium        float64            `json:"premium"`
	Currency       string             `json:"currency"`
	Decision       string             `json:"decision"`
	DecisionReason string             `json:"decision_reason"`
}

// Record sends the decision to audit. Any status other than 200 or 201 is an
// error.
func (r *Recorder) Record(ctx context.Context, d ports.DecisionRecord) error {
	body := decisionBody{
		OfferID: d.OfferID, CustomerID: d.CustomerID, PartnerID: d.PartnerID, ApplicationID: d.ApplicationID, ConsentID: d.ConsentID,
		Channel: d.Channel, RequestedAt: d.RequestedAt, RaterVersion: d.RaterVersion,
		Sources: []decisionSourceBody{}, Rules: []decisionRuleBody{},
		Premium: d.Premium, Currency: d.Currency, Decision: d.Decision, DecisionReason: d.DecisionReason,
	}
	body.Profile.Score, body.Profile.Category, body.Profile.Partial = d.ProfileScore, d.ProfileCategory, d.ProfilePartial
	for _, s := range d.Sources {
		entry := decisionSourceBody{Source: s.Source, Status: s.Status, LatencyMS: s.LatencyMS, Score: s.Score, Category: s.Category}
		if !s.ConsultedAt.IsZero() {
			at := s.ConsultedAt.UTC()
			entry.ConsultedAt = &at
		}
		body.Sources = append(body.Sources, entry)
	}
	for _, rule := range d.Rules {
		body.Rules = append(body.Rules, decisionRuleBody{Order: rule.Order, Code: rule.Code, Value: rule.Value})
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode the decision: %w", err)
	}
	_, err = resilience.Execute(r.breaker, func() (struct{}, error) { return struct{}{}, r.post(ctx, raw) })
	return err
}

func (r *Recorder) post(ctx context.Context, raw []byte) error {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.baseURL+decisionsPath, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := r.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodyBytes))
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}
