// This file checks the outbox of origination (HU-M1.3-01) against a real
// PostgreSQL: the link of an account and its CustomerLinked event are atomic,
// and the claims of several relays never hand the same event to two of them
// while a lease is live.
package integration_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/outbox"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

var outboxAt = time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC)

func outboxCustomer(t *testing.T, i int) (domain.Customer, domain.CustomerLinked) {
	t.Helper()
	c := domain.Customer{
		ID: fmt.Sprintf("cus-%02d", i), DocumentID: fmt.Sprintf("10203456%02d", i), FullName: "Asegurado de prueba",
		Email: fmt.Sprintf("asegurado%02d@example.com", i), BirthDate: time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC),
		CognitoSub: fmt.Sprintf("sub-%02d", i), CreatedAt: outboxAt, UpdatedAt: outboxAt,
	}
	e, err := domain.NewCustomerLinked(c, outboxAt)
	require.NoError(t, err)
	return c, e
}

// If the event cannot be written, the subject is not stored either.
func TestLinkIsAtomicWhenTheOutboxWriteFails(t *testing.T) {
	db := newChargeDB(t)
	ctx := context.Background()
	_, err := db.ExecContext(ctx, `CREATE FUNCTION refuse_outbox() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'the outbox is not writable'; END; $$ LANGUAGE plpgsql;
		CREATE TRIGGER refuse_outbox BEFORE INSERT ON outbox_event FOR EACH ROW EXECUTE FUNCTION refuse_outbox();`)
	require.NoError(t, err)
	customers := postgres.NewCustomerStore(db)
	c, e := outboxCustomer(t, 1)
	before := c
	before.CognitoSub = ""
	_, err = customers.Save(ctx, before)
	require.NoError(t, err)

	_, err = customers.Link(ctx, c, e)

	require.ErrorContains(t, err, "the outbox is not writable")
	stored, err := customers.GetByDocument(ctx, c.DocumentID)
	require.NoError(t, err)
	assert.False(t, stored.Registered(), "no subject without its event")
	var n int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM outbox_event`).Scan(&n))
	assert.Zero(t, n)
}

// 20 relays claim at once: every event goes to exactly one of them.
func TestConcurrentClaimsHandEachEventToASingleRelay(t *testing.T) {
	db := newChargeDB(t)
	ctx := context.Background()
	customers, store := postgres.NewCustomerStore(db), postgres.NewOutboxStore(db)
	const total = 30
	for i := range total {
		c, e := outboxCustomer(t, i)
		_, err := customers.Link(ctx, c, e)
		require.NoError(t, err)
	}

	const relays = 20
	var (
		mu     sync.Mutex
		owners = map[string]int{}
		wg     sync.WaitGroup
		start  = make(chan struct{})
		errs   = make([]error, relays)
	)
	for r := range relays {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			msgs, err := store.Claim(ctx, outboxAt.Add(time.Second), 30*time.Second, 3)
			errs[r] = err
			mu.Lock()
			defer mu.Unlock()
			for _, m := range msgs {
				owners[m.EventID]++
			}
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range errs {
		require.NoError(t, err)
	}
	for id, n := range owners {
		assert.Equal(t, 1, n, "event %s was claimed by %d relays", id, n)
	}
	// Under contention a relay may find every due row locked and claim fewer
	// than three; a second round takes whatever is left, still once each.
	rest, err := store.Claim(ctx, outboxAt.Add(time.Second), 30*time.Second, total)
	require.NoError(t, err)
	for _, m := range rest {
		owners[m.EventID]++
	}
	assert.Len(t, owners, total)
	for id, n := range owners {
		assert.Equal(t, 1, n, "event %s was claimed %d times while its lease was live", id, n)
	}
}

// A relay that dies keeps its lease until it expires; then another relay
// takes the event and the late one cannot overwrite it.
func TestAnExpiredLeaseMovesTheEventToAnotherRelay(t *testing.T) {
	db := newChargeDB(t)
	ctx := context.Background()
	customers, store := postgres.NewCustomerStore(db), postgres.NewOutboxStore(db)
	c, e := outboxCustomer(t, 1)
	_, err := customers.Link(ctx, c, e)
	require.NoError(t, err)

	slow, err := store.Claim(ctx, outboxAt, 5*time.Second, 1)
	require.NoError(t, err)
	require.Len(t, slow, 1)
	none, err := store.Claim(ctx, outboxAt.Add(4*time.Second), 5*time.Second, 1)
	require.NoError(t, err)
	require.Empty(t, none)

	other, err := store.Claim(ctx, outboxAt.Add(6*time.Second), 5*time.Second, 1)
	require.NoError(t, err)
	require.Len(t, other, 1)
	require.NoError(t, store.MarkPublished(ctx, other[0], outboxAt.Add(6*time.Second)))

	assert.ErrorIs(t, store.MarkPublished(ctx, slow[0], outboxAt.Add(7*time.Second)), outbox.ErrClaimLost)
	var attempts int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT attempts FROM outbox_event`).Scan(&attempts))
	assert.Equal(t, 2, attempts)
}
