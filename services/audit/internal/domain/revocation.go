package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"
)

// RevocationChannel is the way a revocation was requested (HU-M1.3-02). The
// type is open on purpose: the compliance portal (HU-W4.2-02) adds its own
// constant without changing the record.
type RevocationChannel string

// RevocationChannelMobileApp is the insured's own mobile app.
const RevocationChannelMobileApp RevocationChannel = "mobile_app"

// ActorType says who requested a revocation.
type ActorType string

// ActorHolder is the holder of the consent, identified by the subject of their
// token. HU-W4.2-02 adds the operations role of the compliance portal.
const ActorHolder ActorType = "holder"

// ActorSystem is audit itself: the worker that notifies the entities.
const ActorSystem ActorType = "system"

// Actor is who requested something: a type and the identifier of the account
// (the subject of the user pool), never a name or a document.
type Actor struct {
	Type ActorType
	ID   string
}

// ConfirmationMethod is how the holder confirmed the revocation on the device
// (criterion 1 of HU-M1.3-02): their biometrics or, without them, the password
// of their Solventa account (decision D-6 a).
type ConfirmationMethod string

const (
	ConfirmationBiometric ConfirmationMethod = "biometric"
	ConfirmationPassword  ConfirmationMethod = "password"
)

// EntityNoticeStatus is the state of the notice of a revocation to one
// financial entity.
type EntityNoticeStatus string

const (
	// NoticePending: the entity has not confirmed yet and its window is open.
	NoticePending EntityNoticeStatus = "pending"
	// NoticeConfirmed: the entity confirmed the revocation. Terminal.
	NoticeConfirmed EntityNoticeStatus = "confirmed"
	// NoticeNotConfirmed: the window ended (or the entity rejected the notice)
	// without a confirmation; a retry may still confirm it (HU-W4.2-02).
	NoticeNotConfirmed EntityNoticeStatus = "not_confirmed"
)

// NoticeSummary sums up the notices of a revocation to all its entities.
type NoticeSummary string

const (
	// NoticeInProgress: some entity has not confirmed yet and nothing failed.
	NoticeInProgress NoticeSummary = "in_progress"
	// NoticeCompleted: every entity confirmed.
	NoticeCompleted NoticeSummary = "completed"
	// NoticePartial: some entity did not confirm in its window; the consent is
	// partially revoked.
	NoticePartial NoticeSummary = "partial"
)

// RevocationNoticeDeadline is the time every entity has to confirm a
// revocation (RN-02: at most 5 minutes). The service can shorten it for the
// tests (REVOCATION_NOTICE_DEADLINE_SECONDS).
const RevocationNoticeDeadline = 5 * time.Minute

// Statuses of the history written by the revocation (HU-M1.3-02), besides
// HistoryRevocationRequested and HistoryRevoked of the whole consent. They are
// events of one entity (EntityCode set).
const (
	// HistoryRevocationConfirmed: the entity confirmed the revocation.
	HistoryRevocationConfirmed = "revocation_confirmed"
	// HistoryRevocationNotConfirmed: the window ended, or the entity rejected
	// the notice, without a confirmation.
	HistoryRevocationNotConfirmed = "revocation_not_confirmed"
	// HistoryRevocationRetryRequested: someone asked to notify again an entity
	// that had not confirmed (HU-W4.2-02); a new window starts.
	HistoryRevocationRetryRequested = "revocation_retry_requested"
)

// Limits of a revocation request.
const (
	// MaxDeviceLength bounds the device description sent by the app.
	MaxDeviceLength = 120
	// MaxClockSkew is how far in the future the requested_at of the device may
	// be: the clock of a phone can be a little ahead of the server's.
	MaxClockSkew = 2 * time.Minute
)

// Errors of a revocation that the holder can act upon.
var (
	// ErrConsentNotActive: the consent expired; there is nothing to revoke
	// (criterion 6, HTTP 409 consent_not_active).
	ErrConsentNotActive = errors.New("the consent is not active")
	// ErrAlreadyRevoked: the consent was already revoked by another request
	// (criterion 6, HTTP 409 consent_already_revoked).
	ErrAlreadyRevoked = errors.New("the consent is already revoked")
	// ErrIdempotencyKeyReused: the idempotency key was already used to revoke
	// another consent.
	ErrIdempotencyKeyReused = errors.New("the idempotency key was used for another consent")
)

