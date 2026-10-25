// Package ports declares what the audit core needs from the outside. The
// adapters implement it (AGENTS.md §2).
package ports

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

// ErrConsentNotFound is returned by a ConsentStore that has no consent with
// the requested id.
var ErrConsentNotFound = errors.New("consent not found")

// ConsentStore keeps the consent records. It is append-only (HU-W1.2-01
// criterion 4): it has no operation to update or delete a record, and the
// PostgreSQL adapter also rejects them in the database. A future revocation
// will be a new record, never an update.
type ConsentStore interface {
	// Create stores the consent. It is idempotent by (customer_id,
	// application_id, terms_version): when a consent for that key exists it
	// returns the stored one with created=false and writes nothing.
	Create(ctx context.Context, c domain.Consent) (stored domain.Consent, created bool, err error)
	// Get returns the consent or ErrConsentNotFound.
	Get(ctx context.Context, id string) (domain.Consent, error)
	// LatestByCustomer returns the most recently granted consent of the
	// customer, or ErrConsentNotFound.
	LatestByCustomer(ctx context.Context, customerID string) (domain.Consent, error)
	// ListByHolder returns every consent granted with the document, most
	// recently granted first (HU-M1.3-01). The document is normalized
	// (domain.NormalizeDocument) and compared without case.
	ListByHolder(ctx context.Context, document string) ([]domain.Consent, error)
}

// ConsentEventReader reads the append-only consent_event table: the changes
// of state of the consents (HU-M1.3-01). HU-M1.3-02 writes them through the
// RevocationStore and the NoticeStore.
type ConsentEventReader interface {
	// ListByConsents returns the events of each consent, keyed by consent id.
	// A consent without events has no key.
	ListByConsents(ctx context.Context, consentIDs []string) (map[string][]domain.ConsentEvent, error)
}

// ErrHolderNotLinked is returned when the account (subject) has no holder
// link yet: origination has not sent its CustomerLinked event, or the account
// never went through the registration (decision D-13 a).
var ErrHolderNotLinked = errors.New("the account is not linked to a holder")

// HolderLinkStore keeps the links between accounts and documents
// (HU-M1.3-01). It is append-only: a link is never updated nor deleted.
type HolderLinkStore interface {
	// Link inserts the link unless the subject already has one: then it
	// returns the stored link with created=false and writes nothing.
	Link(ctx context.Context, l domain.HolderLink) (stored domain.HolderLink, created bool, err error)
	// DocumentOf returns the normalized document linked to the subject, or
	// ErrHolderNotLinked.
	DocumentOf(ctx context.Context, subject string) (string, error)
}

// Clock gives the server time, the only source of granted_at.
type Clock interface {
	Now() time.Time
}

// ErrRevocationNotFound is returned when a consent has no revocation.
var ErrRevocationNotFound = errors.New("revocation not found")

// RevocationStore keeps the immutable revocations (HU-M1.3-02 criterion 4).
// It has no operation to update or delete one, and the PostgreSQL adapter
// rejects them in the database too.
type RevocationStore interface {
	// Record stores, in one transaction and before the holder gets the
	// confirmation, the revocation with a receipt number taken from a
	// sequence, the revocation_requested and revoked events of the consent
	// and, for each notice, its header and its first pending step. It is
	// idempotent by rev.RequestID: when that request already revoked the
	// same consent it returns the stored revocation with created=false and
	// writes nothing. It returns domain.ErrAlreadyRevoked when the consent
	// has another revocation and domain.ErrIdempotencyKeyReused when the
	// request id revoked another consent. If any write fails, nothing is
	// kept.
	Record(ctx context.Context, rev domain.Revocation, notices []NoticeRecord) (stored domain.Revocation, created bool, err error)
	// GetByConsent returns the revocation of the consent, or
	// ErrRevocationNotFound.
	GetByConsent(ctx context.Context, consentID string) (domain.Revocation, error)
}

