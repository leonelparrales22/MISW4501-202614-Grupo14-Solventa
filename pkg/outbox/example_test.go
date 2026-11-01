package outbox_test

import (
	"context"
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/outbox"
)

// printPublisher stands for snspublisher: it prints what it publishes, and
// fails while the broker is down.
type printPublisher struct{ down bool }

func (p *printPublisher) Publish(_ context.Context, e events.Envelope) error {
	if p.down {
		return fmt.Errorf("the broker is down")
	}
	fmt.Println("published", e.EventType, e.EventID)
	return nil
}

// ExampleRelay writes an event with the business operation (here, straight
// into an in-memory store standing for the service's outbox table), then
// relays it: while the broker is down the event stays in the outbox, and it
// is published once the broker answers.
func ExampleRelay() {
	now := time.Date(2026, 10, 24, 20, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	store := newMemStore()
	e, _ := events.New("customer-linked-cus-1", "CustomerLinked", "origination", 1, now,
		map[string]string{"customer_id": "cus-1"})
	store.add(e, now) // in the same transaction as the business change

	broker := &printPublisher{down: true}
	relay, err := outbox.NewRelay(store, broker, clock, outbox.DefaultConfig())
	if err != nil {
		fmt.Println(err)
		return
	}

	_, _ = relay.ProcessOnce(context.Background())
	fmt.Println("pending after the outage:", store.get("customer-linked-cus-1").publishedAt.IsZero())

	broker.down = false
	now = now.Add(time.Second) // past the first retry wait (500 ms)
	_, _ = relay.ProcessOnce(context.Background())
	fmt.Println("pending after the retry:", store.get("customer-linked-cus-1").publishedAt.IsZero())

	// Output:
	// pending after the outage: true
	// published CustomerLinked customer-linked-cus-1
	// pending after the retry: false
}