// RevokeRequest is what the holder's app sends when the holder confirms the
// revocation (HU-M1.3-02).
type RevokeRequest struct {
	// RequestedAt is when the holder confirmed it on the device. It is kept as
	// the time of the request even if the app sends it hours later, from its
	// offline queue (criterion 5).
	RequestedAt        time.Time
	Device             string
	ConfirmationMethod string
}

// Validate checks the request against the consent at now: requested_at is
// required, at most MaxClockSkew in the future and never before the consent
// was granted; the device is required and at most MaxDeviceLength
// characters; the confirmation method is biometric or password. It returns a
// *ValidationError naming every invalid field.
func (r RevokeRequest) Validate(c Consent, now time.Time) error {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }

	switch {
	case r.RequestedAt.IsZero():
		bad("requested_at", CodeRequired)
	case r.RequestedAt.After(now.Add(MaxClockSkew)), r.RequestedAt.Before(c.GrantedAt):
		bad("requested_at", CodeOutOfRange)
	}

	switch device := normalizeName(r.Device); {
	case device == "":
		bad("device", CodeRequired)
	case len([]rune(device)) > MaxDeviceLength:
		bad("device", CodeOutOfRange)
	}

	switch ConfirmationMethod(r.ConfirmationMethod) {
	case ConfirmationBiometric, ConfirmationPassword:
	case "":
		bad("confirmation_method", CodeRequired)
	default:
		bad("confirmation_method", CodeInvalidFormat)
	}

	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}

// NormalizedDevice is the device as it is recorded: spaces collapsed.
func (r RevokeRequest) NormalizedDevice() string { return normalizeName(r.Device) }

// Revocation is the immutable record of a revocation (criterion 4): who
// asked, for which consent, what it covered (a copy of the scope), when it
// was requested and when it took effect, by which channel and device. It is
// written before the holder gets the confirmation and never changes.
type Revocation struct {
	ID            string
	ConsentID     string
	ReceiptNumber string
	Holder        Holder
	// Scope is a copy of the scope of the consent, as it was revoked.
	Scope []SourceTerm
	// Entities are the entities notified, with the name shown at the time.
	Entities []Entity
	// RequestedAt is the time of the request (the device's, criterion 5);
	// EffectiveAt the server time when audit recorded it and stopped
	// verifying the consent as active.
	RequestedAt time.Time
	EffectiveAt time.Time
	// NoticeDeadlineAt is when the window of the entities to confirm ends:
	// EffectiveAt plus the notice deadline (RN-02).
	NoticeDeadlineAt   time.Time
	Channel            RevocationChannel
	Device             string
	ConfirmationMethod ConfirmationMethod
	Actor              Actor
	// Reason and Reference are for the compliance portal (HU-W4.2-02); the
	// app leaves them empty.
	Reason    string
	Reference string
	// RequestID is the idempotency key of the request.
	RequestID string
}

// receiptZone is the time zone of Colombia (UTC-5, no daylight saving time).
// It is fixed so that no time zone database is needed.
var receiptZone = time.FixedZone("America/Bogota", -5*60*60)

// ReceiptZone returns the time zone in which a receipt shows its times.
func ReceiptZone() *time.Location { return receiptZone }

// receiptNumberPattern is the shape of a receipt number: REV-<year>-<4 or more
// digits>, as in the prototype (REV-2026-0088, decision D-5 a).
var receiptNumberPattern = regexp.MustCompile(`^REV-[0-9]{4}-[0-9]{4,}$`)

// FormatReceiptNumber builds the receipt number of the revocation that took
// effect at effectiveAt with the given sequence number: the year is the one
// of Colombia and the sequence has at least four digits.
func FormatReceiptNumber(effectiveAt time.Time, seq int64) string {
	return fmt.Sprintf("REV-%d-%04d", effectiveAt.In(receiptZone).Year(), seq)
}

// ValidReceiptNumber tells whether s has the shape of a receipt number.
func ValidReceiptNumber(s string) bool { return receiptNumberPattern.MatchString(s) }

