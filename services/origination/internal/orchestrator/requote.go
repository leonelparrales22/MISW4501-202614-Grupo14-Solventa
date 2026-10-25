package orchestrator

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// Defaults of the requote worker (OFFER_REQUOTE_POLL_INTERVAL_MS and
// OFFER_REQUOTE_BATCH).
const (
	DefaultRequotePollInterval = time.Minute
	DefaultRequoteBatch        = 50
)

// RequoteDeps are the collaborators of the RequoteWorker.
type RequoteDeps struct {
	Store ports.PersonalizedOfferStore
	// Acceptances is only read: an offer with an acceptance is never replaced.
	Acceptances ports.AcceptanceRepository
	Consents    ports.ConsentVerifier
	Profiles    ProfileResolver
	Rater       ports.Rater
	Notifier    ports.RequoteNotifier
	Clock       ports.Clock
	// Decisions receives the audit record of each replacement offer
	// (HU-W4.1-01). Nil: nothing is recorded.
	Decisions ports.DecisionRecorder
}

// RequoteConfig tunes the worker. Zero values take the defaults.
type RequoteConfig struct {
	PollInterval time.Duration
	Batch        int
}

func (c RequoteConfig) withDefaults() RequoteConfig {
	if c.PollInterval <= 0 {
		c.PollInterval = DefaultRequotePollInterval
	}
	if c.Batch <= 0 {
		c.Batch = DefaultRequoteBatch
	}
	return c
}

// RequoteWorker detects that the external source came back and replaces the
// provisional offers that are still valid and were not accepted with a normal
// one, then tells the customer (HU-W1.2-03 CA-4, decision D6). A provisional
// offer the customer already accepted is respected: the acceptance keeps the
// price it was made at.
//
// Several tasks of the service may run it at once: Supersede is atomic and
// gives each offer a single replacement, and the notice has an idempotency key
// per replaced offer, so the customer gets one e-mail.
//
// Race with an acceptance: the worker reads the acceptance, then supersedes.
// An acceptance registered in between is returned untouched by the
// acceptance service (it finds the existing acceptance first), but the offer
// is also replaced, so the customer can be told about a new offer for a risk
// they already accepted. The worker checks again before notifying, which
// narrows the window to the instants between the second read and the notice.
type RequoteWorker struct {
	d      RequoteDeps
	cfg    RequoteConfig
	pricer offerPricer

	requoted metric.Int64Counter
	// notices tracks the sends in flight, so a caller (and a test) can wait.
	notices sync.WaitGroup
}

// NewRequoteWorker builds a RequoteWorker.
func NewRequoteWorker(d RequoteDeps, cfg RequoteConfig) *RequoteWorker {
	requoted, _ := observability.Meter(instrumentation).Int64Counter("offers.requoted",
		metric.WithDescription("Provisional offers replaced by a normal one"), metric.WithUnit("{offer}"))
	return &RequoteWorker{d: d, cfg: cfg.withDefaults(), pricer: offerPricer{profiles: d.Profiles, rater: d.Rater, newID: newOfferID}, requoted: requoted}
}

