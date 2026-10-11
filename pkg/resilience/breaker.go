// Package resilience provides the fault-tolerance building blocks shared by
// the services. The circuit breaker wraps sony/gobreaker, the tactic already
// validated by experiment EC-D01.
package resilience

import (
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/sony/gobreaker"
)

// ErrOpen is returned without calling the protected dependency when the
// circuit is open, or half-open and already probing.
var ErrOpen = errors.New("circuit breaker is open")

// DefaultWindow is the counting window of the failure-ratio mode when
// Config.Window is not set.
const DefaultWindow = 5 * time.Second

// Config holds the breaker parameters validated in EC-D01.
type Config struct {
	// ConsecutiveFailures is the number of consecutive failures that opens the circuit.
	ConsecutiveFailures uint32
	// OpenTimeout is how long the circuit stays open before probing (half-open).
	OpenTimeout time.Duration
	// HalfOpenSuccesses is the number of consecutive successful probes that closes the circuit.
	HalfOpenSuccesses uint32

	// FailureRatio, when greater than zero, opens the circuit by the share of
	// failed calls instead of by consecutive failures (HA-L01, decision D-3 c):
	// it opens when, within one Window, at least MinRequests calls were made
	// and the failed ones reach FailureRatio of them. Every error counts as a
	// failure, including a call cut by the caller's latency budget, so a
	// dependency that is persistently slow or down is isolated while the
	// normal latency tail does not open the circuit. ConsecutiveFailures is
	// not used in this mode (see ConsecutiveFailureLimit). The half-open
	// behavior does not change: one failed probe reopens and
	// HalfOpenSuccesses consecutive successes close.
	// Zero (as in DefaultConfig) keeps the consecutive-failures mode.
	FailureRatio float64
	// MinRequests is the minimum number of calls within the window before the
	// failure ratio is evaluated. Failure-ratio mode only.
	MinRequests uint32
	// Window is the closed-state counting window of the failure-ratio mode:
	// the counts start over at the end of every window. Zero uses
	// DefaultWindow. Failure-ratio mode only.
	Window time.Duration
	// ConsecutiveFailureLimit, when greater than zero, also opens the circuit
	// in the failure-ratio mode after that many failed calls in a row, so a
	// dependency that hangs is isolated even when the traffic is too low to
	// reach MinRequests within one window (HA-L01). The Breaker keeps this
	// count itself: unlike the counts of the window, it does not start over
	// at the end of every window, only with a success or a state change.
	// Zero disables it. Failure-ratio mode only.
	ConsecutiveFailureLimit uint32
}

// DefaultConfig returns the combination validated by EC-D01: 3 consecutive
// failures open the circuit, 5 s open window, 3 consecutive successes close it.
func DefaultConfig() Config {
	return Config{ConsecutiveFailures: 3, OpenTimeout: 5 * time.Second, HalfOpenSuccesses: 3}
}

// Breaker is a circuit breaker for one external dependency.
type Breaker struct {
	cb *gobreaker.CircuitBreaker
	// failuresInARow counts the failed calls since the last success or state
	// change, across the windows of the failure-ratio mode (gobreaker clears
	// its own counts at the end of every window).
	failuresInARow atomic.Uint32
}

// NewBreaker builds a breaker and logs every state change so it can be
// correlated with load-test metrics.
func NewBreaker(name string, cfg Config) *Breaker {
	b := &Breaker{}
	settings := gobreaker.Settings{
		Name:        name,
		MaxRequests: cfg.HalfOpenSuccesses,
		Timeout:     cfg.OpenTimeout,
		ReadyToTrip: func(c gobreaker.Counts) bool {
			return c.ConsecutiveFailures >= cfg.ConsecutiveFailures
		},
		OnStateChange: func(name string, from, to gobreaker.State) {
			b.failuresInARow.Store(0)
			slog.Info("circuit breaker state change", "breaker", name, "from", from.String(), "to", to.String())
		},
	}
	if cfg.FailureRatio > 0 {
		// gobreaker clears the counts of the closed state every Interval, so
		// the ratio is computed over the calls of the current window.
		settings.Interval = cfg.Window
		if settings.Interval <= 0 {
			settings.Interval = DefaultWindow
		}
		settings.IsSuccessful = b.countFailuresInARow
		settings.ReadyToTrip = func(c gobreaker.Counts) bool {
			byRatio := c.Requests >= cfg.MinRequests &&
				float64(c.TotalFailures) >= cfg.FailureRatio*float64(c.Requests)
			inARow := cfg.ConsecutiveFailureLimit > 0 &&
				b.failuresInARow.Load() >= cfg.ConsecutiveFailureLimit
			return byRatio || inARow
		}
	}
	b.cb = gobreaker.NewCircuitBreaker(settings)
	return b
}

// countFailuresInARow is the IsSuccessful hook of the failure-ratio mode.
// gobreaker calls it once for every executed call, right before it records
// the result and evaluates ReadyToTrip, so the count already includes the
// call being evaluated. Every error is a failure, as in gobreaker's default.
func (b *Breaker) countFailuresInARow(err error) bool {
	if err != nil {
		b.failuresInARow.Add(1)
		return false
	}
	b.failuresInARow.Store(0)
	return true
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
