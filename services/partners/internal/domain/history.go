package domain

import (
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
)

// HistoryType is the kind of an entry of the history of a partner.
type HistoryType string

const (
	HistoryRegistered         HistoryType = "registered"
	HistoryCredentialsIssued  HistoryType = "credentials_issued"
	HistoryCredentialsRotated HistoryType = "credentials_rotated"
	HistoryCredentialsRevoked HistoryType = "credentials_revoked"
)

// HistoryEntry is an append-only fact of the history of a partner.
type HistoryEntry struct {
	ID         string
	PartnerID  string
	Type       HistoryType
	OccurredAt time.Time
	Actor      Actor
	Details    map[string]any
}

// Event types published to the partners topic (docs/events/README.md).
const (
	EventCredentialsIssued  = "PartnerCredentialsIssued"
	EventCredentialsRotated = "PartnerCredentialsRotated"
	EventCredentialsRevoked = "PartnerCredentialsRevoked"
	// EventSource is the source of the events of this service.
	EventSource = "partners"
	// EventVersion is the version of the credential events.
	EventVersion = 1
)

// QuotaPayload is the quota as it travels in the events.
type QuotaPayload struct {
	DailyLimit int `json:"daily_limit"`
	RateLimit  int `json:"rate_limit"`
	BurstLimit int `json:"burst_limit"`
}

// ActorPayload is the actor as it travels in the events.
type ActorPayload struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Label string `json:"label"`
}

// CredentialEventPayload is the payload of the three credential events. It
// never carries the secret nor its hint.
type CredentialEventPayload struct {
	PartnerID            string       `json:"partner_id"`
	PartnerCode          string       `json:"partner_code"`
	PartnerName          string       `json:"partner_name"`
	CredentialID         string       `json:"credential_id"`
	ClientID             string       `json:"client_id"`
	PreviousCredentialID string       `json:"previous_credential_id,omitempty"`
	PreviousClientID     string       `json:"previous_client_id,omitempty"`
	PreviousExpiresAt    *time.Time   `json:"previous_expires_at,omitempty"`
	GracePeriodHours     int          `json:"grace_period_hours,omitempty"`
	Reason               string       `json:"reason"`
	Quota                QuotaPayload `json:"quota"`
	Actor                ActorPayload `json:"actor"`
	OccurredAt           time.Time    `json:"occurred_at"`
}

// CredentialEvent builds the envelope of a credential event with its
// deterministic id (one per fact).
func CredentialEvent(eventType string, p Partner, c Credential, actor Actor, at time.Time,
	previous *Credential, graceHours int) (events.Envelope, error) {
	var prefix, reason string
	switch eventType {
	case EventCredentialsIssued:
		prefix, reason = "partner-credentials-issued-", ReasonApproval
	case EventCredentialsRotated:
		prefix, reason = "partner-credentials-rotated-", ReasonRotation
	default:
		prefix, reason = "partner-credentials-revoked-", ReasonGracePeriodEnded
	}
	payload := CredentialEventPayload{
		PartnerID: p.ID, PartnerCode: p.Code, PartnerName: p.Name,
		CredentialID: c.ID, ClientID: c.ClientID, Reason: reason,
		Quota: QuotaPayload{DailyLimit: p.Quota.DailyLimit, RateLimit: p.Quota.RateLimit, BurstLimit: p.Quota.BurstLimit},
		Actor: ActorPayload(actor), OccurredAt: at.UTC(),
	}
	if previous != nil {
		payload.PreviousCredentialID, payload.PreviousClientID = previous.ID, previous.ClientID
		if previous.ExpiresAt != nil {
			exp := previous.ExpiresAt.UTC()
			payload.PreviousExpiresAt = &exp
		}
		payload.GracePeriodHours = graceHours
	}
	return events.New(prefix+c.ID, eventType, EventSource, EventVersion, at, payload)
}