// Run looks for requotable offers every PollInterval until ctx ends.
func (w *RequoteWorker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()
	for {
		w.ProcessOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ProcessOnce examines up to a batch of provisional offers and returns how
// many it replaced.
func (w *RequoteWorker) ProcessOnce(ctx context.Context) int {
	offers, err := w.d.Store.ListRequotable(ctx, w.d.Clock.Now().UTC(), w.cfg.Batch)
	if err != nil {
		slog.ErrorContext(ctx, "could not list the provisional offers", "error", err)
		return 0
	}
	replaced := 0
	for _, o := range offers {
		if ctx.Err() != nil {
			break
		}
		if w.requote(ctx, o) {
			replaced++
		}
	}
	return replaced
}

// Wait blocks until the notices started so far are sent or given up.
func (w *RequoteWorker) Wait() { w.notices.Wait() }

// requote replaces one offer when it can and returns whether it did.
func (w *RequoteWorker) requote(ctx context.Context, old domain.PersonalizedOffer) bool {
	if w.accepted(ctx, old.ID) {
		return false
	}
	if _, err := verifyConsent(ctx, w.d.Consents, old.CustomerID); err != nil {
		// No longer consented, or audit does not answer: nothing is queried.
		slog.InfoContext(ctx, "provisional offer not requoted: consent not verified", "offer_id", old.ID, "reason", err.Error())
		return false
	}

	now := w.d.Clock.Now().UTC()
	// The profile is the one of the customer; the sources are asked again
	// because a partial profile is never reused by the resolver.
	offer, err := w.pricer.build(ctx, domain.PersonalizedOffer{
		CustomerID: old.CustomerID, Channel: old.Channel, QuoteID: old.QuoteID, ConsentID: old.ConsentID,
		PartnerID: old.PartnerID, ApplicationID: old.ApplicationID,
		Applicant: old.Applicant, BirthDate: old.BirthDate,
		Terms: domain.CoverageTerms{InsuredAmount: old.Terms.InsuredAmount, TermMonths: old.Terms.TermMonths},
		// Supersedes is set by the store; the new offer points to the old one.
		Supersedes: old.ID,
	}, now)
	if err != nil {
		slog.WarnContext(ctx, "provisional offer not requoted: could not price it", "offer_id", old.ID, "error", err)
		return false
	}
	if offer.Mode != domain.ModeNormal {
		// The source is still down: the provisional offer stays as it is.
		return false
	}
	created, err := w.d.Store.Supersede(ctx, old.ID, offer)
	if err != nil {
		slog.ErrorContext(ctx, "could not replace the provisional offer", "offer_id", old.ID, "error", err)
		return false
	}
	if !created {
		return false
	}

	recordDecision(ctx, w.d.Decisions, offer)
	w.requoted.Add(ctx, 1, metric.WithAttributes(attribute.String("profile_source", string(offer.ProfileSource))))
	slog.InfoContext(ctx, "provisional offer replaced", "offer_id", old.ID, "new_offer_id", offer.ID,
		"old_premium", old.Premium, "new_premium", offer.Premium)
	if !w.accepted(ctx, old.ID) {
		w.notify(ctx, old, offer)
	}
	return true
}

// accepted tells whether the offer has an acceptance. When that cannot be
// told, the offer is treated as accepted: it is better to leave a provisional
// offer as it is than to replace one the customer accepted.
func (w *RequoteWorker) accepted(ctx context.Context, offerID string) bool {
	_, err := w.d.Acceptances.GetByOffer(ctx, offerID)
	switch {
	case err == nil:
		return true
	case errors.Is(err, ports.ErrAcceptanceNotFound):
		return false
	default:
		slog.WarnContext(ctx, "could not read the acceptance of a provisional offer", "offer_id", offerID, "error", err)
		return true
	}
}

// notify sends the notice in the background: the adapter retries with a
// growing wait, and the poll must not wait for it.
func (w *RequoteWorker) notify(ctx context.Context, old, replacement domain.PersonalizedOffer) {
	if w.d.Notifier == nil {
		return
	}
	notice := domain.RequoteNotice{
		To: old.Applicant.Email, CustomerName: old.Applicant.FullName,
		OldOfferID: old.ID, NewOfferID: replacement.ID, OldPremium: old.Premium, NewPremium: replacement.Premium,
		Currency: replacement.Currency, ExpiresAt: replacement.ExpiresAt,
		IdempotencyKey: domain.RequoteIdempotencyKey(old.ID),
	}
	// Detached from the poll, but not from its values (trace and correlation ids).
	bg := context.WithoutCancel(ctx)
	w.notices.Add(1)
	go func() {
		defer w.notices.Done()
		if err := w.d.Notifier.Notify(bg, notice); err != nil {
			slog.WarnContext(bg, "requote notice not sent; the customer will see the new offer when they return",
				"offer_id", old.ID, "new_offer_id", replacement.ID, "error", err)
		}
	}()
}
