package memory_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/outbox"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

var outboxNow = time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC)

func registeredCustomer(id, document string) domain.Customer {
	return domain.Customer{ID: id, DocumentID: document, FullName: "Ana", Email: id + "@x.co", CognitoSub: "sub-" + id}
}

func TestLinkStoresTheSubjectAndTheEventTogether(t *testing.T) {
	s := memory.NewCustomerStore()
	ctx := context.Background()
	c := registeredCustomer("c1", "1032456789")
	e, err := domain.NewCustomerLinked(c, outboxNow)
	require.NoError(t, err)

	stored, err := s.Link(ctx, c, e)

	require.NoError(t, err)
	assert.Equal(t, "sub-c1", stored.CognitoSub)
	got, err := s.GetByDocument(ctx, "1032456789")
	require.NoError(t, err)
	assert.True(t, got.Registered())
	pending := s.Outbox().Pending()
	require.Len(t, pending, 1)
	assert.Equal(t, "customer-linked-c1", pending[0].EventID)
	env, err := events.Decode(pending[0].Body)
	require.NoError(t, err)
	assert.Equal(t, domain.EventTypeCustomerLinked, env.EventType)
}

func TestLinkingAgainWritesNoSecondEvent(t *testing.T) {
	s := memory.NewCustomerStore()
	ctx := context.Background()
	c := registeredCustomer("c1", "1032456789")
	e, err := domain.NewCustomerLinked(c, outboxNow)
	require.NoError(t, err)

	_, err = s.Link(ctx, c, e)
	require.NoError(t, err)
	_, err = s.Link(ctx, c, e)
	require.NoError(t, err)

	assert.Len(t, s.Outbox().Pending(), 1)
}

func TestLinkRefusesAnIncompleteEvent(t *testing.T) {
	s := memory.NewCustomerStore()

	_, err := s.Link(context.Background(), registeredCustomer("c1", "1032456789"), domain.CustomerLinked{})

	assert.ErrorIs(t, err, events.ErrInvalidEnvelope)
	_, err = s.GetByDocument(context.Background(), "1032456789")
	assert.Error(t, err, "nothing is stored without its event")
	assert.Empty(t, s.Outbox().Pending())
}

func TestConcurrentLinksAreAtomic(t *testing.T) {
	s := memory.NewCustomerStore()
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := registeredCustomer("c"+string(rune('a'+i)), "10324567"+string(rune('a'+i)))
			e, err := domain.NewCustomerLinked(c, outboxNow)
			if err == nil {
				_, err = s.Link(ctx, c, e)
			}
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	assert.Len(t, s.Outbox().Pending(), 20)
}

func TestOutboxClaimHonorsTheLeaseAndTheDueTime(t *testing.T) {
	o := memory.NewOutbox()
	ctx := context.Background()
	o.Add("e1", "CustomerLinked", []byte(`{}`), outboxNow)
	o.Add("e2", "CustomerLinked", []byte(`{}`), outboxNow.Add(time.Minute))
	o.Add("e1", "CustomerLinked", []byte(`{"dup":true}`), outboxNow)

	first, err := o.Claim(ctx, outboxNow, 30*time.Second, 10)
	require.NoError(t, err)
	require.Len(t, first, 1, "e2 is not due and e1 was added once")
	assert.Equal(t, "e1", first[0].EventID)
	assert.Equal(t, 1, first[0].Attempts)
	assert.Equal(t, `{}`, string(first[0].Body))

	again, err := o.Claim(ctx, outboxNow.Add(10*time.Second), 30*time.Second, 10)
	require.NoError(t, err)
	assert.Empty(t, again, "a live lease is never claimed again")

	stolen, err := o.Claim(ctx, outboxNow.Add(31*time.Second), 30*time.Second, 1)
	require.NoError(t, err)
	require.Len(t, stolen, 1)
	assert.Equal(t, 2, stolen[0].Attempts)

	assert.ErrorIs(t, o.MarkPublished(ctx, first[0], outboxNow), outbox.ErrClaimLost)
	assert.ErrorIs(t, o.Reschedule(ctx, first[0], outboxNow, "x"), outbox.ErrClaimLost)
	require.NoError(t, o.Reschedule(ctx, stolen[0], outboxNow.Add(2*time.Minute), "connection refused"))
	pending := o.Pending()
	require.Len(t, pending, 2)
	assert.Equal(t, "connection refused", pending[0].LastError)

	due, err := o.Claim(ctx, outboxNow.Add(3*time.Minute), 30*time.Second, 10)
	require.NoError(t, err)
	require.Len(t, due, 2)
	require.NoError(t, o.MarkPublished(ctx, due[0], outboxNow.Add(3*time.Minute)))
	require.NoError(t, o.MarkPublished(ctx, due[1], outboxNow.Add(3*time.Minute)))
	assert.Empty(t, o.Pending())
	assert.ErrorIs(t, o.MarkPublished(ctx, due[0], outboxNow), outbox.ErrClaimLost, "already published")
}
