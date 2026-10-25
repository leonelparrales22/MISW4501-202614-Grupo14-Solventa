package consents_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/consents"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// The revocation process (HU-M1.3-02) over in-memory doubles that behave like
// the PostgreSQL adapters: the record writes the events, and confirming or
// failing a notice writes the event of the entity.

// syncEvents is a thread-safe double of the event reader that the other
// doubles append to.
type syncEvents struct {
	mu   sync.Mutex
	rows []domain.ConsentEvent
	err  error
	seq  int
}

func (m *syncEvents) ListByConsents(_ context.Context, ids []string) (map[string][]domain.ConsentEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return nil, m.err
	}
	out := map[string][]domain.ConsentEvent{}
	for _, e := range m.rows {
		for _, id := range ids {
			if e.ConsentID == id {
				out[id] = append(out[id], e)
			}
		}
	}
	return out, nil
}

func (m *syncEvents) add(e domain.ConsentEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	e.EventID = fmt.Sprintf("e%03d", m.seq)
	e.RecordedAt = e.OccurredAt
	m.rows = append(m.rows, e)
}

func (m *syncEvents) statuses(consentID string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, e := range m.rows {
		if e.ConsentID == consentID {
			out = append(out, e.Status+"|"+e.EntityCode)
		}
	}
	return out
}

// memRevocations is a double of the revocation store.
type memRevocations struct {
	mu      sync.Mutex
	events  *syncEvents
	notices *memNotices
	rows    map[string]domain.Revocation
	seq     int64
	err     error
	records int
}

func newMemRevocations(events *syncEvents, notices *memNotices) *memRevocations {
	return &memRevocations{events: events, notices: notices, rows: map[string]domain.Revocation{}}
}

func (m *memRevocations) Record(_ context.Context, rev domain.Revocation, notices []ports.NoticeRecord) (domain.Revocation, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records++
	if m.err != nil {
		return domain.Revocation{}, false, m.err
	}
	for _, r := range m.rows {
		if r.RequestID == rev.RequestID && r.ConsentID != rev.ConsentID {
			return domain.Revocation{}, false, domain.ErrIdempotencyKeyReused
		}
	}
	if existing, ok := m.rows[rev.ConsentID]; ok {
		if existing.RequestID == rev.RequestID {
			return existing, false, nil
		}
		return domain.Revocation{}, false, domain.ErrAlreadyRevoked
	}
	m.seq++
	rev.ReceiptNumber = domain.FormatReceiptNumber(rev.EffectiveAt, m.seq)
	m.rows[rev.ConsentID] = rev
	windows := make([]domain.EntityWindow, 0, len(notices))
	for _, n := range notices {
		windows = append(windows, domain.EntityWindow{Code: n.EntityCode, DeadlineAt: n.WindowEndsAt})
	}
	details, _ := json.Marshal(domain.RevokedDetails{RevocationID: rev.ID, Entities: windows})
	m.events.add(domain.ConsentEvent{ConsentID: rev.ConsentID, Status: domain.HistoryRevocationRequested, OccurredAt: rev.RequestedAt,
		Channel: string(rev.Channel), Device: rev.Device, ActorType: string(rev.Actor.Type), ActorID: rev.Actor.ID})
	m.events.add(domain.ConsentEvent{ConsentID: rev.ConsentID, Status: domain.HistoryRevoked, OccurredAt: rev.EffectiveAt,
		ReceiptID: rev.ReceiptNumber, Channel: string(rev.Channel), Device: rev.Device, Details: details,
		ActorType: string(rev.Actor.Type), ActorID: rev.Actor.ID})
	if m.notices != nil {
		for _, n := range notices {
			n.Seq, n.Status = 1, idempotency.StatusPending
			m.notices.put(n)
		}
	}
	return rev, true, nil
}

func (m *memRevocations) GetByConsent(_ context.Context, consentID string) (domain.Revocation, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.rows[consentID]; ok {
		return r, nil
	}
	return domain.Revocation{}, ports.ErrRevocationNotFound
}

