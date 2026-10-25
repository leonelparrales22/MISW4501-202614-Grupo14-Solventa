package consents_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/consents"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// memStore is a test double of the append-only store.
type memStore struct {
	mu   sync.Mutex
	rows []domain.Consent
	err  error
}

func (m *memStore) Create(_ context.Context, c domain.Consent) (domain.Consent, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return domain.Consent{}, false, m.err
	}
	for _, r := range m.rows {
		if r.CustomerID == c.CustomerID && r.ApplicationID == c.ApplicationID && r.TermsVersion == c.TermsVersion {
			return r, false, nil
		}
	}
	m.rows = append(m.rows, c)
	return c, true, nil
}

func (m *memStore) Get(_ context.Context, id string) (domain.Consent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return domain.Consent{}, m.err
	}
	for _, r := range m.rows {
		if r.ID == id {
			return r, nil
		}
	}
	return domain.Consent{}, ports.ErrConsentNotFound
}

func (m *memStore) LatestByCustomer(_ context.Context, customerID string) (domain.Consent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return domain.Consent{}, m.err
	}
	var own []domain.Consent
	for _, r := range m.rows {
		if r.CustomerID == customerID {
			own = append(own, r)
		}
	}
	if len(own) == 0 {
		return domain.Consent{}, ports.ErrConsentNotFound
	}
	sort.Slice(own, func(i, j int) bool { return own[i].GrantedAt.After(own[j].GrantedAt) })
	return own[0], nil
}

func (m *memStore) ListByHolder(_ context.Context, document string) ([]domain.Consent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	var own []domain.Consent
	for _, r := range m.rows {
		if domain.NormalizeDocument(r.Holder.DocumentID) == document {
			own = append(own, r)
		}
	}
	sort.Slice(own, func(i, j int) bool { return own[i].GrantedAt.After(own[j].GrantedAt) })
	return own, nil
}

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func request() domain.GrantRequest {
	return domain.GrantRequest{
		CustomerID: "cust-1", DocumentID: "1020304050", FullName: "Ana Pérez", TermsVersion: "v1",
		AcceptedSources: []string{"open_finance", "open_data", "kyc"}, AcceptedDataPolicy: true,
		Channel: "embedded", PartnerID: "partner-1", ApplicationID: "app-1", UserAgent: "UA",
	}
}

func newService(t *testing.T) (*consents.Service, *memStore, *fakeClock) {
	t.Helper()
	store := &memStore{}
	clock := &fakeClock{now: time.Date(2026, 10, 17, 12, 0, 0, 0, time.FixedZone("x", -5*3600))}
	return consents.NewService(store, clock), store, clock
}

func TestGrantRecordsTheConsentWithServerTimeAndACopyOfTheScope(t *testing.T) {
	svc, _, clock := newService(t)

	c, created, err := svc.Grant(context.Background(), request())

	require.NoError(t, err)
	assert.True(t, created)
	assert.Regexp(t, `^cns-[0-9a-f]{32}$`, c.ID)
	assert.Equal(t, clock.now.UTC(), c.GrantedAt)
	assert.Equal(t, time.UTC, c.GrantedAt.Location())
	assert.Equal(t, "cust-1", c.CustomerID)
	assert.Equal(t, domain.Holder{DocumentID: "1020304050", FullName: "Ana Pérez"}, c.Holder)
	assert.Equal(t, domain.TermsVersionV1, c.TermsVersion)
	assert.Equal(t, domain.TermsV1().Sources, c.Scope)
	assert.Equal(t, "risk_profiling_mortgage_life_offer", c.Purpose)
	assert.Equal(t, domain.ChannelEmbedded, c.Channel)
	assert.Equal(t, "partner-1", c.PartnerID)
	assert.Equal(t, "app-1", c.ApplicationID)
	assert.Equal(t, "UA", c.UserAgent)
}

func TestGrantIdsAreUnique(t *testing.T) {
	svc, _, _ := newService(t)
	first, _, err := svc.Grant(context.Background(), request())
	require.NoError(t, err)
	req := request()
	req.ApplicationID = "app-2"

	second, _, err := svc.Grant(context.Background(), req)

	require.NoError(t, err)
	assert.NotEqual(t, first.ID, second.ID)
}

func TestGrantIsIdempotentByCustomerApplicationAndTerms(t *testing.T) {
	svc, store, clock := newService(t)
	first, created, err := svc.Grant(context.Background(), request())
	require.NoError(t, err)
	require.True(t, created)
	clock.now = clock.now.Add(time.Hour)

	again, created, err := svc.Grant(context.Background(), request())

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first, again, "the stored consent is returned, with its original granted_at")
	assert.Len(t, store.rows, 1)
}

