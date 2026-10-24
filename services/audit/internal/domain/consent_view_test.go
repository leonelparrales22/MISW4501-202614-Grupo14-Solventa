package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

var viewGrantedAt = time.Date(2025, 11, 1, 15, 0, 0, 0, time.UTC)

func holderConsent(id string, at time.Time) domain.Consent {
	terms := domain.TermsV1()
	return domain.Consent{
		ID: id, CustomerID: "cust-1", Holder: domain.Holder{DocumentID: "1020304050", FullName: "Ana Pérez"},
		TermsVersion: terms.Version, Scope: terms.Sources, Purpose: terms.Purpose, Channel: domain.ChannelEmbedded,
		PartnerID: "aliado-banco-1", ApplicationID: "app-" + id, GrantedAt: at,
	}
}

func event(id, consent, status string, at time.Time) domain.ConsentEvent {
	return domain.ConsentEvent{EventID: id, ConsentID: consent, Status: status, OccurredAt: at, RecordedAt: at}
}

func TestDeriveStatus(t *testing.T) {
	c := holderConsent("cns-1", viewGrantedAt)
	expires := c.ExpiresAt()
	revokedAt := viewGrantedAt.AddDate(0, 3, 0)

	cases := []struct {
		name        string
		events      []domain.ConsentEvent
		now         time.Time
		wantStatus  domain.ConsentStatus
		wantEnded   time.Time
		wantReceipt string
	}{
		{name: "active without events", now: viewGrantedAt.Add(time.Hour), wantStatus: domain.StatusActive},
		{name: "active just before expires_at", now: expires.Add(-time.Nanosecond), wantStatus: domain.StatusActive},
		{name: "expired exactly at expires_at", now: expires, wantStatus: domain.StatusExpired, wantEnded: expires},
		{name: "expired after expires_at", now: expires.AddDate(1, 0, 0), wantStatus: domain.StatusExpired, wantEnded: expires},
		{
			name:   "revoked before expiring, with its receipt",
			events: []domain.ConsentEvent{withReceipt(event("e1", "cns-1", domain.HistoryRevoked, revokedAt), "REV-0001")},
			now:    revokedAt.Add(time.Hour), wantStatus: domain.StatusRevoked, wantEnded: revokedAt, wantReceipt: "REV-0001",
		},
		{
			name:   "revoked stays revoked after expires_at",
			events: []domain.ConsentEvent{withReceipt(event("e1", "cns-1", domain.HistoryRevoked, revokedAt), "REV-0001")},
			now:    expires.AddDate(0, 1, 0), wantStatus: domain.StatusRevoked, wantEnded: revokedAt, wantReceipt: "REV-0001",
		},
		{
			name:   "a revocation after expiring does not change the status",
			events: []domain.ConsentEvent{withReceipt(event("e1", "cns-1", domain.HistoryRevoked, expires.Add(time.Hour)), "REV-LATE")},
			now:    expires.Add(2 * time.Hour), wantStatus: domain.StatusExpired, wantEnded: expires,
		},
		{
			name:   "a revocation of one entity does not revoke the consent",
			events: []domain.ConsentEvent{entityEvent(event("e1", "cns-1", domain.HistoryRevoked, revokedAt), "aliado-banco-1")},
			now:    revokedAt.Add(time.Hour), wantStatus: domain.StatusActive,
		},
		{
			name:   "an event of another consent is ignored",
			events: []domain.ConsentEvent{event("e1", "cns-2", domain.HistoryRevoked, revokedAt)},
			now:    revokedAt.Add(time.Hour), wantStatus: domain.StatusActive,
		},
		{
			name:   "the reserved revocation_pending is taken from the last event and is not current",
			events: []domain.ConsentEvent{event("e1", "cns-1", string(domain.StatusRevocationPending), revokedAt)},
			now:    revokedAt.Add(time.Hour), wantStatus: domain.StatusRevocationPending,
		},
		{
			name:   "an unknown status of the last event leaves it active",
			events: []domain.ConsentEvent{event("e1", "cns-1", domain.HistoryRevocationRequested, revokedAt)},
			now:    revokedAt.Add(time.Hour), wantStatus: domain.StatusActive,
		},
		{
			name: "the earliest revocation wins",
			events: []domain.ConsentEvent{
				withReceipt(event("e2", "cns-1", domain.HistoryRevoked, revokedAt.Add(time.Hour)), "REV-2"),
				withReceipt(event("e1", "cns-1", domain.HistoryRevoked, revokedAt), "REV-1"),
			},
			now: revokedAt.Add(2 * time.Hour), wantStatus: domain.StatusRevoked, wantEnded: revokedAt, wantReceipt: "REV-1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, ended, receipt := domain.DeriveStatus(c, tc.events, tc.now)
			assert.Equal(t, tc.wantStatus, status)
			assert.True(t, tc.wantEnded.Equal(ended), "ended at %s, want %s", ended, tc.wantEnded)
			assert.Equal(t, tc.wantReceipt, receipt)
		})
	}
}