// memNotices is a double of the notice store with the semantics of the
// PostgreSQL one: the last step of each notice decides.
type memNotices struct {
	mu        sync.Mutex
	events    *syncEvents
	last      map[string]ports.NoticeRecord
	customers map[string]string
	effective map[string]time.Time
	resumeErr error
	dueErr    error
}

func newMemNotices(events *syncEvents) *memNotices {
	return &memNotices{events: events, last: map[string]ports.NoticeRecord{}, customers: map[string]string{}, effective: map[string]time.Time{}}
}

func (m *memNotices) put(n ports.NoticeRecord) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n.CustomerID == "" {
		n.CustomerID = "cust-" + n.ConsentID
	}
	if n.EffectiveAt.IsZero() {
		n.EffectiveAt = n.NextAttemptAt
	}
	m.last[n.ID()] = n
}

func (m *memNotices) get(id string) ports.NoticeRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.last[id]
}

func (m *memNotices) Claim(_ context.Context, id string, now time.Time, lease time.Duration) (ports.NoticeRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.last[id]
	if !ok {
		return ports.NoticeRecord{}, false, nil
	}
	due := (n.Status == idempotency.StatusPending && !n.NextAttemptAt.After(now)) ||
		(n.Status == idempotency.StatusInProgress && n.ClaimedUntil.Before(now))
	if !due {
		return ports.NoticeRecord{}, false, nil
	}
	n.Seq++
	n.Status, n.ClaimedUntil = idempotency.StatusInProgress, now.Add(lease)
	m.last[id] = n
	return n, true, nil
}

func (m *memNotices) next(rec ports.NoticeRecord, status idempotency.Status, failed int, nextAt time.Time, ref, cause string) (ports.NoticeRecord, error) {
	cur := m.last[rec.ID()]
	if cur.Seq != rec.Seq {
		return ports.NoticeRecord{}, idempotency.ErrClaimLost
	}
	cur.Seq++
	cur.Status, cur.FailedAttempts, cur.NextAttemptAt, cur.ClaimedUntil, cur.Reference, cur.Cause = status, failed, nextAt, time.Time{}, ref, cause
	m.last[rec.ID()] = cur
	return cur, nil
}

func (m *memNotices) Confirm(_ context.Context, rec ports.NoticeRecord, now time.Time, out idempotency.Outcome) (ports.NoticeRecord, error) {
	m.mu.Lock()
	stored, err := m.next(rec, idempotency.StatusConfirmed, rec.FailedAttempts, time.Time{}, out.Reference, "")
	m.mu.Unlock()
	if err == nil {
		m.events.add(domain.ConsentEvent{ConsentID: rec.ConsentID, EntityCode: rec.EntityCode, Status: domain.HistoryRevocationConfirmed,
			OccurredAt: now, Reference: out.Reference})
	}
	return stored, err
}

func (m *memNotices) Reschedule(_ context.Context, rec ports.NoticeRecord, _, next time.Time, cause string) (ports.NoticeRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.next(rec, idempotency.StatusPending, rec.FailedAttempts+1, next, "", cause)
}

func (m *memNotices) Fail(_ context.Context, rec ports.NoticeRecord, now time.Time, reason idempotency.FailureReason, cause string) (ports.NoticeRecord, error) {
	m.mu.Lock()
	stored, err := m.next(rec, idempotency.StatusFailed, rec.FailedAttempts+1, time.Time{}, "", cause)
	m.mu.Unlock()
	if err == nil {
		m.events.add(domain.ConsentEvent{ConsentID: rec.ConsentID, EntityCode: rec.EntityCode, Status: domain.HistoryRevocationNotConfirmed,
			OccurredAt: now, Reason: string(reason)})
	}
	return stored, err
}

func (m *memNotices) Due(_ context.Context, now time.Time, limit int) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dueErr != nil {
		return nil, m.dueErr
	}
	var ids []string
	for id, n := range m.last {
		if (n.Status == idempotency.StatusPending && !n.NextAttemptAt.After(now)) ||
			(n.Status == idempotency.StatusInProgress && n.ClaimedUntil.Before(now)) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	return ids, nil
}

