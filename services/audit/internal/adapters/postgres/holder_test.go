package postgres_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

// The holder's side of the consents (HU-M1.3-01): the consents by document,
// the append-only events and the links between accounts and documents.

func holderConsent(id, document string, grantedAt time.Time) domain.Consent {
	c := consent(id, "cust-"+id, "app-"+id, grantedAt)
	c.Holder.DocumentID = document
	return c
}

func TestListByHolderReturnsOnlyTheConsentsOfTheDocument(t *testing.T) {
	store := postgres.NewConsentStore(newDB(t))
	ctx := context.Background()
	for _, c := range []domain.Consent{
		holderConsent("cns-a1", "AB12345", base),
		holderConsent("cns-a2", "ab12345", base.Add(time.Hour)),
		holderConsent("cns-b1", "9988776655", base.Add(2*time.Hour)),
	} {
		_, _, err := store.Create(ctx, c)
		require.NoError(t, err)
	}

	own, err := store.ListByHolder(ctx, "AB12345")
	require.NoError(t, err)

	require.Len(t, own, 2, "the document is compared without case")
	assert.Equal(t, "cns-a2", own[0].ID, "most recently granted first")
	assert.Equal(t, "cns-a1", own[1].ID)

	none, err := store.ListByHolder(ctx, "0000000000")
	require.NoError(t, err)
	assert.Empty(t, none)

	lower, err := store.ListByHolder(ctx, " ab12345 ")
	require.NoError(t, err)
	assert.Len(t, lower, 2, "the argument is normalized too")
}

func insertEvent(t *testing.T, ctx context.Context, exec func(context.Context, string, ...any) error,
	id, consentID, entity, status, receipt string, at time.Time) {
	t.Helper()
	var entityArg, receiptArg any
	if entity != "" {
		entityArg = entity
	}
	if receipt != "" {
		receiptArg = receipt
	}
	require.NoError(t, exec(ctx, `INSERT INTO consent_event (event_id, consent_id, entity_code, status, occurred_at,
		actor_type, actor_id, channel, device, receipt_id, reason, reference, details)
		VALUES ($1, $2, $3, $4, $5, 'insured', 'sub-1', 'mobile', 'Pixel 8', $6, 'user_request', 'ref-1', '{"k":"v"}')`,
		id, consentID, entityArg, status, at, receiptArg))
}

