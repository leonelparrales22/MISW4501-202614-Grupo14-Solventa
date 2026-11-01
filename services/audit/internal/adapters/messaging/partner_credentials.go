package messaging

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

// PartnerCredentialEventTypes are the events of partners that audit records
// (HU-W3.1-01): one handler serves the three of them.
var PartnerCredentialEventTypes = []string{
	domain.PartnerCredentialsIssued,
	domain.PartnerCredentialsRotated,
	domain.PartnerCredentialsRevoked,
}

// PartnerCredentialRecorder records a partner credential change
// (partnercredentials.Service).
type PartnerCredentialRecorder interface {
	Record(ctx context.Context, r domain.PartnerCredentialRecord) (bool, error)
}

type credentialQuotaPayload struct {
	DailyLimit int64 `json:"daily_limit"`
	RateLimit  int64 `json:"rate_limit"`
	BurstLimit int64 `json:"burst_limit"`
}

type credentialActorPayload struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Label string `json:"label"`
}

// partnerCredentialPayload is the payload of the three events, v1. It has no
// field for the secret: partners never sends it.
type partnerCredentialPayload struct {
	PartnerID            string                 `json:"partner_id"`
	PartnerCode          string                 `json:"partner_code"`
	PartnerName          string                 `json:"partner_name"`
	CredentialID         string                 `json:"credential_id"`
	ClientID             string                 `json:"client_id"`
	PreviousCredentialID string                 `json:"previous_credential_id"`
	PreviousClientID     string                 `json:"previous_client_id"`
	PreviousExpiresAt    *time.Time             `json:"previous_expires_at"`
	GracePeriodHours     int                    `json:"grace_period_hours"`
	Reason               string                 `json:"reason"`
	Quota                credentialQuotaPayload `json:"quota"`
	Actor                credentialActorPayload `json:"actor"`
	OccurredAt           time.Time              `json:"occurred_at"`
}

// NewPartnerCredentialsHandler handles PartnerCredentialsIssued,
// PartnerCredentialsRotated and PartnerCredentialsRevoked: it records the
// change, idempotently by event id. A payload that cannot be decoded or
// validated is an error, so the message is retried and ends in the
// dead-letter queue of the audit queue.
func NewPartnerCredentialsHandler(svc PartnerCredentialRecorder) events.Handler {
	return events.HandlerFunc(func(ctx context.Context, e events.Envelope) error {
		var p partnerCredentialPayload
		if err := e.DecodePayload(&p); err != nil {
			return err
		}
		r := domain.PartnerCredentialRecord{
			EventID: e.EventID, EventType: e.EventType, PartnerID: p.PartnerID, PartnerCode: p.PartnerCode,
			PartnerName: p.PartnerName, CredentialID: p.CredentialID, ClientID: p.ClientID,
			PreviousCredentialID: p.PreviousCredentialID, PreviousClientID: p.PreviousClientID,
			GracePeriodHours: p.GracePeriodHours, Reason: p.Reason,
			Quota:      domain.CredentialQuota{DailyLimit: p.Quota.DailyLimit, RateLimit: p.Quota.RateLimit, BurstLimit: p.Quota.BurstLimit},
			Actor:      domain.CredentialActor{Type: p.Actor.Type, ID: p.Actor.ID, Label: p.Actor.Label},
			OccurredAt: p.OccurredAt,
		}
		if p.PreviousExpiresAt != nil {
			r.PreviousExpiresAt = *p.PreviousExpiresAt
		}
		if r.OccurredAt.IsZero() {
			r.OccurredAt = e.OccurredAt
		}
		created, err := svc.Record(ctx, r)
		if err != nil {
			return fmt.Errorf("event %s: %w", e.EventID, err)
		}
		slog.InfoContext(ctx, "partner credential change recorded", "event_id", e.EventID, "event_type", e.EventType,
			"partner_id", p.PartnerID, "credential_id", p.CredentialID, "duplicate", !created)
		return nil
	})
}
