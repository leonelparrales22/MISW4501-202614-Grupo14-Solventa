package postgres_test

import (
	"context"
	"encoding/json"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/outbox"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/migrations"
)

// The outbox of origination in PostgreSQL (HU-M1.3-01).

var linkAt = time.Date(2026, 10, 24, 20, 0, 0, 123_456_789, time.UTC)

func linkedCustomer(t *testing.T, id, document string) (domain.Customer, domain.CustomerLinked) {
	t.Helper()
	c := cliente(id, document, id+"@example.com")
	c.CognitoSub = "sub-" + id
	e, err := domain.NewCustomerLinked(c, linkAt)
	require.NoError(t, err)
	return c, e
}

func TestLinkStoresTheSubjectAndTheEventInOneTransaction(t *testing.T) {
	db := newDB(t)
	s := postgres.NewCustomerStore(db)
	ctx := context.Background()
	c := cliente("cus-1", "1020345678", "carolina@example.com")
	_, err := s.Save(ctx, c)
	require.NoError(t, err)
	c.CognitoSub = "sub-carolina"
	e, err := domain.NewCustomerLinked(c, linkAt)
	require.NoError(t, err)

	stored, err := s.Link(ctx, c, e)
	require.NoError(t, err)
	assert.Equal(t, "sub-carolina", stored.CognitoSub)

	var (
		eventType string
		payload   []byte
		attempts  int
		occurred  time.Time
	)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT event_type, payload, attempts, occurred_at FROM outbox_event
		WHERE event_id = 'customer-linked-cus-1'`).Scan(&eventType, &payload, &attempts, &occurred))
	assert.Equal(t, "CustomerLinked", eventType)
	assert.Zero(t, attempts)
	assert.True(t, linkAt.Truncate(time.Microsecond).Equal(occurred))
	env, err := events.Decode(payload)
	require.NoError(t, err)
	var p map[string]any
	require.NoError(t, json.Unmarshal(env.Payload, &p))
	assert.Equal(t, "sub-carolina", p["subject"])
	assert.Equal(t, "1020345678", p["document_id"])

	_, err = s.Link(ctx, c, e)
	require.NoError(t, err)
	var n int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM outbox_event`).Scan(&n))
	assert.Equal(t, 1, n, "linking again writes no second event")
}

func TestLinkRefusesAnEventOfAnotherCustomer(t *testing.T) {
	db := newDB(t)
	s := postgres.NewCustomerStore(db)
	ctx := context.Background()
	c, _ := linkedCustomer(t, "cus-1", "1020345678")
	_, other := linkedCustomer(t, "cus-2", "9999999999")

	_, err := s.Link(ctx, c, other)

	require.Error(t, err)
	var n int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM customer`).Scan(&n))
	assert.Zero(t, n, "the transaction was rolled back")
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM outbox_event`).Scan(&n))
	assert.Zero(t, n)
}

func TestOutboxStoreClaimsPublishesAndReschedules(t *testing.T) {
	db := newDB(t)
	customers, store := postgres.NewCustomerStore(db), postgres.NewOutboxStore(db)
	ctx := context.Background()
	for _, id := range []string{"cus-1", "cus-2", "cus-3"} {
		c, e := linkedCustomer(t, id, "10203456"+id[len(id)-1:])
		_, err := customers.Link(ctx, c, e)
		require.NoError(t, err)
	}

	early, err := store.Claim(ctx, linkAt.Add(-time.Second), 30*time.Second, 10)
	require.NoError(t, err)
	assert.Empty(t, early, "not due before the instant of the link")

	claimed, err := store.Claim(ctx, linkAt, 30*time.Second, 2)
	require.NoError(t, err)
	require.Len(t, claimed, 2, "the limit holds")
	assert.Equal(t, "customer-linked-cus-1", claimed[0].EventID, "oldest first")
	assert.Equal(t, 1, claimed[0].Attempts)
	assert.Equal(t, "CustomerLinked", claimed[0].EventType)
	_, err = events.Decode(claimed[0].Body)
	require.NoError(t, err, "the body is the envelope")

	require.NoError(t, store.MarkPublished(ctx, claimed[0], linkAt.Add(time.Second)))
	next := linkAt.Add(time.Minute)
	require.NoError(t, store.Reschedule(ctx, claimed[1], next, "connection refused"))
	assert.ErrorIs(t, store.MarkPublished(ctx, claimed[0], linkAt), outbox.ErrClaimLost, "already published")
	assert.ErrorIs(t, store.Reschedule(ctx, claimed[1], next, "again"), outbox.ErrClaimLost, "the claim was released")

	var lastError string
	var published *time.Time
	require.NoError(t, db.QueryRowContext(ctx, `SELECT last_error, published_at FROM outbox_event WHERE event_id = $1`,
		claimed[1].EventID).Scan(&lastError, &published))
	assert.Equal(t, "connection refused", lastError)
	assert.Nil(t, published)

	rest, err := store.Claim(ctx, linkAt.Add(time.Second), 10*time.Minute, 10)
	require.NoError(t, err)
	require.Len(t, rest, 1, "the rescheduled one waits; the third is due")
	assert.Equal(t, "customer-linked-cus-3", rest[0].EventID)

	later, err := store.Claim(ctx, next.Add(time.Second), 30*time.Second, 10)
	require.NoError(t, err)
	require.Len(t, later, 1, "the third still holds its lease; the rescheduled one is due again")
	assert.Equal(t, claimed[1].EventID, later[0].EventID)
	assert.Equal(t, 2, later[0].Attempts)
}