func withReceipt(e domain.ConsentEvent, receipt string) domain.ConsentEvent {
	e.ReceiptID = receipt
	return e
}

func entityEvent(e domain.ConsentEvent, entity string) domain.ConsentEvent {
	e.EntityCode = entity
	return e
}

func TestOnlyActiveIsCurrent(t *testing.T) {
	assert.True(t, domain.StatusActive.Current())
	for _, s := range []domain.ConsentStatus{domain.StatusExpired, domain.StatusRevoked, domain.StatusRevocationPending, domain.StatusPartiallyRevoked} {
		assert.False(t, s.Current(), s)
	}
}

func TestViewOfAnActiveConsent(t *testing.T) {
	c := holderConsent("cns-1", viewGrantedAt)

	v := domain.View(c, nil, viewGrantedAt.Add(time.Hour), domain.EntityNames{"aliado-banco-1": "Banco Aliado"})

	assert.Equal(t, domain.StatusActive, v.Status)
	assert.True(t, v.Current)
	assert.True(t, v.EndedAt.IsZero())
	assert.Empty(t, v.RevocationReceiptID)
	assert.Equal(t, []domain.Entity{{Code: "aliado-banco-1", Name: "Banco Aliado"}}, v.Entities)
	assert.Equal(t, []domain.HistoryEntry{{Status: domain.HistoryGranted, OccurredAt: viewGrantedAt, Channel: "embedded"}}, v.History)
	assert.Equal(t, c, v.Consent)
}

func TestViewOfARevokedConsentShowsTheReceiptAndTheHistory(t *testing.T) {
	c := holderConsent("cns-1", viewGrantedAt)
	requested := viewGrantedAt.AddDate(0, 2, 0)
	revoked := requested.Add(3 * time.Second)
	events := []domain.ConsentEvent{
		withReceipt(event("e2", "cns-1", domain.HistoryRevoked, revoked), "REV-0001"),
		{EventID: "e1", ConsentID: "cns-1", Status: domain.HistoryRevocationRequested, OccurredAt: requested, RecordedAt: requested, Channel: "mobile"},
	}

	v := domain.View(c, events, revoked.Add(time.Hour), nil)

	assert.Equal(t, domain.StatusRevoked, v.Status)
	assert.False(t, v.Current)
	assert.Equal(t, revoked, v.EndedAt)
	assert.Equal(t, "REV-0001", v.RevocationReceiptID)
	assert.Equal(t, []domain.HistoryEntry{
		{Status: domain.HistoryGranted, OccurredAt: viewGrantedAt, Channel: "embedded"},
		{Status: domain.HistoryRevocationRequested, OccurredAt: requested, Channel: "mobile"},
		{Status: domain.HistoryRevoked, OccurredAt: revoked, ReceiptID: "REV-0001"},
	}, v.History)
	assert.Equal(t, []domain.Entity{{Code: "aliado-banco-1", Name: "aliado-banco-1"}}, v.Entities, "a code without a name is shown as it is")
}