func (m *memNotices) Resume(_ context.Context, consentID string, entities []string, now time.Time, window time.Duration,
	actor domain.Actor) ([]string, error) {
	m.mu.Lock()
	if m.resumeErr != nil {
		m.mu.Unlock()
		return nil, m.resumeErr
	}
	var resumed []string
	for id, n := range m.last {
		if n.ConsentID != consentID {
			continue
		}
		if len(entities) > 0 && !contains(entities, n.EntityCode) {
			continue
		}
		if n.Status == idempotency.StatusFailed || (n.Status == idempotency.StatusPending && !n.WindowEndsAt.After(now)) {
			n.Seq++
			n.Status, n.FailedAttempts, n.WindowEndsAt, n.NextAttemptAt = idempotency.StatusPending, 0, now.Add(window), now
			m.last[id] = n
			resumed = append(resumed, n.EntityCode)
		}
	}
	m.mu.Unlock()
	sort.Strings(resumed)
	for _, e := range resumed {
		details, _ := json.Marshal(domain.RetryDetails{DeadlineAt: now.Add(window)})
		m.events.add(domain.ConsentEvent{ConsentID: consentID, EntityCode: e, Status: domain.HistoryRevocationRetryRequested,
			OccurredAt: now, Details: details, ActorType: string(actor.Type), ActorID: actor.ID})
	}
	return resumed, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// fakeRenderer is a double of the receipt renderer.
type fakeRenderer struct {
	lang string
	err  error
}

func (f *fakeRenderer) Render(rev domain.Revocation, c domain.Consent, lang string) ([]byte, error) {
	f.lang = lang
	if f.err != nil {
		return nil, f.err
	}
	return []byte("%PDF " + rev.ReceiptNumber + " " + c.ID), nil
}

// lockedClock is a thread-safe adjustable clock.
type lockedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *lockedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *lockedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

var revGrantedAt = time.Date(2026, 9, 1, 15, 0, 0, 0, time.UTC)

type revocationFixture struct {
	svc         *consents.RevocationService
	consents    *memStore
	events      *syncEvents
	revocations *memRevocations
	notices     *memNotices
	renderer    *fakeRenderer
	clock       *lockedClock
	wakes       int
}

func newRevocationFixture(t *testing.T) *revocationFixture {
	t.Helper()
	f := &revocationFixture{consents: &memStore{}, events: &syncEvents{}, renderer: &fakeRenderer{},
		clock: &lockedClock{now: time.Date(2026, 10, 24, 20, 0, 0, 123456789, time.UTC)}}
	f.notices = newMemNotices(f.events)
	f.revocations = newMemRevocations(f.events, f.notices)
	for _, c := range []domain.Consent{
		holderConsent("cns-a1", "1020304050", revGrantedAt),
		holderConsent("cns-a2", "1020304050", revGrantedAt),
		holderConsent("cns-b1", "9988776655", revGrantedAt),
		holderConsent("cns-old", "1020304050", revGrantedAt.AddDate(-1, 0, 0)),
	} {
		_, _, err := f.consents.Create(context.Background(), c)
		require.NoError(t, err)
	}
	f.svc = consents.NewRevocationService(consents.RevocationDeps{
		Consents: f.consents, Events: f.events, Revocations: f.revocations, Notices: f.notices, Receipts: f.renderer,
		Clock: f.clock, Names: domain.EntityNames{"aliado-banco-1": "Banco Aliado"},
	})
	f.svc.OnNewNotices(func() { f.wakes++ })
	return f
}

func holderRequest(at time.Time) domain.RevokeRequest {
	return domain.RevokeRequest{RequestedAt: at, Device: "Google Pixel 8", ConfirmationMethod: "biometric"}
}

func TestRevokeAsHolderRecordsBeforeAnsweringAndShowsRevoked(t *testing.T) {
	f := newRevocationFixture(t)
	requested := f.clock.Now().Add(-1500 * time.Millisecond)

	v, created, err := f.svc.RevokeAsHolder(context.Background(), "1020304050", "sub-a", "cns-a1", "req-1", holderRequest(requested))

	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, 1, f.wakes, "the worker is woken up")
	rev, err := f.revocations.GetByConsent(context.Background(), "cns-a1")
	require.NoError(t, err, "criterion 4: recorded before the answer")
	assert.Equal(t, "req-1", rev.RequestID)
	assert.Equal(t, domain.Actor{Type: domain.ActorHolder, ID: "sub-a"}, rev.Actor)
	assert.Equal(t, domain.RevocationChannelMobileApp, rev.Channel)
	assert.Equal(t, "Google Pixel 8", rev.Device)
	assert.Equal(t, domain.ConfirmationBiometric, rev.ConfirmationMethod)
	assert.Equal(t, requested.Truncate(time.Microsecond), rev.RequestedAt)
	assert.Equal(t, f.clock.Now().UTC().Truncate(time.Microsecond), rev.EffectiveAt, "the effective time is the server's")
	assert.Equal(t, rev.EffectiveAt.Add(domain.RevocationNoticeDeadline), rev.NoticeDeadlineAt)
	assert.Equal(t, []domain.Entity{{Code: "aliado-banco-1", Name: "Banco Aliado"}}, rev.Entities)
	assert.Equal(t, f.consents.rows[0].Scope, rev.Scope)
	assert.Regexp(t, `^rvk-[0-9a-f]{32}$`, rev.ID)

	assert.Equal(t, domain.StatusRevoked, v.Status)
	assert.False(t, v.Revocable)
	require.NotNil(t, v.Revocation)
	assert.Equal(t, rev.ReceiptNumber, v.Revocation.ReceiptNumber)
	assert.Equal(t, domain.NoticeInProgress, v.Revocation.Summary)
	require.Len(t, v.Revocation.Entities, 1)
	assert.Equal(t, domain.NoticePending, v.Revocation.Entities[0].Status)
	assert.Equal(t, "Banco Aliado", v.Revocation.Entities[0].Entity.Name)

	n := f.notices.get(ports.NoticeID("cns-a1", "aliado-banco-1"))
	assert.Equal(t, "revocation-cns-a1-aliado-banco-1", n.Key)
	assert.Equal(t, rev.NoticeDeadlineAt, n.WindowEndsAt)
}