func TestGrantForAnotherApplicationIsANewRecord(t *testing.T) {
	svc, store, _ := newService(t)
	_, _, err := svc.Grant(context.Background(), request())
	require.NoError(t, err)
	req := request()
	req.ApplicationID = "app-2"

	_, created, err := svc.Grant(context.Background(), req)

	require.NoError(t, err)
	assert.True(t, created)
	assert.Len(t, store.rows, 2)
}

func TestGrantRejectsAnInvalidRequestWithoutWriting(t *testing.T) {
	svc, store, _ := newService(t)
	req := request()
	req.AcceptedSources = []string{"open_finance"}

	_, _, err := svc.Grant(context.Background(), req)

	var ve *domain.ValidationError
	require.ErrorAs(t, err, &ve)
	assert.Empty(t, store.rows)
}

func TestGrantRejectsOutdatedTerms(t *testing.T) {
	svc, store, _ := newService(t)
	req := request()
	req.TermsVersion = "v0"

	_, _, err := svc.Grant(context.Background(), req)

	assert.ErrorIs(t, err, domain.ErrTermsOutdated)
	assert.Empty(t, store.rows)
}

func TestGrantPropagatesAStoreFailure(t *testing.T) {
	svc, store, _ := newService(t)
	store.err = errors.New("db down")

	_, _, err := svc.Grant(context.Background(), request())

	assert.ErrorContains(t, err, "db down")
}

func TestGetReturnsTheEvidenceOrNotFound(t *testing.T) {
	svc, _, _ := newService(t)
	c, _, err := svc.Grant(context.Background(), request())
	require.NoError(t, err)

	got, err := svc.Get(context.Background(), c.ID)
	require.NoError(t, err)
	assert.Equal(t, c, got)

	_, err = svc.Get(context.Background(), "cns-unknown")
	assert.ErrorIs(t, err, ports.ErrConsentNotFound)
}

func TestGetPropagatesAStoreFailure(t *testing.T) {
	svc, store, _ := newService(t)
	store.err = errors.New("db down")

	_, err := svc.Get(context.Background(), "cns-1")

	assert.ErrorContains(t, err, "db down")
	assert.NotErrorIs(t, err, ports.ErrConsentNotFound)
}

func TestVerifyWithoutConsentIsInactiveNotAnError(t *testing.T) {
	svc, _, _ := newService(t)

	v, err := svc.Verify(context.Background(), "nobody")

	require.NoError(t, err)
	assert.Equal(t, domain.Verification{}, v)
}

func TestVerifyReportsTheActiveConsent(t *testing.T) {
	svc, _, clock := newService(t)
	c, _, err := svc.Grant(context.Background(), request())
	require.NoError(t, err)

	v, err := svc.Verify(context.Background(), "cust-1")

	require.NoError(t, err)
	assert.True(t, v.Active)
	assert.Equal(t, c.ID, v.ConsentID)
	assert.Equal(t, c.GrantedAt, v.GrantedAt)
	assert.Equal(t, c.GrantedAt.AddDate(0, 12, 0), v.ExpiresAt)
	assert.Equal(t, []string{"open_finance", "open_data", "kyc"}, v.Sources)
	assert.Equal(t, clock.now.UTC(), v.GrantedAt)
}

func TestVerifyExpiresAtTwelveMonths(t *testing.T) {
	svc, _, clock := newService(t)
	c, _, err := svc.Grant(context.Background(), request())
	require.NoError(t, err)

	tests := []struct {
		name   string
		now    time.Time
		active bool
	}{
		{"one nanosecond before", c.ExpiresAt().Add(-time.Nanosecond), true},
		{"at the expiry", c.ExpiresAt(), false},
		{"one nanosecond after", c.ExpiresAt().Add(time.Nanosecond), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			clock.now = tc.now

			v, err := svc.Verify(context.Background(), "cust-1")

			require.NoError(t, err)
			assert.Equal(t, tc.active, v.Active)
			assert.Equal(t, c.ID, v.ConsentID)
			if !tc.active {
				assert.Empty(t, v.Sources)
			}
		})
	}
}

func TestVerifyDoesNotMixCustomers(t *testing.T) {
	svc, _, _ := newService(t)
	_, _, err := svc.Grant(context.Background(), request())
	require.NoError(t, err)

	v, err := svc.Verify(context.Background(), "cust-2")

	require.NoError(t, err)
	assert.False(t, v.Active)
}

func TestVerifyUsesTheLatestConsent(t *testing.T) {
	svc, _, clock := newService(t)
	_, _, err := svc.Grant(context.Background(), request())
	require.NoError(t, err)
	clock.now = clock.now.Add(24 * time.Hour)
	req := request()
	req.ApplicationID = "app-2"
	second, _, err := svc.Grant(context.Background(), req)
	require.NoError(t, err)

	v, err := svc.Verify(context.Background(), "cust-1")

	require.NoError(t, err)
	assert.Equal(t, second.ID, v.ConsentID)
}