func TestListByConsentsReadsTheEventsInOrder(t *testing.T) {
	db := newDB(t)
	consents, events := postgres.NewConsentStore(db), postgres.NewConsentEventStore(db)
	ctx := context.Background()
	for _, c := range []domain.Consent{holderConsent("cns-1", "1020304050", base), holderConsent("cns-2", "1020304050", base)} {
		_, _, err := consents.Create(ctx, c)
		require.NoError(t, err)
	}
	exec := func(ctx context.Context, q string, args ...any) error {
		_, err := db.ExecContext(ctx, q, args...)
		return err
	}
	insertEvent(t, ctx, exec, "e2", "cns-1", "", "revoked", "REV-0001", base.Add(2*time.Hour))
	insertEvent(t, ctx, exec, "e1", "cns-1", "", "revocation_requested", "", base.Add(time.Hour))
	insertEvent(t, ctx, exec, "e3", "cns-2", "aliado-banco-1", "revoked", "", base.Add(time.Hour))

	got, err := events.ListByConsents(ctx, []string{"cns-1", "cns-2", "cns-none"})
	require.NoError(t, err)

	require.Len(t, got["cns-1"], 2)
	assert.Equal(t, "e1", got["cns-1"][0].EventID)
	revoked := got["cns-1"][1]
	assert.Equal(t, "revoked", revoked.Status)
	assert.Equal(t, "REV-0001", revoked.ReceiptID)
	assert.Equal(t, "", revoked.EntityCode, "an event of the whole consent")
	assert.Equal(t, "insured", revoked.ActorType)
	assert.Equal(t, "sub-1", revoked.ActorID)
	assert.Equal(t, "mobile", revoked.Channel)
	assert.Equal(t, "Pixel 8", revoked.Device)
	assert.Equal(t, "user_request", revoked.Reason)
	assert.Equal(t, "ref-1", revoked.Reference)
	assert.JSONEq(t, `{"k":"v"}`, string(revoked.Details))
	assert.True(t, base.Add(2*time.Hour).Equal(revoked.OccurredAt))
	assert.False(t, revoked.RecordedAt.IsZero())
	require.Len(t, got["cns-2"], 1)
	assert.Equal(t, "aliado-banco-1", got["cns-2"][0].EntityCode)
	assert.NotContains(t, got, "cns-none")

	empty, err := events.ListByConsents(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// sqlState returns the SQLSTATE of a PostgreSQL error.
func sqlState(t *testing.T, err error) string {
	t.Helper()
	var pgErr *pgconn.PgError
	require.ErrorAs(t, err, &pgErr)
	return pgErr.Code
}

// countEvents counts the rows of consent_event with the given ids.
func countEvents(t *testing.T, db *sql.DB, ids ...string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM consent_event WHERE event_id = ANY($1::text[])`, ids).Scan(&n))
	return n
}

// The trigger consent_event_requires_consent acts as the foreign key to
// consent_record (decision #89): a foreign key would preempt the append-only
// error of TRUNCATE consent_record that HU-W1.2-01 expects.
func TestAnEventNeedsAnExistingConsent(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	_, _, err := postgres.NewConsentStore(db).Create(ctx, holderConsent("cns-1", "1020304050", base))
	require.NoError(t, err)
	const insert = `INSERT INTO consent_event (event_id, consent_id, status, occurred_at) VALUES `

	t.Run("a consent that does not exist is refused", func(t *testing.T) {
		_, err := db.ExecContext(ctx, insert+`('e-missing', 'cns-missing', 'revoked', $1)`, base)

		require.Error(t, err)
		assert.Equal(t, "23503", sqlState(t, err), "foreign_key_violation, as a foreign key")
		assert.Contains(t, err.Error(), "cns-missing")
		assert.Zero(t, countEvents(t, db, "e-missing"), "nothing is stored")
	})

	t.Run("an existing consent is accepted", func(t *testing.T) {
		_, err := db.ExecContext(ctx, insert+`('e-1', 'cns-1', 'revoked', $1)`, base)
		require.NoError(t, err)

		got, err := postgres.NewConsentEventStore(db).ListByConsents(ctx, []string{"cns-1"})
		require.NoError(t, err)
		require.Len(t, got["cns-1"], 1)
		assert.Equal(t, "e-1", got["cns-1"][0].EventID)
	})

	t.Run("one missing consent refuses every row of the statement", func(t *testing.T) {
		_, err := db.ExecContext(ctx, insert+`('e-2', 'cns-1', 'revocation_requested', $1), ('e-3', 'cns-missing', 'revoked', $1)`, base)

		require.Error(t, err)
		assert.Equal(t, "23503", sqlState(t, err))
		assert.Zero(t, countEvents(t, db, "e-2", "e-3"), "neither row is stored")
	})

	t.Run("a NULL consent is refused by NOT NULL", func(t *testing.T) {
		_, err := db.ExecContext(ctx, insert+`('e-null', NULL, 'revoked', $1)`, base)

		require.Error(t, err)
		assert.Equal(t, "23502", sqlState(t, err), "not_null_violation, as with a foreign key")
	})

	assert.Equal(t, 1, countEvents(t, db, "e-missing", "e-1", "e-2", "e-3", "e-null"))
}

func TestHolderLinkIsInsertedOnceAndRead(t *testing.T) {
	store := postgres.NewHolderLinkStore(newDB(t))
	ctx := context.Background()
	l := domain.HolderLink{Subject: "sub-1", DocumentID: "1020304050", CustomerID: "cus-1", LinkedAt: base, EventID: "customer-linked-cus-1"}

	stored, created, err := store.Link(ctx, l)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, l, stored)

	again, created, err := store.Link(ctx, l)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, l, again)

	conflict := l
	conflict.DocumentID, conflict.EventID = "9999999999", "customer-linked-cus-9"
	kept, created, err := store.Link(ctx, conflict)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, "1020304050", kept.DocumentID, "the first link of a subject is kept")

	doc, err := store.DocumentOf(ctx, "sub-1")
	require.NoError(t, err)
	assert.Equal(t, "1020304050", doc)

	_, err = store.DocumentOf(ctx, "sub-unknown")
	assert.ErrorIs(t, err, ports.ErrHolderNotLinked)

	otherSubject := l
	otherSubject.Subject = "sub-2"
	_, _, err = store.Link(ctx, otherSubject)
	assert.Error(t, err, "the same event cannot link a second account")
}

func TestTheHolderTablesAreAppendOnly(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	consents, links := postgres.NewConsentStore(db), postgres.NewHolderLinkStore(db)
	_, _, err := consents.Create(ctx, holderConsent("cns-1", "1020304050", base))
	require.NoError(t, err)
	exec := func(ctx context.Context, q string, args ...any) error {
		_, err := db.ExecContext(ctx, q, args...)
		return err
	}
	insertEvent(t, ctx, exec, "e1", "cns-1", "", "revoked", "REV-1", base)
	_, _, err = links.Link(ctx, domain.HolderLink{Subject: "sub-1", DocumentID: "1020304050", CustomerID: "cus-1", LinkedAt: base, EventID: "ev-1"})
	require.NoError(t, err)

	for _, stmt := range []string{
		`UPDATE consent_event SET status = 'active'`,
		`DELETE FROM consent_event`,
		`TRUNCATE consent_event`,
		`UPDATE holder_link SET document_id = '9999999999'`,
		`DELETE FROM holder_link`,
		`TRUNCATE holder_link`,
	} {
		_, err := db.ExecContext(ctx, stmt)
		require.Error(t, err, stmt)
		assert.Contains(t, err.Error(), "is append-only", stmt)
	}
}