func TestAnExpiredLeaseIsClaimedAgainAndTheFirstClaimIsLost(t *testing.T) {
	db := newDB(t)
	customers, store := postgres.NewCustomerStore(db), postgres.NewOutboxStore(db)
	ctx := context.Background()
	c, e := linkedCustomer(t, "cus-1", "1020345678")
	_, err := customers.Link(ctx, c, e)
	require.NoError(t, err)

	first, err := store.Claim(ctx, linkAt, time.Second, 10)
	require.NoError(t, err)
	require.Len(t, first, 1)
	live, err := store.Claim(ctx, linkAt.Add(500*time.Millisecond), time.Second, 10)
	require.NoError(t, err)
	assert.Empty(t, live, "a live lease is never claimed again")

	second, err := store.Claim(ctx, linkAt.Add(2*time.Second), time.Second, 10)
	require.NoError(t, err)
	require.Len(t, second, 1)

	assert.ErrorIs(t, store.MarkPublished(ctx, first[0], linkAt.Add(2*time.Second)), outbox.ErrClaimLost)
	assert.ErrorIs(t, store.Reschedule(ctx, first[0], linkAt, "x"), outbox.ErrClaimLost)
	require.NoError(t, store.MarkPublished(ctx, second[0], linkAt.Add(2*time.Second)))
}

// Migration 0008 enqueues a CustomerLinked for every customer that registered
// before it, with the same envelope the service builds.
func TestMigration0008EnqueuesTheLinksOfTheCustomersAlreadyRegistered(t *testing.T) {
	db := newUnmigratedDB(t)
	ctx := context.Background()
	old := fstest.MapFS{}
	names, err := fs.Glob(migrations.FS, "*.sql")
	require.NoError(t, err)
	for _, name := range names {
		if name >= "0008" {
			continue
		}
		data, err := migrations.FS.ReadFile(name)
		require.NoError(t, err)
		old[name] = &fstest.MapFile{Data: data}
	}
	require.NoError(t, postgres.MigrateFS(ctx, db, old))
	customers := postgres.NewCustomerStore(db)
	registered := cliente("cus-1", "1020345678", "carolina@example.com")
	registered.CognitoSub = "sub-carolina"
	_, err = customers.Save(ctx, registered)
	require.NoError(t, err)
	_, err = customers.Save(ctx, cliente("cus-2", "9999999999", "half@example.com"))
	require.NoError(t, err, "a registration left halfway, without subject")

	require.NoError(t, postgres.Migrate(ctx, db))

	claimed, err := postgres.NewOutboxStore(db).Claim(ctx, time.Now().Add(time.Second), 30*time.Second, 10)
	require.NoError(t, err)
	require.Len(t, claimed, 1, "only the customer with a subject")
	m := claimed[0]
	assert.Equal(t, "customer-linked-cus-1", m.EventID)
	env, err := events.Decode(m.Body)
	require.NoError(t, err)
	assert.Equal(t, "CustomerLinked", env.EventType)
	assert.Equal(t, 1, env.EventVersion)
	assert.Equal(t, "origination", env.Source)
	assert.True(t, t0.Equal(env.OccurredAt), "the link instant is the updated_at of the customer")
	var p struct {
		CustomerID string    `json:"customer_id"`
		Subject    string    `json:"subject"`
		DocumentID string    `json:"document_id"`
		LinkedAt   time.Time `json:"linked_at"`
	}
	require.NoError(t, env.DecodePayload(&p))
	assert.Equal(t, "cus-1", p.CustomerID)
	assert.Equal(t, "sub-carolina", p.Subject)
	assert.Equal(t, "1020345678", p.DocumentID)
	assert.True(t, t0.Equal(p.LinkedAt))

	// The same shape as the event the service writes: the consumer reads both.
	e, err := domain.NewCustomerLinked(registered, t0)
	require.NoError(t, err)
	fromGo, err := e.Envelope()
	require.NoError(t, err)
	var a, b map[string]any
	require.NoError(t, json.Unmarshal(fromGo.Payload, &a))
	require.NoError(t, json.Unmarshal(env.Payload, &b))
	assert.ElementsMatch(t, keys(a), keys(b))
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
