package events

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// ErrUnhandledType is returned by a Router that has no handler for the type
// of the event. A consumer discards such a message: a queue receives every
// event of the topics it subscribes to, including the ones it does not
// consume yet.
var ErrUnhandledType = errors.New("events: no handler for the event type")

// Publisher publishes an envelope to the broker. It is called by the outbox
// relay, never inside a business transaction.
type Publisher interface {
	Publish(ctx context.Context, e Envelope) error
}

// Handler handles one event. It must be idempotent: the same event can arrive
// more than once. An error leaves the message in the queue to be retried.
type Handler interface {
	Handle(ctx context.Context, e Envelope) error
}

// HandlerFunc adapts a function to a Handler.
type HandlerFunc func(ctx context.Context, e Envelope) error

// Handle calls f.
func (f HandlerFunc) Handle(ctx context.Context, e Envelope) error { return f(ctx, e) }

// Router dispatches each event to the handler registered for its type. It is
// itself a Handler, so a consumer takes one Router for all the types it
// consumes.
type Router struct {
	mu       sync.RWMutex
	handlers map[string]Handler
}

// NewRouter builds an empty Router.
func NewRouter() *Router { return &Router{handlers: map[string]Handler{}} }

// Register sets the handler of an event type. A type can have only one
// handler.
func (r *Router) Register(eventType string, h Handler) error {
	if eventType == "" {
		return errors.New("events: the event type of a handler is required")
	}
	if h == nil {
		return fmt.Errorf("events: the handler of %s is nil", eventType)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, taken := r.handlers[eventType]; taken {
		return fmt.Errorf("events: %s already has a handler", eventType)
	}
	r.handlers[eventType] = h
	return nil
}

// Handle calls the handler of the event type, or returns ErrUnhandledType.
func (r *Router) Handle(ctx context.Context, e Envelope) error {
	r.mu.RLock()
	h, ok := r.handlers[e.EventType]
	r.mu.RUnlock()
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnhandledType, e.EventType)
	}
	return h.Handle(ctx, e)
}
