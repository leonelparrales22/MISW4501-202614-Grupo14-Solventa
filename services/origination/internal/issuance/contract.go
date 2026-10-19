package issuance

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Defaults of the contractual issuance (HU-W1.3-03).
const (
	DefaultContractPollInterval   = 500 * time.Millisecond
	DefaultContractAttemptTimeout = 10 * time.Second
	DefaultContractRetryBase      = time.Second
	DefaultContractRetryMax       = 15 * time.Second
	DefaultContractMaxAttempts    = 12
	DefaultDocumentLinkTTL        = 7 * 24 * time.Hour
	contractBatchSize             = 50
	contentTypePDF                = "application/pdf"
	contractInstrumentation       = "origination/issuance"
)

// ContractConfig tunes the contractual issuance. Zero values take the defaults.
type ContractConfig struct {
	// PollInterval is how often due issuances are looked up (a received
	// activation wakes the worker at once).
	PollInterval time.Duration
	// AttemptTimeout bounds one processing attempt.
	AttemptTimeout time.Duration
	// RetryBase is the wait after the first failed attempt; it doubles after
	// each failure up to RetryMax. With the defaults several attempts fit in
	// the 60 seconds of the SLO.
	RetryBase time.Duration
	RetryMax  time.Duration
	// MaxAttempts is how many failed attempts the notification gets before the
	// issuance is marked failed (the document stays available).
	MaxAttempts int
	// LinkTTL is how long the secure link of the notification works.
	LinkTTL time.Duration
	// PublicBaseURL is where the document link points, e.g.
	// https://api.solventa.example (no trailing slash).
	PublicBaseURL string
}

func (c ContractConfig) withDefaults() ContractConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = DefaultContractPollInterval
	}
	if c.AttemptTimeout <= 0 {
		c.AttemptTimeout = DefaultContractAttemptTimeout
	}
	if c.RetryBase <= 0 {
		c.RetryBase = DefaultContractRetryBase
	}
	if c.RetryMax < c.RetryBase {
		c.RetryMax = max(DefaultContractRetryMax, c.RetryBase)
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = DefaultContractMaxAttempts
	}
	if c.LinkTTL <= 0 {
		c.LinkTTL = DefaultDocumentLinkTTL
	}
	c.PublicBaseURL = strings.TrimRight(c.PublicBaseURL, "/")
	return c
}

// ContractDeps are the ports the contractual issuance needs.
type ContractDeps struct {
	Repo      ports.ContractRepository
	Generator ports.DocumentGenerator
	Store     ports.DocumentStore
	Signer    ports.LinkSigner
	Notifier  ports.Notifier
	Audit     ports.IssuanceAudit
	Clock     ports.Clock
}

// ContractService issues the contractual document of a policy when its
// coverage is activated and notifies the insured (HU-W1.3-03). Receive only
// persists the activation; Run turns it into a stored document and a
// notification asynchronously, retrying failed steps with a growing wait, so
// no step needs manual intervention and none is repeated once it finished.
type ContractService struct {
	d    ContractDeps
	cfg  ContractConfig
	wake chan struct{}

	available metric.Float64Histogram
	completed metric.Int64Counter
	breaches  metric.Int64Counter
	failures  metric.Int64Counter
}

var _ ports.CoverageActivationHandler = (*ContractService)(nil)

// NewContractService builds a ContractService.
func NewContractService(deps ContractDeps, cfg ContractConfig) *ContractService {
	m := observability.Meter(contractInstrumentation)
	available, _ := m.Float64Histogram("contract.activation_to_available",
		metric.WithDescription("Seconds from the coverage activation until the contractual document is available (SLO 60 s)"),
		metric.WithUnit("s"))
	completed, _ := m.Int64Counter("contract.issuance.completed", metric.WithDescription("Contractual issuances completed"))
	breaches, _ := m.Int64Counter("contract.slo.breached", metric.WithDescription("Documents available later than the 60 s SLO"))
	failures, _ := m.Int64Counter("contract.attempt.failed", metric.WithDescription("Failed issuance attempts, by step"))
	return &ContractService{
		d: deps, cfg: cfg.withDefaults(), wake: make(chan struct{}, 1),
		available: available, completed: completed, breaches: breaches, failures: failures,
	}
}

// Receive registers the activation. It is idempotent by policy: a redelivered
// event changes nothing.
func (s *ContractService) Receive(ctx context.Context, ev domain.CoverageActivated) error {
	if err := ev.Validate(); err != nil {
		return fmt.Errorf("invalid coverage activation: %w", err)
	}
	now := s.d.Clock.Now().UTC()
	_, created, err := s.d.Repo.Create(ctx, domain.ContractIssuance{
		Event: ev, Status: domain.ContractPending,
		NotificationStatus: domain.NotificationPending, NotificationChannel: domain.ChannelEmail,
		NextAttemptAt: now, ReceivedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return fmt.Errorf("could not register the coverage activation: %w", err)
	}
	if created {
		slog.InfoContext(ctx, "coverage activation received", "policy_id", ev.PolicyID, "event_id", ev.EventID)
		s.Notify()
	}
	return nil
}

// Notify asks Run to look for due issuances now. It never blocks.
func (s *ContractService) Notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run processes due issuances every PollInterval and on Notify until ctx ends.
func (s *ContractService) Run(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.PollInterval)
	defer ticker.Stop()
	for {
		s.ProcessDue(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.wake:
		}
	}
}

