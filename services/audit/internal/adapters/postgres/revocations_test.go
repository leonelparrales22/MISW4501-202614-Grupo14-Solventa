package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// The revocation of a consent (HU-M1.3-02): the immutable record, its events
// and the first step of the notice to each entity, in one transaction.

const revEntity = "partner-1"

// granted stores a consent of the partner revEntity and returns it.
func granted(t *testing.T, db *sql.DB, id string) domain.Consent {
	t.Helper()
	c := consent(id, "cust-"+id, "app-"+id, base)
	_, _, err := postgres.NewConsentStore(db).Create(context.Background(), c)
	require.NoError(t, err)
	return c
}

// revocationOf builds the revocation of c requested by the holder, effective
// at effective, with one notice per entity.
func revocationOf(t *testing.T, c domain.Consent, requestID string, effective time.Time, entities ...string) (domain.Revocation, []ports.NoticeRecord) {
	t.Helper()
	if len(entities) == 0 {
		entities = []string{revEntity}
	}
	deadline := effective.Add(domain.RevocationNoticeDeadline)
	rev := domain.Revocation{
		ID: "rvk-" + randomHex(t, 8), ConsentID: c.ID, Holder: c.Holder, Scope: c.Scope,
		RequestedAt: effective.Add(-2 * time.Second), EffectiveAt: effective, NoticeDeadlineAt: deadline,
		Channel: domain.RevocationChannelMobileApp, Device: "Google Pixel 8", ConfirmationMethod: domain.ConfirmationBiometric,
		Actor: domain.Actor{Type: domain.ActorHolder, ID: "sub-1"}, RequestID: requestID,
	}
	notices := make([]ports.NoticeRecord, 0, len(entities))
	for _, e := range entities {
		rev.Entities = append(rev.Entities, domain.Entity{Code: e, Name: "Banco " + e})
		key, err := idempotency.Key("revocation", c.ID+"-"+e)
		require.NoError(t, err)
		notices = append(notices, ports.NoticeRecord{
			ConsentID: c.ID, EntityCode: e, Key: key, WindowEndsAt: deadline, NextAttemptAt: effective,
		})
	}
	return rev, notices
}

func count(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(query, args...).Scan(&n))
	return n
}

func TestRecordStoresTheRevocationItsEventsAndTheNotices(t *testing.T) {
	db := newDB(t)
	store := postgres.NewRevocationStore(db)
	ctx := context.Background()
	c := granted(t, db, "cns-1")
	effective := base.AddDate(0, 1, 0)
	rev, notices := revocationOf(t, c, "req-1", effective)

	stored, created, err := store.Record(ctx, rev, notices)

	require.NoError(t, err)
	assert.True(t, created)
	assert.True(t, domain.ValidReceiptNumber(stored.ReceiptNumber), stored.ReceiptNumber)
	assert.Equal(t, "REV-2026-", stored.ReceiptNumber[:9], "the year of the effective time in Colombia")
	got, err := store.GetByConsent(ctx, "cns-1")
	require.NoError(t, err)
	assert.Equal(t, stored, got, "every field survives the round trip")

	byConsent, err := postgres.NewConsentEventStore(db).ListByConsents(ctx, []string{"cns-1"})
	require.NoError(t, err)
	events := byConsent["cns-1"]
	require.Len(t, events, 2)
	assert.Equal(t, domain.HistoryRevocationRequested, events[0].Status)
	assert.True(t, rev.RequestedAt.Equal(events[0].OccurredAt), "the request keeps the time of the device")
	assert.Equal(t, domain.HistoryRevoked, events[1].Status)
	assert.True(t, effective.Equal(events[1].OccurredAt))
	assert.Equal(t, stored.ReceiptNumber, events[1].ReceiptID)
	for _, e := range events {
		assert.Equal(t, "mobile_app", e.Channel)
		assert.Equal(t, "Google Pixel 8", e.Device)
		assert.Equal(t, "holder", e.ActorType)
		assert.Equal(t, "sub-1", e.ActorID)
	}

	v := domain.View(c, events, effective.Add(time.Second), nil)
	assert.Equal(t, domain.StatusRevoked, v.Status)
	require.NotNil(t, v.Revocation)
	require.Len(t, v.Revocation.Entities, 1)
	assert.Equal(t, domain.NoticePending, v.Revocation.Entities[0].Status)
	assert.True(t, rev.NoticeDeadlineAt.Equal(v.Revocation.Entities[0].DeadlineAt))

	assert.Equal(t, 1, count(t, db, `SELECT count(*) FROM revocation_notice WHERE consent_id = 'cns-1' AND idempotency_key = $1`, notices[0].Key))
	assert.Equal(t, 1, count(t, db, `SELECT count(*) FROM revocation_notice_step
		WHERE consent_id = 'cns-1' AND seq = 1 AND status = 'pending' AND next_attempt_at = $1`, effective))
}