func TestRevokeAsHolderWithTheSameKeyReturnsTheSameRevocation(t *testing.T) {
	f := newRevocationFixture(t)
	ctx := context.Background()
	req := holderRequest(f.clock.Now().Add(-time.Second))
	first, _, err := f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-a1", "req-1", req)
	require.NoError(t, err)
	f.clock.advance(10 * time.Second)

	again, created, err := f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-a1", "req-1", req)

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first.Revocation.ReceiptNumber, again.Revocation.ReceiptNumber)
	assert.Equal(t, 1, f.wakes, "nothing new to notify")
	assert.Equal(t, 1, f.revocations.records, "the store is not asked again")
}

func TestRevokeAsHolderWithAnotherKeyOnARevokedConsentIsRefused(t *testing.T) {
	f := newRevocationFixture(t)
	ctx := context.Background()
	_, _, err := f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-a1", "req-1", holderRequest(f.clock.Now()))
	require.NoError(t, err)

	_, created, err := f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-a1", "req-2", holderRequest(f.clock.Now()))

	assert.ErrorIs(t, err, domain.ErrAlreadyRevoked, "criterion 6")
	assert.False(t, created)
}

func TestRevokeASeededRevocationWithoutRecordIsAlreadyRevoked(t *testing.T) {
	f := newRevocationFixture(t)
	f.events.add(domain.ConsentEvent{ConsentID: "cns-a1", Status: domain.HistoryRevoked, OccurredAt: revGrantedAt.Add(time.Hour), ReceiptID: "REV-0001"})

	_, _, err := f.svc.RevokeAsHolder(context.Background(), "1020304050", "sub-a", "cns-a1", "req-1", holderRequest(f.clock.Now()))

	assert.ErrorIs(t, err, domain.ErrAlreadyRevoked)
}

func TestRevokeAnExpiredConsentIsNotActive(t *testing.T) {
	f := newRevocationFixture(t)

	_, _, err := f.svc.RevokeAsHolder(context.Background(), "1020304050", "sub-a", "cns-old", "req-1", holderRequest(f.clock.Now()))

	assert.ErrorIs(t, err, domain.ErrConsentNotActive)
	assert.Zero(t, f.revocations.records, "nothing is recorded")
}