// RevokedDetails is the JSON written in the details of the revoked event: the
// entities notified and when their window ends. With it the state of each
// entity is derived from the events alone, like the status of the consent.
type RevokedDetails struct {
	RevocationID string         `json:"revocation_id,omitempty"`
	Entities     []EntityWindow `json:"entities"`
}

// EntityWindow is one entity of a revocation and the end of its window.
type EntityWindow struct {
	Code       string    `json:"code"`
	DeadlineAt time.Time `json:"deadline_at"`
}

// RetryDetails is the JSON written in the details of a retry event: the end
// of the new window of the entity.
type RetryDetails struct {
	DeadlineAt time.Time `json:"deadline_at"`
}

// EntityNotice is the state of the notice to one entity, as derived from the
// events at a given instant.
type EntityNotice struct {
	Entity      Entity
	Status      EntityNoticeStatus
	ConfirmedAt time.Time
	// DeadlineAt is the end of the current window of the entity.
	DeadlineAt time.Time
}

// RevocationView is the revocation of a consent as derived from its events at
// a given instant: the receipt, the two times, the channel and device, and
// the state of the notice to each entity.
type RevocationView struct {
	ReceiptNumber string
	Status        ConsentStatus
	RequestedAt   time.Time
	EffectiveAt   time.Time
	Channel       string
	Device        string
	Summary       NoticeSummary
	Entities      []EntityNotice
}

// deriveNotices returns the state of the notice to each entity of the revoked
// event at now, and whether the revocation is partial: some entity has a
// revocation_not_confirmed without a later revocation_confirmed, or has not
// confirmed and its window already ended (derived from the date, like the
// expiry, so it does not depend on the worker having run). A confirmation is
// final. A revoked event without entities (written before HU-M1.3-02, for
// example by a seed) has nothing to notify.
func deriveNotices(revoked ConsentEvent, entityEvents []ConsentEvent, now time.Time, names EntityNames) ([]EntityNotice, bool) {
	var details RevokedDetails
	if len(revoked.Details) > 0 {
		// An unreadable details means no entity to follow, never a failure
		// of the read.
		_ = json.Unmarshal(revoked.Details, &details)
	}
	notices := make([]EntityNotice, 0, len(details.Entities))
	partial := false
	for _, w := range details.Entities {
		n := EntityNotice{Entity: names.Entity(w.Code), Status: NoticePending, DeadlineAt: w.DeadlineAt.UTC()}
		unresolved := false
		for _, e := range entityEvents {
			if e.EntityCode != w.Code || n.Status == NoticeConfirmed {
				continue
			}
			switch e.Status {
			case HistoryRevocationConfirmed:
				n.Status, n.ConfirmedAt, unresolved = NoticeConfirmed, e.OccurredAt.UTC(), false
			case HistoryRevocationNotConfirmed:
				n.Status, unresolved = NoticeNotConfirmed, true
			case HistoryRevocationRetryRequested:
				var retry RetryDetails
				if json.Unmarshal(e.Details, &retry) == nil && !retry.DeadlineAt.IsZero() {
					n.Status, n.DeadlineAt = NoticePending, retry.DeadlineAt.UTC()
				}
			}
		}
		if n.Status == NoticePending && !n.DeadlineAt.IsZero() && !now.Before(n.DeadlineAt) {
			n.Status = NoticeNotConfirmed
		}
		if n.Status == NoticeNotConfirmed || unresolved {
			partial = true
		}
		notices = append(notices, n)
	}
	return notices, partial
}

// summarize returns the summary of the notices.
func summarize(notices []EntityNotice, partial bool) NoticeSummary {
	if partial {
		return NoticePartial
	}
	for _, n := range notices {
		if n.Status != NoticeConfirmed {
			return NoticeInProgress
		}
	}
	return NoticeCompleted
}

// CheckRevocable tells whether the consent of the view can be revoked
// (criterion 6): only an active one can. A revoked or partially revoked
// consent gives ErrAlreadyRevoked and any other status (expired, reserved)
// ErrConsentNotActive.
func CheckRevocable(v ConsentView) error {
	switch v.Status {
	case StatusActive:
		return nil
	case StatusRevoked, StatusPartiallyRevoked:
		return ErrAlreadyRevoked
	default:
		return ErrConsentNotActive
	}
}