func TestRecordIsIdempotentByRequest(t *testing.T) {
	db := newDB(t)
	store := postgres.NewRevocationStore(db)
	ctx := context.Background()
	c := granted(t, db, "cns-1")
	rev, notices := revocationOf(t, c, "req-1", base.AddDate(0, 1, 0))
	first, created, err := store.Record(ctx, rev, notices)
	require.NoError(t, err)
	require.True(t, created)

	again, notices2 := revocationOf(t, c, "req-1", base.AddDate(0, 1, 1))
	second, created, err := store.Record(ctx, again, notices2)

	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first, second, "the same request returns the stored revocation")
	assert.Equal(t, 1, count(t, db, `SELECT count(*) FROM consent_revocation`))
	assert.Equal(t, 2, count(t, db, `SELECT count(*) FROM consent_event`))
	assert.Equal(t, 1, count(t, db, `SELECT count(*) FROM revocation_notice_step`))
}

func TestRecordOfAnotherRequestOnARevokedConsentIsAlreadyRevoked(t *testing.T) {
	db := newDB(t)
	store := postgres.NewRevocationStore(db)
	ctx := context.Background()
	c := granted(t, db, "cns-1")
	rev, notices := revocationOf(t, c, "req-1", base.AddDate(0, 1, 0))
	_, _, err := store.Record(ctx, rev, notices)
	require.NoError(t, err)

	other, notices2 := revocationOf(t, c, "req-2", base.AddDate(0, 1, 1))
	_, created, err := store.Record(ctx, other, notices2)

	assert.ErrorIs(t, err, domain.ErrAlreadyRevoked)
	assert.False(t, created)
	assert.Equal(t, 1, count(t, db, `SELECT count(*) FROM consent_revocation`))
}

func TestRecordWithTheKeyOfAnotherConsentIsRefused(t *testing.T) {
	db := newDB(t)
	store := postgres.NewRevocationStore(db)
	ctx := context.Background()
	rev, notices := revocationOf(t, granted(t, db, "cns-1"), "req-1", base.AddDate(0, 1, 0))
	_, _, err := store.Record(ctx, rev, notices)
	require.NoError(t, err)

	other, notices2 := revocationOf(t, granted(t, db, "cns-2"), "req-1", base.AddDate(0, 1, 0))
	_, _, err = store.Record(ctx, other, notices2)

	assert.ErrorIs(t, err, domain.ErrIdempotencyKeyReused)
	_, err = store.GetByConsent(ctx, "cns-2")
	assert.ErrorIs(t, err, ports.ErrRevocationNotFound, "nothing was recorded for the second consent")
}

func TestGetByConsentWithoutRevocationIsNotFound(t *testing.T) {
	db := newDB(t)
	granted(t, db, "cns-1")

	_, err := postgres.NewRevocationStore(db).GetByConsent(context.Background(), "cns-1")

	assert.ErrorIs(t, err, ports.ErrRevocationNotFound)
}

func TestRecordKeepsTheOptionalFieldsOfThePortal(t *testing.T) {
	db := newDB(t)
	store := postgres.NewRevocationStore(db)
	ctx := context.Background()
	rev, notices := revocationOf(t, granted(t, db, "cns-1"), "req-1", base.AddDate(0, 1, 0))
	rev.Channel, rev.Device, rev.ConfirmationMethod = "compliance_portal", "", ""
	rev.Actor = domain.Actor{Type: "operations", ID: "sub-ops"}
	rev.Reason, rev.Reference = "customer_request", "TICKET-1"

	stored, _, err := store.Record(ctx, rev, notices)
	require.NoError(t, err)
	got, err := store.GetByConsent(ctx, "cns-1")

	require.NoError(t, err)
	assert.Equal(t, stored, got)
	assert.Empty(t, got.Device)
	assert.Equal(t, "TICKET-1", got.Reference)
	assert.Equal(t, 1, count(t, db, `SELECT count(*) FROM consent_event WHERE reason = 'customer_request' AND reference = 'TICKET-1'
		AND status = 'revoked' AND device IS NULL`))
}

func TestRecordOfAnUnknownConsentIsAForeignKeyViolation(t *testing.T) {
	db := newDB(t)
	c := consent("cns-missing", "cust-x", "app-x", base)
	rev, notices := revocationOf(t, c, "req-1", base.AddDate(0, 1, 0))

	_, _, err := postgres.NewRevocationStore(db).Record(context.Background(), rev, notices)

	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "got %v", err)
	assert.Equal(t, "23503", pgErr.Code, "require_existing_consent() acts as the foreign key (decision #89)")
	assert.Equal(t, 0, count(t, db, `SELECT count(*) FROM consent_revocation`))
}