// NoticeRecord is the notice of a revocation to one entity, as the notice
// worker processes it with pkg/idempotency. Every change of state of a notice
// is a new step (Seq), never an update: the database of audit is append-only
// (decision D-3 a).
type NoticeRecord struct {
	ConsentID  string
	EntityCode string
	// Key is the idempotency key sent to the entity on every attempt.
	Key            string
	Seq            int
	Status         idempotency.Status
	FailedAttempts int
	WindowEndsAt   time.Time
	NextAttemptAt  time.Time
	ClaimedUntil   time.Time
	Reference      string
	Cause          string
	// CustomerID is the customer of the partner the consent was granted to;
	// EffectiveAt is when the revocation took effect. They are what the
	// entity is told (data minimization: no document nor name).
	CustomerID  string
	EffectiveAt time.Time
}

// ID is the identifier of the notice for the idempotency store.
func (r NoticeRecord) ID() string { return NoticeID(r.ConsentID, r.EntityCode) }

// AttemptState implements idempotency.Record.
func (r NoticeRecord) AttemptState() idempotency.Attempt {
	return idempotency.Attempt{
		ID: r.ID(), Key: r.Key, Status: r.Status, FailedAttempts: r.FailedAttempts,
		WindowEndsAt: r.WindowEndsAt, NextAttemptAt: r.NextAttemptAt, ClaimedUntil: r.ClaimedUntil,
	}
}

// noticeIDSeparator joins a consent id and an entity code; neither of them
// can contain it.
const noticeIDSeparator = "/"

// NoticeID returns the identifier of the notice of the consent to the entity.
func NoticeID(consentID, entityCode string) string { return consentID + noticeIDSeparator + entityCode }

// ParseNoticeID splits a notice identifier; ok is false when it is not one.
func ParseNoticeID(id string) (consentID, entityCode string, ok bool) {
	consentID, entityCode, ok = strings.Cut(id, noticeIDSeparator)
	return consentID, entityCode, ok && consentID != "" && entityCode != ""
}

// NoticeStore keeps the notices of the revocations to the entities. Besides
// the idempotency.Store operations (each one inserts a step), it lists the
// notices that are due and resumes the ones that did not confirm. Confirm
// also writes the revocation_confirmed event of the entity, and Fail its
// revocation_not_confirmed event, in the same transaction as the step: the
// derived status never misses a result.
type NoticeStore interface {
	idempotency.Store[NoticeRecord]
	// Due returns the ids of the notices whose last step is pending and due
	// at now, or in progress with an expired lease, at most limit.
	Due(ctx context.Context, now time.Time, limit int) ([]string, error)
	// Resume starts a new window, from now and window long, for the given
	// entities of the consent (every entity when empty) that did not
	// confirm: their last step failed, or is pending and its window already
	// ended. It writes a revocation_retry_requested event per entity, by the
	// actor, in the same transaction, and returns the entities resumed. A
	// confirmed notice is never resumed (HU-W4.2-02, decision D-8 a).
	Resume(ctx context.Context, consentID string, entities []string, now time.Time, window time.Duration, actor domain.Actor) ([]string, error)
}

// EntityNotifier tells one entity that the consent was revoked, sending the
// idempotency key of the notice. Its errors wrap the classification of
// pkg/idempotency (ErrRejected, ErrAmbiguous, ErrNotSent).
type EntityNotifier interface {
	Notify(ctx context.Context, notice NoticeRecord) (idempotency.Outcome, error)
}

// ReceiptRenderer renders the receipt of a revocation (criterion 4) in the
// language given ("es" or "en"). The same revocation gives the same bytes.
type ReceiptRenderer interface {
	Render(rev domain.Revocation, consent domain.Consent, lang string) ([]byte, error)
}

// DecisionStore keeps the underwriting decision records (HU-W4.1-01). It is
// append-only: it has no operation to update or delete a record, and the
// PostgreSQL adapter also rejects them in the database.
type DecisionStore interface {
	// Create stores the record. It is idempotent by offer id: when a record
	// for that offer exists it returns the stored one with created=false and
	// writes nothing.
	Create(ctx context.Context, r domain.DecisionRecord) (stored domain.DecisionRecord, created bool, err error)
	// Get returns the record of the offer or domain.ErrDecisionNotFound.
	Get(ctx context.Context, offerID string) (domain.DecisionRecord, error)
}