// ProcessDue runs one attempt for each issuance whose next attempt is due and
// returns how many it attempted.
func (s *ContractService) ProcessDue(ctx context.Context) int {
	due, err := s.d.Repo.ListDue(ctx, s.d.Clock.Now().UTC(), contractBatchSize)
	if err != nil {
		slog.ErrorContext(ctx, "could not list the due contract issuances", "error", err)
		return 0
	}
	for _, c := range due {
		if ctx.Err() != nil {
			break
		}
		s.attempt(ctx, c)
	}
	return len(due)
}

// stepError tells which step of the issuance failed.
type stepError struct {
	step string
	err  error
}

func (e *stepError) Error() string { return e.step + ": " + e.err.Error() }
func (e *stepError) Unwrap() error { return e.err }

func (s *ContractService) attempt(ctx context.Context, c domain.ContractIssuance) {
	actx, cancel := context.WithTimeout(ctx, s.cfg.AttemptTimeout)
	defer cancel()
	actx, span := observability.Tracer(contractInstrumentation).Start(actx, "contract.advance",
		trace.WithAttributes(attribute.String("policy_id", c.Event.PolicyID), attribute.Int("attempt", c.Attempts+1)))
	defer span.End()

	err := s.advance(actx, &c)
	if err == nil {
		return
	}
	span.RecordError(err)
	span.SetStatus(codes.Error, "contract issuance attempt failed")

	var step *stepError
	stepName := "unknown"
	if errors.As(err, &step) {
		stepName = step.step
	}
	s.failures.Add(ctx, 1, metric.WithAttributes(attribute.String("step", stepName)))

	now := s.d.Clock.Now().UTC()
	c.Attempts++
	c.LastFailureAt = now
	c.LastFailure = err.Error()
	c.UpdatedAt = now
	exhausted := c.Attempts >= s.cfg.MaxAttempts
	if exhausted {
		c.Status = domain.ContractFailed
		c.NotificationStatus = failedUnlessSent(c.NotificationStatus)
		c.NextAttemptAt = time.Time{}
	} else {
		c.NextAttemptAt = now.Add(s.backoff(c.Attempts))
	}
	slog.WarnContext(ctx, "contract issuance attempt failed", "policy_id", c.Event.PolicyID, "step", stepName,
		"attempts", c.Attempts, "exhausted", exhausted, "next_attempt_at", c.NextAttemptAt, "error", err)

	if uerr := s.d.Repo.Update(ctx, c); uerr != nil {
		// The issuance stays due, so the next poll retries it.
		slog.ErrorContext(ctx, "could not schedule the next contract attempt", "policy_id", c.Event.PolicyID, "error", uerr)
		return
	}
	if stepName == "notify" {
		s.audit(ctx, domain.IssuanceAuditEntry{
			PolicyID: c.Event.PolicyID, Event: domain.AuditNotificationFailed, At: now,
			DocumentID: c.Document.ID, DocumentVersion: c.Document.Version, Checksum: c.Document.Checksum,
			DeliveryResult: domain.NotificationFailed, Channel: c.NotificationChannel, Attempt: c.Attempts, Detail: err.Error(),
		})
	}
}

func failedUnlessSent(s domain.NotificationStatus) domain.NotificationStatus {
	if s == domain.NotificationSent {
		return s
	}
	return domain.NotificationFailed
}

// advance moves an issuance forward: it generates, stores and associates the
// document if that did not happen yet, then notifies the insured. Each step is
// persisted before the next one, so a resumed issuance only repeats the step
// that failed.
func (s *ContractService) advance(ctx context.Context, c *domain.ContractIssuance) error {
	if c.Document == nil {
		if err := s.issueDocument(ctx, c); err != nil {
			return &stepError{step: "document", err: err}
		}
	}
	if c.NotificationStatus != domain.NotificationSent {
		if err := s.notify(ctx, c); err != nil {
			return &stepError{step: "notify", err: err}
		}
	}
	return nil
}

