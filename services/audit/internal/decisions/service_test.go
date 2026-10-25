package decisions_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/decisions"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

type memStore struct {
	mu   sync.Mutex
	rows map[string]domain.DecisionRecord
	err  error
}

func (m *memStore) Create(_ context.Context, r domain.DecisionRecord) (domain.DecisionRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return domain.DecisionRecord{}, false, m.err
	}
	if existing, ok := m.rows[r.OfferID]; ok {
		return existing, false, nil
	}
	if m.rows == nil {
		m.rows = map[string]domain.DecisionRecord{}
	}
	m.rows[r.OfferID] = r
	return r, true, nil
}

func (m *memStore) GetByApplication(_ context.Context, app string) (domain.DecisionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found *domain.DecisionRecord
	for _, r := range m.rows {
		if r.ApplicationID == app && (found == nil || r.RequestedAt.After(found.RequestedAt)) {
			r := r
			found = &r
		}
	}
	if found == nil {
		return domain.DecisionRecord{}, domain.ErrDecisionNotFound
	}
	return *found, nil
}

func (m *memStore) Get(_ context.Context, id string) (domain.DecisionRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[id]; ok {
		return r, nil
	}
	return domain.DecisionRecord{}, domain.ErrDecisionNotFound
}

type fixedClock time.Time

func (c fixedClock) Now() time.Time { return time.Time(c) }

func record() domain.DecisionRecord {
	return domain.DecisionRecord{
		OfferID: "off-1", CustomerID: "cus-1", PartnerID: "p-1", ApplicationID: "app-1", Channel: "embedded",
		RequestedAt: time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC), RaterVersion: "v1",
		Rules:   []domain.RuleApplied{{Order: 1, Code: "base_monthly_rate", Value: 0.001}},
		Premium: 100, Currency: "COP", Decision: domain.DecisionAccepted,
	}
}

func TestRecordStampsServerTimeAndIsIdempotent(t *testing.T) {
	now := time.Date(2026, 10, 20, 10, 0, 1, 0, time.UTC)
	svc := decisions.NewService(&memStore{}, fixedClock(now))

	first, created, err := svc.Record(context.Background(), record())
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, now, first.RecordedAt)

	changed := record()
	changed.Premium = 999
	second, created, err := svc.Record(context.Background(), changed)
	require.NoError(t, err)
	assert.False(t, created, "a second record for the same offer is not stored")
	assert.Equal(t, float64(100), second.Premium, "the first record is never replaced")
}

func TestRecordRejectsInvalidAndReportsStoreFailure(t *testing.T) {
	svc := decisions.NewService(&memStore{}, fixedClock(time.Now()))
	bad := record()
	bad.OfferID = ""
	_, _, err := svc.Record(context.Background(), bad)
	var verr *domain.ValidationError
	assert.ErrorAs(t, err, &verr)

	boom := errors.New("db down")
	svc = decisions.NewService(&memStore{err: boom}, fixedClock(time.Now()))
	_, _, err = svc.Record(context.Background(), record())
	assert.ErrorIs(t, err, boom)
}

func TestGet(t *testing.T) {
	svc := decisions.NewService(&memStore{}, fixedClock(time.Now()))
	_, _, err := svc.Record(context.Background(), record())
	require.NoError(t, err)

	got, err := svc.Get(context.Background(), "off-1")
	require.NoError(t, err)
	assert.Equal(t, "cus-1", got.CustomerID)

	_, err = svc.Get(context.Background(), "nope")
	assert.ErrorIs(t, err, domain.ErrDecisionNotFound)
}

func TestGetFindsTheMostRecentDecisionOfAnApplication(t *testing.T) {
	svc := decisions.NewService(&memStore{}, fixedClock(time.Now()))
	older, newer := record(), record()
	older.OfferID, older.RequestedAt = "off-old", time.Date(2026, 10, 19, 10, 0, 0, 0, time.UTC)
	newer.OfferID, newer.RequestedAt = "off-new", time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	for _, r := range []domain.DecisionRecord{older, newer} {
		_, _, err := svc.Record(context.Background(), r)
		require.NoError(t, err)
	}

	got, err := svc.Get(context.Background(), "app-1")

	require.NoError(t, err)
	assert.Equal(t, "off-new", got.OfferID)
}
