// Package resilience provides the fault-tolerance building blocks shared by
// the services. The circuit breaker wraps sony/gobreaker, the tactic already
// validated by experiment EC-D01.
package resilience

import (
	"context"
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
	// IgnoreCallerCancellation keeps a call that failed because the caller's
	// context was canceled or ran out of time (for example, the per-dependency
	// latency budget of the parallel profiling, HA-L01) from counting as a
	// failure: the caller gave up, the dependency did not fail. Errors of the
	// dependency itself (5xx, refused connections, its own timeout) still
	// count. Only ExecuteContext knows the caller's context; Execute keeps
	// counting every error. It is off in DefaultConfig.
	IgnoreCallerCancellation bool
}

// DefaultConfig returns the combination validated by EC-D01: 3 consecutive
// failures open the circuit, 5 s open window, 3 consecutive successes close it.
func DefaultConfig() Config {
	return Config{ConsecutiveFailures: 3, OpenTimeout: 5 * time.Second, HalfOpenSuccesses: 3}
}

// Breaker is a circuit breaker for one external dependency.
type Breaker struct {
	cb                       *gobreaker.CircuitBreaker
	ignoreCallerCancellation bool
}

// NewBreaker builds a breaker and logs every state change so it can be
// correlated with load-test metrics.
func NewBreaker(name string, cfg Config) *Breaker {
	settings := gobreaker.Settings{
		Name:        name,
		MaxRequests: cfg.HalfOpenSuccesses,
		Timeout:     cfg.OpenTimeout,
		ReadyToTrip: func(c gobreaker.Counts) bool {
			return c.ConsecutiveFailures >= cfg.ConsecutiveFailures
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			slog.Info("circuit breaker state change", "breaker", name, "from", from.String(), "to", to.String())
		},
	}
	if cfg.IgnoreCallerCancellation {
		// gobreaker v1 has no neutral outcome: a call the caller abandoned is
		// recorded as a non-failure (it resets the consecutive-failure count).
		settings.IsSuccessful = func(err error) bool {
			var canceled *callerCanceledError
			return err == nil || errors.As(err, &canceled)
		}
	}
	return &Breaker{
		cb:                       gobreaker.NewCircuitBreaker(settings),
		ignoreCallerCancellation: cfg.IgnoreCallerCancellation,
	}
}

// callerCanceledError marks a failure caused by the caller's context so the
// breaker does not count it. It never leaves this package.
type callerCanceledError struct {
	err error
}

func (e *callerCanceledError) Error() string { return e.err.Error() }
func (e *callerCanceledError) Unwrap() error { return e.err }

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

// ExecuteContext runs fn through the breaker like Execute, for a call bound to
// the caller's context ctx. When the breaker was built with
// IgnoreCallerCancellation and fn fails once ctx is already done, the failure
// is not counted against the dependency. The error returned is always the one
// fn returned (or ErrOpen).
func ExecuteContext[T any](ctx context.Context, b *Breaker, fn func() (T, error)) (T, error) {
	res, err := Execute(b, func() (T, error) {
		res, err := fn()
		if err != nil && b.ignoreCallerCancellation && ctx.Err() != nil {
			return res, &callerCanceledError{err: err}
		}
		return res, err
	})
	var canceled *callerCanceledError
	if errors.As(err, &canceled) {
		return res, canceled.err
	}
	return res, err
}