func (s *ContractService) issueDocument(ctx context.Context, c *domain.ContractIssuance) error {
	ctx, span := observability.Tracer(contractInstrumentation).Start(ctx, "contract.generate")
	defer span.End()

	ev := c.Event
	now := s.d.Clock.Now().UTC()
	const version = 1
	content, err := s.d.Generator.Generate(ctx, domain.ContractData{
		PolicyID: ev.PolicyID, Version: version, Holder: ev.Holder, Premium: ev.Premium, Currency: ev.Currency,
		InsuredAmount: ev.InsuredAmount, Coverages: ev.Coverages,
		CoverageStart: ev.CoverageStart, CoverageEnd: ev.CoverageEnd, ActivatedAt: ev.ActivatedAt, GeneratedAt: now,
	})
	if err != nil {
		return fmt.Errorf("could not generate the document: %w", err)
	}
	id, err := newContractID()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(content)
	doc := domain.ContractDocument{
		ID: id, PolicyID: ev.PolicyID, Version: version, ContentType: contentTypePDF, SizeBytes: len(content),
		StorageKey: fmt.Sprintf("policies/%s/contract-v%d.pdf", ev.PolicyID, version),
		Checksum:   hex.EncodeToString(sum[:]), GeneratedAt: now,
	}
	if err := s.d.Store.Put(ctx, doc.StorageKey, content, doc.ContentType); err != nil {
		return fmt.Errorf("could not store the document: %w", err)
	}

	c.Document = &doc
	c.Status = domain.ContractDocumentReady
	c.AvailableAt = now
	c.UpdatedAt = now
	if err := s.d.Repo.Update(ctx, *c); err != nil {
		c.Document, c.Status, c.AvailableAt = nil, domain.ContractPending, time.Time{}
		return fmt.Errorf("could not associate the document to the policy: %w", err)
	}

	elapsed := now.Sub(ev.ActivatedAt)
	s.available.Record(ctx, elapsed.Seconds())
	within := elapsed <= domain.ContractSLO
	span.SetAttributes(attribute.Float64("activation_to_available_s", elapsed.Seconds()), attribute.Bool("within_slo", within))
	if !within {
		s.breaches.Add(ctx, 1)
		slog.WarnContext(ctx, "contract document available after the SLO", "policy_id", ev.PolicyID, "elapsed_s", elapsed.Seconds())
	}
	s.audit(ctx, domain.IssuanceAuditEntry{
		PolicyID: ev.PolicyID, Event: domain.AuditDocumentGenerated, At: now,
		DocumentID: doc.ID, DocumentVersion: doc.Version, Checksum: doc.Checksum,
		Detail: fmt.Sprintf("%d bytes, available %.1f s after the activation", doc.SizeBytes, elapsed.Seconds()),
	})
	slog.InfoContext(ctx, "contract document available", "policy_id", ev.PolicyID, "version", doc.Version,
		"elapsed_s", elapsed.Seconds(), "within_slo", within)
	return nil
}

func (s *ContractService) notify(ctx context.Context, c *domain.ContractIssuance) error {
	ctx, span := observability.Tracer(contractInstrumentation).Start(ctx, "contract.notify")
	defer span.End()

	now := s.d.Clock.Now().UTC()
	doc := c.Document
	expires := now.Add(s.cfg.LinkTTL)
	link := s.cfg.PublicBaseURL + "/v1/documents/" + s.d.Signer.Sign(doc.PolicyID, doc.Version, expires)
	err := s.d.Notifier.Notify(ctx, domain.IssuedNotification{
		To: c.Event.Holder.Email, HolderName: c.Event.Holder.FullName, PolicyID: c.Event.PolicyID,
		DocumentURL: link, LinkExpiresAt: expires,
		IdempotencyKey: fmt.Sprintf("policy-issued:%s:v%d", c.Event.PolicyID, doc.Version),
	})
	if err != nil {
		return fmt.Errorf("could not notify the insured: %w", err)
	}

	c.NotificationStatus = domain.NotificationSent
	c.NotifiedAt = now
	c.Status = domain.ContractCompleted
	c.NextAttemptAt = time.Time{}
	c.UpdatedAt = now
	if err := s.d.Repo.Update(ctx, *c); err != nil {
		// The message went out; a retry resends it with the same idempotency key.
		c.NotificationStatus, c.Status, c.NotifiedAt = domain.NotificationPending, domain.ContractDocumentReady, time.Time{}
		return fmt.Errorf("could not record the notification: %w", err)
	}
	s.completed.Add(ctx, 1)
	s.audit(ctx, domain.IssuanceAuditEntry{
		PolicyID: c.Event.PolicyID, Event: domain.AuditNotificationSent, At: now,
		DocumentID: doc.ID, DocumentVersion: doc.Version, Checksum: doc.Checksum,
		DeliveryResult: domain.NotificationSent, Channel: c.NotificationChannel, Attempt: c.Attempts + 1,
	})
	slog.InfoContext(ctx, "issuance notification sent", "policy_id", c.Event.PolicyID, "channel", c.NotificationChannel)
	return nil
}

// audit appends an entry. The audit trail never blocks the issuance: a failure
// to write it is logged loudly and the journey goes on.
func (s *ContractService) audit(ctx context.Context, e domain.IssuanceAuditEntry) {
	if err := s.d.Audit.Record(ctx, e); err != nil {
		slog.ErrorContext(ctx, "could not record the issuance audit entry", "policy_id", e.PolicyID, "event", e.Event, "error", err)
	}
}

// backoff returns the wait before the next attempt after the given number of
// failed attempts: RetryBase doubled per failure, capped at RetryMax.
func (s *ContractService) backoff(attempts int) time.Duration {
	wait := s.cfg.RetryBase
	for i := 1; i < attempts && wait < s.cfg.RetryMax; i++ {
		wait *= 2
	}
	return min(wait, s.cfg.RetryMax)
}

func newContractID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("could not generate the document id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