func TestRevokeAForeignOrUnknownConsentIsNotFound(t *testing.T) {
	f := newRevocationFixture(t)
	ctx := context.Background()

	_, _, err := f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-b1", "req-1", holderRequest(f.clock.Now()))
	assert.ErrorIs(t, err, ports.ErrConsentNotFound, "another holder's consent")
	_, _, err = f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-missing", "req-1", holderRequest(f.clock.Now()))
	assert.ErrorIs(t, err, ports.ErrConsentNotFound)
	_, _, err = f.svc.Revoke(ctx, consents.RevokeCommand{ConsentID: "cns-missing", RequestID: "r"})
	assert.ErrorIs(t, err, ports.ErrConsentNotFound)
	assert.Zero(t, f.revocations.records)
}

func TestRevokeAnInvalidRequestWritesNothing(t *testing.T) {
	f := newRevocationFixture(t)
	ctx := context.Background()

	_, _, err := f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-a1", "req-1", domain.RevokeRequest{})
	var verr *domain.ValidationError
	require.ErrorAs(t, err, &verr)
	assert.Len(t, verr.Fields, 3)

	_, _, err = f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-a1", "", holderRequest(f.clock.Now()))
	require.ErrorAs(t, err, &verr)
	assert.Equal(t, []domain.FieldError{{Field: "Idempotency-Key", Code: domain.CodeRequired}}, verr.Fields)
	assert.Zero(t, f.revocations.records)
}

func TestRevokeKeepsTheTimeOfTheRequestFromTheOfflineQueue(t *testing.T) {
	f := newRevocationFixture(t)
	confirmedOffline := f.clock.Now().Add(-2 * time.Hour)

	v, _, err := f.svc.RevokeAsHolder(context.Background(), "1020304050", "sub-a", "cns-a1", "req-1", holderRequest(confirmedOffline))

	require.NoError(t, err)
	assert.Equal(t, confirmedOffline.UTC().Truncate(time.Microsecond), v.Revocation.RequestedAt, "criterion 5")
	assert.Equal(t, f.clock.Now().UTC().Truncate(time.Microsecond), v.Revocation.EffectiveAt)
}

func TestRevokeReportsTheErrorsOfTheStores(t *testing.T) {
	f := newRevocationFixture(t)
	ctx := context.Background()

	f.revocations.err = errors.New("ERROR: consent_revocation references the consent cns-a1, which does not exist (SQLSTATE 23503)")
	_, _, err := f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-a1", "req-1", holderRequest(f.clock.Now()))
	require.Error(t, err)
	assert.NotErrorIs(t, err, ports.ErrConsentNotFound, "a foreign key violation is a defect, never a not found")
	assert.ErrorContains(t, err, "record the revocation")

	f.revocations.err = domain.ErrIdempotencyKeyReused
	_, _, err = f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-a1", "req-1", holderRequest(f.clock.Now()))
	assert.ErrorIs(t, err, domain.ErrIdempotencyKeyReused)

	f.revocations.err = nil
	f.events.err = errors.New("events down")
	_, _, err = f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-a1", "req-1", holderRequest(f.clock.Now()))
	assert.ErrorContains(t, err, "events down")

	f.events.err = nil
	f.consents.err = errors.New("db down")
	_, _, err = f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-a1", "req-1", holderRequest(f.clock.Now()))
	assert.ErrorContains(t, err, "db down")
	_, _, err = f.svc.Revoke(ctx, consents.RevokeCommand{ConsentID: "cns-a1", RequestID: "r"})
	assert.ErrorContains(t, err, "db down")
}

func TestTheCompliancePortalReusesRevoke(t *testing.T) {
	f := newRevocationFixture(t)
	now := f.clock.Now()

	v, created, err := f.svc.Revoke(context.Background(), consents.RevokeCommand{
		ConsentID: "cns-b1", Actor: domain.Actor{Type: "operations", ID: "sub-ops"}, Channel: "compliance_portal",
		Request: domain.RevokeRequest{RequestedAt: now, Device: "web", ConfirmationMethod: "password"},
		Reason:  "customer_request", Reference: "TICKET-7", RequestID: "req-portal",
	})

	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, domain.StatusRevoked, v.Status)
	rev, err := f.revocations.GetByConsent(context.Background(), "cns-b1")
	require.NoError(t, err)
	assert.Equal(t, "TICKET-7", rev.Reference)
	assert.Equal(t, domain.RevocationChannel("compliance_portal"), rev.Channel)
}

