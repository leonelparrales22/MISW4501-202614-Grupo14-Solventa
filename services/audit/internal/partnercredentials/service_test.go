package partnercredentials_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/partnercredentials"
)

type memStore struct {
	byEvent map[string]domain.PartnerCredentialRecord
	err     error
}

func (m *memStore) Append(_ context.Context, r domain.PartnerCredentialRecord) (bool, error) {
	if m.err != nil {
		return false, m.err
	}
	if _, ok := m.byEvent[r.EventID]; ok {
		return false, nil
	}
	m.byEvent[r.EventID] = r
	return true, nil
}

func (m *memStore) ListByPartner(_ context.Context, partnerID string) ([]domain.PartnerCredentialRecord, error) {
	if m.err != nil {
		return nil, m.err
	}
	var out []domain.PartnerCredentialRecord
	for _, r := range m.byEvent {
		if r.PartnerID == partnerID {
			out = append(out, r)
		}
	}
	return out, nil
}

func issued() domain.PartnerCredentialRecord {
	return domain.PartnerCredentialRecord{
		EventID: "partner-credentials-issued-crd-1", EventType: domain.PartnerCredentialsIssued, PartnerID: "ptn-1",
		PartnerCode: "ten_banco", PartnerName: "Banco", CredentialID: "crd-1", ClientID: "sol_1",
		Reason: domain.CredentialReasonApproval, Quota: domain.CredentialQuota{DailyLimit: 10, RateLimit: 1, BurstLimit: 2},
		Actor:      domain.CredentialActor{Type: "operations", ID: "sub-1", Label: "andres@solventa.co"},
		OccurredAt: time.Date(2026, 11, 1, 15, 0, 0, 123456789, time.FixedZone("COT", -5*3600)),
	}
}

func TestRecordIsIdempotentAndTruncatesToMicroseconds(t *testing.T) {
	store := &memStore{byEvent: map[string]domain.PartnerCredentialRecord{}}
	svc := partnercredentials.NewService(store)
	created, err := svc.Record(context.Background(), issued())
	require.NoError(t, err)
	assert.True(t, created)
	created, err = svc.Record(context.Background(), issued())
	require.NoError(t, err)
	assert.False(t, created, "the same event again writes nothing")
	got := store.byEvent["partner-credentials-issued-crd-1"]
	assert.Equal(t, time.UTC, got.OccurredAt.Location())
	assert.Zero(t, got.OccurredAt.Nanosecond()%1000)

	history, err := svc.History(context.Background(), "ptn-1")
	require.NoError(t, err)
	assert.Len(t, history, 1)
}

func TestRecordRejectsInvalidAndPropagatesStoreErrors(t *testing.T) {
	store := &memStore{byEvent: map[string]domain.PartnerCredentialRecord{}}
	svc := partnercredentials.NewService(store)
	_, err := svc.Record(context.Background(), domain.PartnerCredentialRecord{})
	var verr *domain.ValidationError
	require.ErrorAs(t, err, &verr)
	assert.Empty(t, store.byEvent)

	store.err = errors.New("db down")
	_, err = svc.Record(context.Background(), issued())
	require.ErrorIs(t, err, store.err)
	_, err = svc.History(context.Background(), "ptn-1")
	require.ErrorIs(t, err, store.err)
}
