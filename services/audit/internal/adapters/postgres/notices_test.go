package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// The queue of notices to the entities (HU-M1.3-02, decision D-3 a): every
// change of state is a new step; the claim is one INSERT ... ON CONFLICT.

// revoked records the revocation of a new consent with the entities and
// returns the consent and the effective time.
func revoked(t *testing.T, db *sql.DB, id string, entities ...string) (domain.Consent, time.Time) {
	t.Helper()
	c := granted(t, db, id)
	effective := base.AddDate(0, 1, 0)
	rev, notices := revocationOf(t, c, "req-"+id, effective, entities...)
	_, created, err := postgres.NewRevocationStore(db).Record(context.Background(), rev, notices)
	require.NoError(t, err)
	require.True(t, created)
	return c, effective
}

func eventsOf(t *testing.T, db *sql.DB, consentID string) []domain.ConsentEvent {
	t.Helper()
	byConsent, err := postgres.NewConsentEventStore(db).ListByConsents(context.Background(), []string{consentID})
	require.NoError(t, err)
	return byConsent[consentID]
}

func TestClaimThenConfirmWritesTheStepsAndTheConfirmedEvent(t *testing.T) {
	db := newDB(t)
	store := postgres.NewNoticeStore(db)
	ctx := context.Background()
	c, effective := revoked(t, db, "cns-1")
	id := ports.NoticeID("cns-1", revEntity)

	rec, claimed, err := store.Claim(ctx, id, effective, 5*time.Second)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Equal(t, 2, rec.Seq)
	assert.Equal(t, idempotency.StatusInProgress, rec.Status)
	assert.Equal(t, "revocation-cns-1-"+revEntity, rec.Key)
	assert.Equal(t, c.CustomerID, rec.CustomerID, "the customer of the partner, never the document")
	assert.True(t, effective.Equal(rec.EffectiveAt))
	assert.True(t, effective.Add(5*time.Second).Equal(rec.ClaimedUntil))
	assert.Equal(t, id, rec.AttemptState().ID)

	stored, err := store.Confirm(ctx, rec, effective.Add(time.Second), idempotency.Outcome{Reference: "of-rev-1"})
	require.NoError(t, err)
	assert.Equal(t, 3, stored.Seq)
	assert.Equal(t, idempotency.StatusConfirmed, stored.Status)

	events := eventsOf(t, db, "cns-1")
	require.Len(t, events, 3)
	confirmed := events[2]
	assert.Equal(t, domain.HistoryRevocationConfirmed, confirmed.Status)
	assert.Equal(t, revEntity, confirmed.EntityCode)
	assert.Equal(t, "of-rev-1", confirmed.Reference)
	assert.Equal(t, "system", confirmed.ActorType)
	v := domain.View(c, events, effective.Add(time.Hour), nil)
	assert.Equal(t, domain.StatusRevoked, v.Status)
	assert.Equal(t, domain.NoticeCompleted, v.Revocation.Summary)

	_, claimed, err = store.Claim(ctx, id, effective.Add(time.Hour), 5*time.Second)
	require.NoError(t, err)
	assert.False(t, claimed, "a confirmed notice is never claimed again")
	_, err = store.Confirm(ctx, rec, effective.Add(2*time.Second), idempotency.Outcome{})
	assert.ErrorIs(t, err, idempotency.ErrClaimLost, "the claim already recorded its result")
}

func TestRescheduleThenFailWritesTheNotConfirmedEvent(t *testing.T) {
	db := newDB(t)
	store := postgres.NewNoticeStore(db)
	ctx := context.Background()
	c, effective := revoked(t, db, "cns-1")
	id := ports.NoticeID("cns-1", revEntity)

	rec, claimed, err := store.Claim(ctx, id, effective, 5*time.Second)
	require.NoError(t, err)
	require.True(t, claimed)
	pending, err := store.Reschedule(ctx, rec, effective, effective.Add(time.Second), "status 503")
	require.NoError(t, err)
	assert.Equal(t, idempotency.StatusPending, pending.Status)
	assert.Equal(t, 1, pending.FailedAttempts)
	assert.Equal(t, "status 503", pending.Cause)

	_, claimed, err = store.Claim(ctx, id, effective.Add(500*time.Millisecond), 5*time.Second)
	require.NoError(t, err)
	assert.False(t, claimed, "not due yet")
	rec, claimed, err = store.Claim(ctx, id, effective.Add(time.Second), 5*time.Second)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Equal(t, 1, rec.FailedAttempts, "the count is kept across the claim")
	assert.Equal(t, rec.Key, pending.Key, "the same key on every attempt")

	failed, err := store.Fail(ctx, rec, effective.Add(2*time.Second), idempotency.ReasonWindowExpired, "status 503")
	require.NoError(t, err)
	assert.Equal(t, idempotency.StatusFailed, failed.Status)
	assert.Equal(t, 2, failed.FailedAttempts)

	events := eventsOf(t, db, "cns-1")
	last := events[len(events)-1]
	assert.Equal(t, domain.HistoryRevocationNotConfirmed, last.Status)
	assert.Equal(t, string(idempotency.ReasonWindowExpired), last.Reason)
	v := domain.View(c, events, effective.Add(3*time.Second), nil)
	assert.Equal(t, domain.StatusPartiallyRevoked, v.Status)
	assert.Equal(t, 1, count(t, db, `SELECT count(*) FROM revocation_notice_step WHERE status = 'failed' AND reason = 'retry_window_expired'`))
}