func TestViewOfAnExpiredConsentAddsTheExpiryInTimeOrder(t *testing.T) {
	c := holderConsent("cns-1", viewGrantedAt)
	expires := c.ExpiresAt()
	late := withReceipt(event("e1", "cns-1", domain.HistoryRevoked, expires.Add(time.Hour)), "REV-LATE")

	v := domain.View(c, []domain.ConsentEvent{late}, expires.Add(2*time.Hour), nil)

	assert.Equal(t, domain.StatusExpired, v.Status)
	assert.Equal(t, expires, v.EndedAt)
	assert.Empty(t, v.RevocationReceiptID, "the late revocation is not the end of the consent")
	require.Len(t, v.History, 3)
	assert.Equal(t, domain.HistoryGranted, v.History[0].Status)
	assert.Equal(t, domain.HistoryExpired, v.History[1].Status)
	assert.Equal(t, expires, v.History[1].OccurredAt)
	assert.Equal(t, domain.HistoryRevoked, v.History[2].Status, "the late revocation still shows in the history")
}

func TestHistoryTiesAreOrderedByRecordedAtThenID(t *testing.T) {
	c := holderConsent("cns-1", viewGrantedAt)
	at := viewGrantedAt.Add(time.Hour)
	events := []domain.ConsentEvent{
		{EventID: "b", ConsentID: "cns-1", Status: "x", OccurredAt: at, RecordedAt: at.Add(time.Second)},
		{EventID: "c", ConsentID: "cns-1", Status: "y", OccurredAt: at, RecordedAt: at},
		{EventID: "a", ConsentID: "cns-1", Status: "z", OccurredAt: at, RecordedAt: at.Add(time.Second)},
		{EventID: "other", ConsentID: "cns-9", Status: "w", OccurredAt: at, RecordedAt: at},
	}

	v := domain.View(c, events, at.Add(time.Hour), nil)

	got := make([]string, 0, len(v.History))
	for _, h := range v.History {
		got = append(got, h.Status)
	}
	assert.Equal(t, []string{domain.HistoryGranted, "y", "z", "x"}, got)
}

func TestHistoryKeepsTheEntityOfAnEntityEvent(t *testing.T) {
	c := holderConsent("cns-1", viewGrantedAt)
	e := entityEvent(event("e1", "cns-1", domain.HistoryRevoked, viewGrantedAt.Add(time.Hour)), "aliado-banco-1")

	v := domain.View(c, []domain.ConsentEvent{e}, viewGrantedAt.Add(2*time.Hour), nil)

	require.Len(t, v.History, 2)
	assert.Equal(t, "aliado-banco-1", v.History[1].EntityCode)
}

func TestSortForHolder(t *testing.T) {
	now := viewGrantedAt.AddDate(1, 6, 0)
	older := domain.View(holderConsent("cns-old-active", now.AddDate(0, -6, 0)), nil, now, nil)
	newer := domain.View(holderConsent("cns-new-active", now.AddDate(0, -1, 0)), nil, now, nil)
	expired := domain.View(holderConsent("cns-expired", viewGrantedAt), nil, now, nil)
	revokedConsent := holderConsent("cns-revoked", now.AddDate(0, -3, 0))
	revoked := domain.View(revokedConsent, []domain.ConsentEvent{event("e1", "cns-revoked", domain.HistoryRevoked, now.AddDate(0, 0, -1))}, now, nil)
	tieA := domain.View(holderConsent("cns-tie-a", now.AddDate(0, -2, 0)), nil, now, nil)
	tieB := domain.View(holderConsent("cns-tie-b", now.AddDate(0, -2, 0)), nil, now, nil)

	views := []domain.ConsentView{expired, older, revoked, tieB, newer, tieA}
	domain.SortForHolder(views)

	ids := make([]string, len(views))
	for i, v := range views {
		ids[i] = v.Consent.ID
	}
	assert.Equal(t, []string{"cns-new-active", "cns-tie-a", "cns-tie-b", "cns-old-active", "cns-revoked", "cns-expired"}, ids,
		"current by granted_at desc, then history by end desc")
}

func TestSortForHolderTiesInTheHistoryByID(t *testing.T) {
	now := viewGrantedAt.AddDate(2, 0, 0)
	a := domain.View(holderConsent("cns-a", viewGrantedAt), nil, now, nil)
	b := domain.View(holderConsent("cns-b", viewGrantedAt), nil, now, nil)
	views := []domain.ConsentView{b, a}

	domain.SortForHolder(views)

	assert.Equal(t, "cns-a", views[0].Consent.ID)
}