func TestReceiptRendersTheRevocationOfTheHolder(t *testing.T) {
	f := newRevocationFixture(t)
	ctx := context.Background()
	_, _, err := f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-a1", "req-1", holderRequest(f.clock.Now()))
	require.NoError(t, err)

	pdf, number, err := f.svc.Receipt(ctx, "1020304050", "cns-a1", "en")

	require.NoError(t, err)
	assert.True(t, domain.ValidReceiptNumber(number))
	assert.Equal(t, "%PDF "+number+" cns-a1", string(pdf))
	assert.Equal(t, "en", f.renderer.lang)

	_, _, err = f.svc.Receipt(ctx, "9988776655", "cns-a1", "es")
	assert.ErrorIs(t, err, ports.ErrConsentNotFound, "another holder")
	_, _, err = f.svc.Receipt(ctx, "1020304050", "cns-a2", "es")
	assert.ErrorIs(t, err, ports.ErrConsentNotFound, "not revoked")
	f.renderer.err = errors.New("pdf broken")
	_, _, err = f.svc.Receipt(ctx, "1020304050", "cns-a1", "es")
	assert.ErrorContains(t, err, "pdf broken")
}

func TestRetryPendingResumesOnlyTheEntitiesThatDidNotConfirm(t *testing.T) {
	f := newRevocationFixture(t)
	ctx := context.Background()
	_, _, err := f.svc.RevokeAsHolder(ctx, "1020304050", "sub-a", "cns-a1", "req-1", holderRequest(f.clock.Now()))
	require.NoError(t, err)
	id := ports.NoticeID("cns-a1", "aliado-banco-1")
	rec, claimed, err := f.notices.Claim(ctx, id, f.clock.Now(), time.Second)
	require.NoError(t, err)
	require.True(t, claimed)
	_, err = f.notices.Fail(ctx, rec, f.clock.Now(), idempotency.ReasonWindowExpired, "status 503")
	require.NoError(t, err)
	wakes := f.wakes

	retried, err := f.svc.RetryPending(ctx, "cns-a1", domain.Actor{Type: "operations", ID: "sub-ops"})

	require.NoError(t, err)
	assert.Equal(t, []string{"aliado-banco-1"}, retried)
	assert.Equal(t, wakes+1, f.wakes)
	assert.Equal(t, idempotency.StatusPending, f.notices.get(id).Status)
	assert.Equal(t, f.clock.Now().UTC().Add(f.svc.NoticeDeadline()), f.notices.get(id).WindowEndsAt)

	none, err := f.svc.RetryPending(ctx, "cns-a1", domain.Actor{Type: "operations", ID: "sub-ops"})
	require.NoError(t, err)
	assert.Empty(t, none, "already pending in its new window")
	assert.Equal(t, wakes+1, f.wakes, "nothing to wake up for")

	_, err = f.svc.RetryPending(ctx, "cns-missing", domain.Actor{})
	assert.ErrorIs(t, err, ports.ErrConsentNotFound)
	f.notices.resumeErr = errors.New("db down")
	_, err = f.svc.RetryPending(ctx, "cns-a1", domain.Actor{})
	assert.ErrorContains(t, err, "db down")
	f.consents.err = errors.New("consents down")
	_, err = f.svc.RetryPending(ctx, "cns-a1", domain.Actor{})
	assert.ErrorContains(t, err, "consents down")
}

func TestNewRevocationServiceUsesTheDeadlineGiven(t *testing.T) {
	assert.Equal(t, domain.RevocationNoticeDeadline, consents.NewRevocationService(consents.RevocationDeps{}).NoticeDeadline())
	assert.Equal(t, 5*time.Second, consents.NewRevocationService(consents.RevocationDeps{NoticeDeadline: 5 * time.Second}).NoticeDeadline())
}