func TestClaimOfAnUnknownOrMalformedNoticeClaimsNothing(t *testing.T) {
	db := newDB(t)
	store := postgres.NewNoticeStore(db)
	revoked(t, db, "cns-1")

	for _, id := range []string{ports.NoticeID("cns-1", "other-bank"), ports.NoticeID("cns-x", revEntity), "no-separator", "/x", "x/"} {
		_, claimed, err := store.Claim(context.Background(), id, base.AddDate(1, 0, 0), time.Second)
		require.NoError(t, err, id)
		assert.False(t, claimed, id)
	}
}

func TestDueListsPendingNoticesAndExpiredLeases(t *testing.T) {
	db := newDB(t)
	store := postgres.NewNoticeStore(db)
	ctx := context.Background()
	_, effective := revoked(t, db, "cns-1", "bank-a", "bank-b", "bank-c")

	due, err := store.Due(ctx, effective.Add(-time.Second), 10)
	require.NoError(t, err)
	assert.Empty(t, due, "nothing is due before the effective time")

	due, err = store.Due(ctx, effective, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"cns-1/bank-a", "cns-1/bank-b", "cns-1/bank-c"}, due)

	a, _, err := store.Claim(ctx, "cns-1/bank-a", effective, 5*time.Second)
	require.NoError(t, err)
	b, _, err := store.Claim(ctx, "cns-1/bank-b", effective, 5*time.Second)
	require.NoError(t, err)
	_, err = store.Confirm(ctx, b, effective.Add(time.Second), idempotency.Outcome{})
	require.NoError(t, err)

	due, err = store.Due(ctx, effective.Add(time.Second), 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"cns-1/bank-c"}, due, "a held claim and a confirmed notice are not due")
	due, err = store.Due(ctx, a.ClaimedUntil.Add(time.Millisecond), 10)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"cns-1/bank-c", "cns-1/bank-a"}, due, "an expired lease is due again")
	due, err = store.Due(ctx, a.ClaimedUntil.Add(time.Millisecond), 1)
	require.NoError(t, err)
	assert.Len(t, due, 1, "at most limit")
}

func TestResumeRetriesOnlyTheNoticesThatDidNotConfirm(t *testing.T) {
	db := newDB(t)
	store := postgres.NewNoticeStore(db)
	ctx := context.Background()
	c, effective := revoked(t, db, "cns-1", "bank-a", "bank-b", "bank-c")
	actor := domain.Actor{Type: "operations", ID: "sub-ops"}

	a, _, err := store.Claim(ctx, "cns-1/bank-a", effective, 5*time.Second)
	require.NoError(t, err)
	_, err = store.Confirm(ctx, a, effective.Add(time.Second), idempotency.Outcome{})
	require.NoError(t, err)
	b, _, err := store.Claim(ctx, "cns-1/bank-b", effective, 5*time.Second)
	require.NoError(t, err)
	_, err = store.Fail(ctx, b, effective.Add(time.Second), idempotency.ReasonRejected, "status 400")
	require.NoError(t, err)

	first := effective.Add(time.Minute)
	resumed, err := store.Resume(ctx, "cns-1", nil, first, 10*time.Minute, actor)
	require.NoError(t, err)
	assert.Equal(t, []string{"bank-b"}, resumed, "bank-b failed; bank-c is pending and its window is still open; bank-a confirmed")

	later := effective.Add(domain.RevocationNoticeDeadline)
	resumed, err = store.Resume(ctx, "cns-1", nil, later, time.Minute, actor)
	require.NoError(t, err)
	assert.Equal(t, []string{"bank-c"}, resumed, "bank-b is pending again in its new window; bank-c's window ended")

	only, err := store.Resume(ctx, "cns-1", []string{"bank-a"}, later.Add(time.Hour), time.Minute, actor)
	require.NoError(t, err)
	assert.Empty(t, only, "a confirmed notice is never resumed")

	events := eventsOf(t, db, "cns-1")
	retries := 0
	for _, e := range events {
		if e.Status != domain.HistoryRevocationRetryRequested {
			continue
		}
		retries++
		assert.Equal(t, "operations", e.ActorType)
		assert.Equal(t, "sub-ops", e.ActorID)
		var d domain.RetryDetails
		require.NoError(t, json.Unmarshal(e.Details, &d))
		assert.True(t, d.DeadlineAt.After(e.OccurredAt), "the retry carries the end of its new window")
	}
	assert.Equal(t, 2, retries)

	due, err := store.Due(ctx, later, 10)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"cns-1/bank-b", "cns-1/bank-c"}, due)
	b2, claimed, err := store.Claim(ctx, "cns-1/bank-b", later, 5*time.Second)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Equal(t, 0, b2.FailedAttempts, "a new window starts the count again")
	assert.True(t, first.Add(10*time.Minute).Equal(b2.WindowEndsAt), "window %s", b2.WindowEndsAt)

	v := domain.View(c, events, later.Add(time.Second), nil)
	assert.Equal(t, domain.StatusPartiallyRevoked, v.Status, "partial until the entities confirm")
}
