package messaging_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/messaging"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

type recorder struct {
	got []domain.PartnerCredentialRecord
	err error
}

func (r *recorder) Record(_ context.Context, rec domain.PartnerCredentialRecord) (bool, error) {
	if r.err != nil {
		return false, r.err
	}
	r.got = append(r.got, rec)
	return true, nil
}

var issuedAt = time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC)

func credentialEvent(t *testing.T, typ string, payload map[string]any) events.Envelope {
	t.Helper()
	e, err := events.New("partner-credentials-x-crd-2", typ, "partners", 1, issuedAt, payload)
	require.NoError(t, err)
	return e
}

func rotatedPayload() map[string]any {
	return map[string]any{
		"partner_id": "ptn-1", "partner_code": "ten_banco", "partner_name": "Banco Aliado",
		"credential_id": "crd-2", "client_id": "sol_2", "previous_credential_id": "crd-1", "previous_client_id": "sol_1",
		"previous_expires_at": issuedAt.Add(24 * time.Hour), "grace_period_hours": 24, "reason": "rotation",
		"quota":       map[string]any{"daily_limit": 5000, "rate_limit": 100, "burst_limit": 200},
		"actor":       map[string]any{"type": "operations", "id": "sub-1", "label": "andres@solventa.co"},
		"occurred_at": issuedAt,
	}
}

func TestPartnerCredentialsHandlerRecordsTheChange(t *testing.T) {
	r := &recorder{}
	h := messaging.NewPartnerCredentialsHandler(r)
	require.NoError(t, h.Handle(context.Background(), credentialEvent(t, domain.PartnerCredentialsRotated, rotatedPayload())))
	require.Len(t, r.got, 1)
	got := r.got[0]
	assert.Equal(t, "partner-credentials-x-crd-2", got.EventID)
	assert.Equal(t, domain.PartnerCredentialsRotated, got.EventType)
	assert.Equal(t, "crd-1", got.PreviousCredentialID)
	assert.Equal(t, issuedAt.Add(24*time.Hour), got.PreviousExpiresAt.UTC())
	assert.Equal(t, int64(5000), got.Quota.DailyLimit)
	assert.Equal(t, domain.CredentialActor{Type: "operations", ID: "sub-1", Label: "andres@solventa.co"}, got.Actor)
	assert.Len(t, messaging.PartnerCredentialEventTypes, 3)
}

func TestPartnerCredentialsHandlerFallsBackToTheEnvelopeInstant(t *testing.T) {
	r := &recorder{}
	p := rotatedPayload()
	delete(p, "occurred_at")
	require.NoError(t, messaging.NewPartnerCredentialsHandler(r).Handle(context.Background(),
		credentialEvent(t, domain.PartnerCredentialsRotated, p)))
	assert.Equal(t, issuedAt, r.got[0].OccurredAt)
}

func TestPartnerCredentialsHandlerErrors(t *testing.T) {
	bad := events.Envelope{EventID: "e", EventType: domain.PartnerCredentialsIssued, Payload: []byte(`[1]`)}
	require.Error(t, messaging.NewPartnerCredentialsHandler(&recorder{}).Handle(context.Background(), bad))

	boom := errors.New("db down")
	err := messaging.NewPartnerCredentialsHandler(&recorder{err: boom}).Handle(context.Background(),
		credentialEvent(t, domain.PartnerCredentialsRotated, rotatedPayload()))
	require.ErrorIs(t, err, boom)
}
