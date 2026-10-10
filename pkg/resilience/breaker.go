// Package resilience provides the fault-tolerance building blocks shared by
// the services. The circuit breaker wraps sony/gobreaker, the tactic already
// validated by experiment EC-D01.
package resilience

import (
	"errors"
	"log/slog"
	"time"

	"github.com/sony/gobreaker"
)

// ErrOpen is returned without calling the protected dependency when the
// circuit is open, or half-open and already probing.
var ErrOpen = errors.New("circuit breaker is open")

// Config holds the breaker parameters validated in EC-D01.
type Config struct {
	// ConsecutiveFailures is the number of consecutive failures that opens the circuit.
	ConsecutiveFailures uint32
	// OpenTimeout is how long the circuit stays open before probing (half-open).
	OpenTimeout time.Duration
	// HalfOpenSuccesses is the number of consecutive successful probes that closes the circuit.
	HalfOpenSuccesses uint32
}

// DefaultConfig returns the combination validated by EC-D01: 3 consecutive
// failures open the circuit, 5 s open window, 3 consecutive successes close it.
func DefaultConfig() Config {
	return Config{ConsecutiveFailures: 3, OpenTimeout: 5 * time.Second, HalfOpenSuccesses: 3}
}

// Breaker is a circuit breaker for one external dependency.
type Breaker struct {
	cb *gobreaker.CircuitBreaker
}

// NewBreaker builds a breaker and logs every state change so it can be
// correlated with load-test metrics.
func NewBreaker(name string, cfg Config) *Breaker {
	return &Breaker{cb: gobreaker.NewCircuitBreaker(gobreaker.Settings{
		Name:        name,
		MaxRequests: cfg.HalfOpenSuccesses,
		Timeout:     cfg.OpenTimeout,
		ReadyToTrip: func(c gobreaker.Counts) bool {
			return c.ConsecutiveFailures >= cfg.ConsecutiveFailures
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			slog.Info("circuit breaker state change", "breaker", name, "from", from.String(), "to", to.String())
		},
	})}
}

// State returns the current state: "closed", "open" or "half-open".
func (b *Breaker) State() string {
	return b.cb.State().String()
}

// Execute runs fn through the breaker. When the circuit rejects the call it
// returns ErrOpen and fn is not invoked.
func Execute[T any](b *Breaker, fn func() (T, error)) (T, error) {
	res, err := b.cb.Execute(func() (any, error) { return fn() })
	if err != nil {
		if errors.Is(err, gobreaker.ErrOpenState) || errors.Is(err, gobreaker.ErrTooManyRequests) {
			var zero T
			return zero, ErrOpen
		}
		var zero T
		return zero, err
	}
	return res.(T), nil
}
