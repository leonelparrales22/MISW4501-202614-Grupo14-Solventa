package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

func partnerCredentialRecord(eventID, eventType string, at time.Time) domain.PartnerCredentialRecord {
	r := domain.PartnerCredentialRecord{
		EventID: eventID, EventType: eventType, PartnerID: "ptn-1", PartnerCode: "ten_banco", PartnerName: "Banco Aliado",
		CredentialID: "crd-1", ClientID: "sol_1", Reason: domain.CredentialReasonApproval,
		Quota: domain.CredentialQuota{DailyLimit: 5000, RateLimit: 100, BurstLimit: 200},
		Actor: domain.CredentialActor{Type: "operations", ID: "sub-1", Label: "andres@solventa.co"}, OccurredAt: at,
	}
	if eventType == domain.PartnerCredentialsRotated {
		r.CredentialID, r.ClientID, r.Reason = "crd-2", "sol_2", domain.CredentialReasonRotation
		r.PreviousCredentialID, r.PreviousClientID, r.GracePeriodHours = "crd-1", "sol_1", 24
		r.PreviousExpiresAt = at.Add(24 * time.Hour)
	}
	return r
}

// HU-W3.1-01 CA-3: the audit record of the partner credentials.
func TestPartnerCredentialStoreRoundTripAndIdempotency(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	s := postgres.NewPartnerCredentialStore(db)
	at := time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC)

	created, err := s.Append(ctx, partnerCredentialRecord("e-issued", domain.PartnerCredentialsIssued, at))
	require.NoError(t, err)
	assert.True(t, created)
	created, err = s.Append(ctx, partnerCredentialRecord("e-issued", domain.PartnerCredentialsIssued, at))
	require.NoError(t, err)
	assert.False(t, created, "a redelivery writes nothing")
	_, err = s.Append(ctx, partnerCredentialRecord("e-rotated", domain.PartnerCredentialsRotated, at.Add(time.Hour)))
	require.NoError(t, err)

	got, err := s.ListByPartner(ctx, "ptn-1")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, "e-rotated", got[0].EventID)
	assert.Equal(t, "crd-1", got[0].PreviousCredentialID)
	assert.Equal(t, 24, got[0].GracePeriodHours)
	assert.Equal(t, at.Add(25*time.Hour), got[0].PreviousExpiresAt)
	assert.Equal(t, "andres@solventa.co", got[1].Actor.Label)
	assert.Empty(t, got[1].PreviousCredentialID)
	assert.False(t, got[1].RecordedAt.IsZero())

	none, err := s.ListByPartner(ctx, "ptn-x")
	require.NoError(t, err)
	assert.Empty(t, none)
}

func TestPartnerCredentialTableIsAppendOnly(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	_, err := postgres.NewPartnerCredentialStore(db).Append(ctx,
		partnerCredentialRecord("e-1", domain.PartnerCredentialsIssued, time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)))
	require.NoError(t, err)
	for _, stmt := range []string{
		`UPDATE partner_credential_record SET actor_label = 'x'`,
		`DELETE FROM partner_credential_record`,
		`TRUNCATE partner_credential_record`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.Error(t, err, stmt)
	}
}
