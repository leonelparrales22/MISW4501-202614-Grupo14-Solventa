package domain

import (
	"strings"
	"time"
)

// Event types partners publishes when the credentials of a partner change
// (HU-W3.1-01, docs/events/README.md).
const (
	PartnerCredentialsIssued  = "PartnerCredentialsIssued"
	PartnerCredentialsRotated = "PartnerCredentialsRotated"
	PartnerCredentialsRevoked = "PartnerCredentialsRevoked"
)

// Reasons of a credential change; each event type has exactly one.
const (
	CredentialReasonApproval         = "approval"
	CredentialReasonRotation         = "rotation"
	CredentialReasonGracePeriodEnded = "grace_period_ended"
)

// Actor types of a credential change: an operations analyst (the sub of
// their token) or the partners process itself (the end of a grace period).
const (
	CredentialActorOperations = "operations"
	CredentialActorSystem     = "system"
)

// maxCredentialTextLength bounds the free texts of a record (names and
// labels), so a malformed event cannot fill the table.
const maxCredentialTextLength = 254

// maxGracePeriodHours is the longest grace period partners accepts (168 h).
const maxGracePeriodHours = 168

// reasonOfCredentialEvent is the only reason each event type may carry.
var reasonOfCredentialEvent = map[string]string{
	PartnerCredentialsIssued:  CredentialReasonApproval,
	PartnerCredentialsRotated: CredentialReasonRotation,
	PartnerCredentialsRevoked: CredentialReasonGracePeriodEnded,
}

// CredentialQuota is the quota of the partner when the change happened.
type CredentialQuota struct {
	DailyLimit int64
	RateLimit  int64
	BurstLimit int64
}

// CredentialActor is who issued the change: the label is the e-mail or user
// name of the analyst, or "Sistema" for the process.
type CredentialActor struct {
	Type  string
	ID    string
	Label string
}

// PartnerCredentialRecord is one immutable entry of the audit record of the
// partner credentials (HU-W3.1-01 criterion 3): what changed, when and who
// issued it. It never holds the secret nor its hint.
type PartnerCredentialRecord struct {
	EventID     string
	EventType   string
	PartnerID   string
	PartnerCode string
	PartnerName string

	CredentialID string
	ClientID     string

	// Only for a rotation: the previous credential and the end of its grace
	// period.
	PreviousCredentialID string
	PreviousClientID     string
	PreviousExpiresAt    time.Time
	GracePeriodHours     int

	Reason     string
	Quota      CredentialQuota
	Actor      CredentialActor
	OccurredAt time.Time
	// RecordedAt is set by the database when the record is stored.
	RecordedAt time.Time
}

// Validate checks the record and returns a *ValidationError naming every
// invalid field.
func (r PartnerCredentialRecord) Validate() error {
	var fields []FieldError
	bad := func(field, code string) { fields = append(fields, FieldError{Field: field, Code: code}) }
	identifier := func(field, value string) {
		switch {
		case value == "":
			bad(field, CodeRequired)
		case len(value) > MaxIdentifierLength || !identifierPattern.MatchString(value):
			bad(field, CodeInvalidFormat)
		}
	}
	text := func(field, value string, required bool) {
		switch {
		case strings.TrimSpace(value) == "" && required:
			bad(field, CodeRequired)
		case len(value) > maxCredentialTextLength:
			bad(field, CodeOutOfRange)
		}
	}

	identifier("event_id", r.EventID)
	reason, known := reasonOfCredentialEvent[r.EventType]
	if !known {
		bad("event_type", CodeInvalidFormat)
	}
	identifier("partner_id", r.PartnerID)
	identifier("partner_code", r.PartnerCode)
	text("partner_name", r.PartnerName, true)
	identifier("credential_id", r.CredentialID)
	identifier("client_id", r.ClientID)
	if known && r.Reason != reason {
		bad("reason", CodeInvalidFormat)
	}

	if r.EventType == PartnerCredentialsRotated {
		identifier("previous_credential_id", r.PreviousCredentialID)
		identifier("previous_client_id", r.PreviousClientID)
		if r.PreviousExpiresAt.IsZero() {
			bad("previous_expires_at", CodeRequired)
		}
		if r.GracePeriodHours < 1 || r.GracePeriodHours > maxGracePeriodHours {
			bad("grace_period_hours", CodeOutOfRange)
		}
	}

	if r.Quota.DailyLimit < 1 {
		bad("quota.daily_limit", CodeOutOfRange)
	}
	if r.Quota.RateLimit < 1 {
		bad("quota.rate_limit", CodeOutOfRange)
	}
	if r.Quota.BurstLimit < r.Quota.RateLimit || r.Quota.BurstLimit < 1 {
		bad("quota.burst_limit", CodeOutOfRange)
	}

	if r.Actor.Type != CredentialActorOperations && r.Actor.Type != CredentialActorSystem {
		bad("actor.type", CodeInvalidFormat)
	}
	text("actor.id", r.Actor.ID, true)
	text("actor.label", r.Actor.Label, false)
	if r.OccurredAt.IsZero() {
		bad("occurred_at", CodeRequired)
	}

	if len(fields) > 0 {
		return &ValidationError{Fields: fields}
	}
	return nil
}