func TestVerifyPropagatesAStoreFailure(t *testing.T) {
	svc, store, _ := newService(t)
	store.err = errors.New("db down")

	_, err := svc.Verify(context.Background(), "cust-1")

	assert.ErrorContains(t, err, "db down")
}

func TestCurrentTermsAreV1(t *testing.T) {
	svc, _, _ := newService(t)

	assert.Equal(t, domain.TermsV1(), svc.CurrentTerms())
}

// HU-M1.3-02 criterion 3: with the events, a revoked consent stops being
// active the instant its revocation is recorded.

func newServiceWithEvents(t *testing.T) (*consents.Service, *memEvents, *fakeClock) {
	t.Helper()
	events := &memEvents{}
	clock := &fakeClock{now: time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)}
	return consents.NewService(&memStore{}, clock, consents.WithEvents(events)), events, clock
}

func TestVerifyWithEventsOfARevokedConsentIsInactiveAtOnce(t *testing.T) {
	svc, events, clock := newServiceWithEvents(t)
	c, _, err := svc.Grant(context.Background(), request())
	require.NoError(t, err)
	other := request()
	other.CustomerID, other.ApplicationID = "cust-2", "app-2"
	c2, _, err := svc.Grant(context.Background(), other)
	require.NoError(t, err)

	before, err := svc.Verify(context.Background(), "cust-1")
	require.NoError(t, err)
	require.True(t, before.Active)

	revokedAt := clock.now.Add(time.Hour)
	events.rows = append(events.rows, domain.ConsentEvent{
		EventID: "e1", ConsentID: c.ID, Status: domain.HistoryRevoked, OccurredAt: revokedAt, RecordedAt: revokedAt,
	})
	clock.now = revokedAt

	v, err := svc.Verify(context.Background(), "cust-1")
	require.NoError(t, err)
	assert.False(t, v.Active, "inactive from the effective instant of the revocation")
	assert.Empty(t, v.Sources)
	assert.NotNil(t, v.Sources, "an empty list, not null")
	assert.Equal(t, c.ID, v.ConsentID, "the consent is still named")

	v2, err := svc.Verify(context.Background(), "cust-2")
	require.NoError(t, err)
	assert.True(t, v2.Active, "the consent of another customer is not affected")
	assert.Equal(t, c2.ID, v2.ConsentID)
}

func TestVerifyWithEventsOfAPartiallyRevokedConsentIsInactive(t *testing.T) {
	svc, events, clock := newServiceWithEvents(t)
	c, _, err := svc.Grant(context.Background(), request())
	require.NoError(t, err)
	at := clock.now.Add(time.Hour)
	events.rows = append(events.rows,
		domain.ConsentEvent{EventID: "e1", ConsentID: c.ID, Status: domain.HistoryRevoked, OccurredAt: at, RecordedAt: at,
			Details: []byte(`{"entities":[{"code":"partner-1","deadline_at":"2026-10-17T13:05:00Z"}]}`)},
		domain.ConsentEvent{EventID: "e2", ConsentID: c.ID, EntityCode: "partner-1", Status: domain.HistoryRevocationNotConfirmed,
			OccurredAt: at.Add(5 * time.Minute), RecordedAt: at.Add(5 * time.Minute)},
	)
	clock.now = at.Add(time.Hour)

	v, err := svc.Verify(context.Background(), "cust-1")

	require.NoError(t, err)
	assert.False(t, v.Active)
}

func TestVerifyWithEventsKeepsAnActiveConsentActive(t *testing.T) {
	svc, events, clock := newServiceWithEvents(t)
	c, _, err := svc.Grant(context.Background(), request())
	require.NoError(t, err)
	events.rows = append(events.rows, domain.ConsentEvent{
		EventID: "e1", ConsentID: c.ID, EntityCode: "partner-1", Status: domain.HistoryRevoked, OccurredAt: clock.now, RecordedAt: clock.now,
	})

	v, err := svc.Verify(context.Background(), "cust-1")

	require.NoError(t, err)
	assert.True(t, v.Active, "an event of one entity does not revoke the consent")
	assert.Equal(t, []string{"open_finance", "open_data", "kyc"}, v.Sources)
}

func TestVerifyWithEventsPropagatesAReadFailure(t *testing.T) {
	svc, events, _ := newServiceWithEvents(t)
	_, _, err := svc.Grant(context.Background(), request())
	require.NoError(t, err)
	events.err = errors.New("events down")

	_, err = svc.Verify(context.Background(), "cust-1")

	assert.ErrorContains(t, err, "events down")
}

func TestVerifyWithEventsDoesNotReadThemForAnExpiredConsent(t *testing.T) {
	svc, events, clock := newServiceWithEvents(t)
	c, _, err := svc.Grant(context.Background(), request())
	require.NoError(t, err)
	events.err = errors.New("must not be read")
	clock.now = c.ExpiresAt()

	v, err := svc.Verify(context.Background(), "cust-1")

	require.NoError(t, err)
	assert.False(t, v.Active)
}
