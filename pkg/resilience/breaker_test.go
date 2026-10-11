package resilience_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

var errBoom = errors.New("boom")

func newTestBreaker() *resilience.Breaker {
	return resilience.NewBreaker("test", resilience.Config{
		ConsecutiveFailures: 3,
		OpenTimeout:         50 * time.Millisecond,
		HalfOpenSuccesses:   3,
	})
}

func fail(b *resilience.Breaker) error {
	_, err := resilience.Execute(b, func() (int, error) { return 0, errBoom })
	return err
}

func succeed(b *resilience.Breaker) error {
	_, err := resilience.Execute(b, func() (int, error) { return 1, nil })
	return err
}

func TestDefaultConfigMatchesValidatedExperiment(t *testing.T) {
	cfg := resilience.DefaultConfig()
	assert.Equal(t, uint32(3), cfg.ConsecutiveFailures)
	assert.Equal(t, 5*time.Second, cfg.OpenTimeout)
	assert.Equal(t, uint32(3), cfg.HalfOpenSuccesses)
}

func TestExecuteReturnsResultWhileClosed(t *testing.T) {
	b := newTestBreaker()
	got, err := resilience.Execute(b, func() (string, error) { return "ok", nil })
	require.NoError(t, err)
	assert.Equal(t, "ok", got)
	assert.Equal(t, "closed", b.State())
}

func TestOpensAfterConsecutiveFailuresAndSkipsTheDependency(t *testing.T) {
	b := newTestBreaker()
	for i := 0; i < 3; i++ {
		require.ErrorIs(t, fail(b), errBoom)
	}
	assert.Equal(t, "open", b.State())

	called := false
	_, err := resilience.Execute(b, func() (int, error) { called = true; return 1, nil })
	assert.ErrorIs(t, err, resilience.ErrOpen)
	assert.False(t, called, "an open circuit must not call the dependency")
}

func TestNonConsecutiveFailuresDoNotOpen(t *testing.T) {
	b := newTestBreaker()
	require.Error(t, fail(b))
	require.Error(t, fail(b))
	require.NoError(t, succeed(b))
	require.Error(t, fail(b))
	assert.Equal(t, "closed", b.State())
}

func TestClosesOnlyAfterThreeConsecutiveHalfOpenSuccesses(t *testing.T) {
	b := newTestBreaker()
	for i := 0; i < 3; i++ {
		_ = fail(b)
	}
	time.Sleep(70 * time.Millisecond)
	assert.Equal(t, "half-open", b.State())

	require.NoError(t, succeed(b))
	require.NoError(t, succeed(b))
	assert.Equal(t, "half-open", b.State(), "two successes must not close the circuit")
	require.NoError(t, succeed(b))
	assert.Equal(t, "closed", b.State())
}

func TestHalfOpenFailureReopens(t *testing.T) {
	b := newTestBreaker()
	for i := 0; i < 3; i++ {
		_ = fail(b)
	}
	time.Sleep(70 * time.Millisecond)
	require.NoError(t, succeed(b))
	require.Error(t, fail(b))
	assert.Equal(t, "open", b.State())
}

// --- Failure-ratio mode (HA-L01, decision D-3 c) --------------------------------

// newRatioBreaker opens at 50 % of failed calls with at least 20 calls in the
// window (decision D-3 c; origination now defaults to 80 %, see the tests of
// the safety net below), without the consecutive-failure limit, and uses the
// given windows.
func newRatioBreaker(window, openTimeout time.Duration) *resilience.Breaker {
	return resilience.NewBreaker("ratio", resilience.Config{
		ConsecutiveFailures: 3, // not used in this mode
		OpenTimeout:         openTimeout,
		HalfOpenSuccesses:   3,
		FailureRatio:        0.5,
		MinRequests:         20,
		Window:              window,
	})
}

// budgetCut runs a call that the caller abandons because its latency budget
// expired, as the parallel profiling does with a slow source (120 ms).
func budgetCut(b *resilience.Breaker) error {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Millisecond))
	defer cancel()
	_, err := resilience.Execute(b, func() (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	})
	return err
}

func TestDefaultConfigKeepsTheConsecutiveFailuresMode(t *testing.T) {
	cfg := resilience.DefaultConfig()
	assert.Zero(t, cfg.FailureRatio)
	assert.Zero(t, cfg.MinRequests)
	assert.Zero(t, cfg.Window)
	assert.Equal(t, 5*time.Second, resilience.DefaultWindow)
}

func TestBudgetCutCountsAsAFailure(t *testing.T) {
	b := resilience.NewBreaker("budget", resilience.Config{ConsecutiveFailures: 3, OpenTimeout: time.Minute, HalfOpenSuccesses: 3})
	for i := 0; i < 3; i++ {
		require.ErrorIs(t, budgetCut(b), context.DeadlineExceeded, "the caller still gets the original error")
	}
	assert.Equal(t, "open", b.State(), "a call cut by the caller's budget is a failure of the dependency")
}

func TestRatioModeDoesNotOpenWithTheNormalLatencyTail(t *testing.T) {
	b := newRatioBreaker(time.Minute, time.Minute)
	// About 17 % of the calls miss the budget (EXP-L01 normal profile), even in
	// bursts of three that would open the consecutive-failures mode.
	for round := 0; round < 4; round++ {
		for i := 0; i < 3; i++ {
			require.ErrorIs(t, budgetCut(b), context.DeadlineExceeded)
		}
		for i := 0; i < 15; i++ {
			require.NoError(t, succeed(b))
		}
	}
	assert.Equal(t, "closed", b.State(), "12 failures out of 72 calls must not isolate the source")
}

func TestRatioModeOpensWhenHalfTheCallsFailWithEnoughRequests(t *testing.T) {
	b := newRatioBreaker(time.Minute, time.Minute)
	for i := 0; i < 10; i++ {
		require.NoError(t, succeed(b))
	}
	for i := 0; i < 9; i++ {
		require.ErrorIs(t, fail(b), errBoom)
	}
	assert.Equal(t, "closed", b.State(), "9 failures out of 19 calls are below 50 %")

	require.ErrorIs(t, budgetCut(b), context.DeadlineExceeded)
	assert.Equal(t, "open", b.State(), "10 failures out of 20 calls reach 50 %")

	called := false
	_, err := resilience.Execute(b, func() (int, error) { called = true; return 1, nil })
	assert.ErrorIs(t, err, resilience.ErrOpen)
	assert.False(t, called, "an open circuit must not call the dependency")
}

func TestRatioModeNeedsTheMinimumNumberOfRequests(t *testing.T) {
	b := newRatioBreaker(time.Minute, time.Minute)
	for i := 0; i < 19; i++ {
		require.ErrorIs(t, budgetCut(b), context.DeadlineExceeded)
	}
	assert.Equal(t, "closed", b.State(), "19 calls are not enough, even if all of them failed")

	require.ErrorIs(t, budgetCut(b), context.DeadlineExceeded)
	assert.Equal(t, "open", b.State(), "a persistently slow dependency is isolated (HA-D01 CA-1)")
}

func TestRatioModeCountsWithinOneWindow(t *testing.T) {
	window := 50 * time.Millisecond
	b := newRatioBreaker(window, time.Minute)
	for i := 0; i < 15; i++ {
		require.Error(t, fail(b))
	}
	time.Sleep(3 * window)
	for i := 0; i < 15; i++ {
		require.Error(t, fail(b))
	}
	assert.Equal(t, "closed", b.State(), "the counts start over in every window")

	for i := 0; i < 5; i++ {
		require.Error(t, fail(b))
	}
	assert.Equal(t, "open", b.State(), "20 failed calls in the same window")
}

func TestRatioModeZeroWindowUsesTheDefault(t *testing.T) {
	b := resilience.NewBreaker("default-window", resilience.Config{
		OpenTimeout: time.Minute, HalfOpenSuccesses: 3, FailureRatio: 0.5, MinRequests: 20,
	})
	for i := 0; i < 20; i++ {
		require.Error(t, fail(b))
	}
	assert.Equal(t, "open", b.State())
}

// openRatioBreaker opens a ratio breaker with a short open window and waits
// until it lets the probes through (half-open).
func openRatioBreaker(t *testing.T) *resilience.Breaker {
	t.Helper()
	b := newRatioBreaker(time.Minute, 50*time.Millisecond)
	for i := 0; i < 20; i++ {
		_ = budgetCut(b)
	}
	require.Equal(t, "open", b.State())
	time.Sleep(80 * time.Millisecond)
	require.Equal(t, "half-open", b.State())
	return b
}

func TestRatioModeHalfOpenBudgetCutReopens(t *testing.T) {
	b := openRatioBreaker(t)
	require.ErrorIs(t, budgetCut(b), context.DeadlineExceeded)
	assert.Equal(t, "open", b.State(), "a probe cut by the budget is a failed probe")
}

func TestRatioModeHalfOpenFailureAfterSuccessesReopens(t *testing.T) {
	b := openRatioBreaker(t)
	require.NoError(t, succeed(b))
	require.NoError(t, succeed(b))
	require.ErrorIs(t, budgetCut(b), context.DeadlineExceeded)
	assert.Equal(t, "open", b.State(), "one failed probe reopens, as in EC-D01")
}

func TestRatioModeClosesAfterThreeConsecutiveHalfOpenSuccesses(t *testing.T) {
	b := openRatioBreaker(t)
	require.NoError(t, succeed(b))
	require.NoError(t, succeed(b))
	assert.Equal(t, "half-open", b.State(), "two successes must not close the circuit")
	require.NoError(t, succeed(b))
	assert.Equal(t, "closed", b.State())
}

// --- Consecutive-failure limit of the failure-ratio mode (HA-L01) -------------

// newSafetyNetBreaker uses the thresholds origination runs with: 80 % of
// failed calls with at least minRequests calls in the window, or 30 failed
// calls in a row.
func newSafetyNetBreaker(window time.Duration, minRequests uint32) *resilience.Breaker {
	return resilience.NewBreaker("safety-net", resilience.Config{
		OpenTimeout:             time.Minute,
		HalfOpenSuccesses:       3,
		FailureRatio:            0.8,
		MinRequests:             minRequests,
		Window:                  window,
		ConsecutiveFailureLimit: 30,
	})
}

// lowTrafficCuts runs n budget cuts in bursts of at most 10 calls, one burst
// per window, so no window ever reaches the 20 calls the ratio needs.
func lowTrafficCuts(t *testing.T, b *resilience.Breaker, n int, window time.Duration) {
	t.Helper()
	for n > 0 {
		burst := min(n, 10)
		for i := 0; i < burst; i++ {
			require.ErrorIs(t, budgetCut(b), context.DeadlineExceeded)
		}
		n -= burst
		time.Sleep(2 * window)
	}
}

func TestLowTrafficWithoutTheLimitNeverOpens(t *testing.T) {
	window := 30 * time.Millisecond
	b := resilience.NewBreaker("no-limit", resilience.Config{
		OpenTimeout: time.Minute, HalfOpenSuccesses: 3, FailureRatio: 0.8, MinRequests: 20, Window: window,
	})
	lowTrafficCuts(t, b, 60, window)
	assert.Equal(t, "closed", b.State(), "with fewer than 20 calls per window the ratio alone never isolates a hung dependency")
}

func TestConsecutiveFailureLimitOpensWithLowTraffic(t *testing.T) {
	window := 30 * time.Millisecond
	b := newSafetyNetBreaker(window, 20)

	lowTrafficCuts(t, b, 29, window)
	assert.Equal(t, "closed", b.State(), "29 failures in a row do not open the circuit")

	require.ErrorIs(t, budgetCut(b), context.DeadlineExceeded)
	assert.Equal(t, "open", b.State(), "the 30th failure in a row isolates the dependency, whatever the window")

	called := false
	_, err := resilience.Execute(b, func() (int, error) { called = true; return 1, nil })
	assert.ErrorIs(t, err, resilience.ErrOpen)
	assert.False(t, called, "an open circuit must not call the dependency")
}

func TestASuccessStartsTheFailuresInARowOver(t *testing.T) {
	b := newSafetyNetBreaker(time.Minute, 1000) // the ratio never applies here
	for i := 0; i < 29; i++ {
		require.Error(t, fail(b))
	}
	require.NoError(t, succeed(b))
	for i := 0; i < 29; i++ {
		require.Error(t, fail(b))
	}
	assert.Equal(t, "closed", b.State(), "a success starts the count over")

	require.Error(t, fail(b))
	assert.Equal(t, "open", b.State())
}

func TestRatio80DoesNotOpenWithTheDegradedProfile(t *testing.T) {
	b := newSafetyNetBreaker(time.Minute, 20)
	// 11 of every 18 calls fail (61 %, the degraded profile of EXP-L01), never
	// more than two in a row: neither the ratio nor the limit is reached.
	pattern := "FFSFFSFSFFSFSFFSFS"
	for round := 0; round < 5; round++ {
		for _, call := range pattern {
			if call == 'F' {
				require.ErrorIs(t, budgetCut(b), context.DeadlineExceeded)
			} else {
				require.NoError(t, succeed(b))
			}
		}
	}
	assert.Equal(t, "closed", b.State(), "55 failures out of 90 calls (61 %) are below 80 %")
}

func TestRatio80OpensWhenEveryCallFails(t *testing.T) {
	b := newSafetyNetBreaker(time.Minute, 20)
	for i := 0; i < 19; i++ {
		require.ErrorIs(t, budgetCut(b), context.DeadlineExceeded)
	}
	assert.Equal(t, "closed", b.State(), "19 calls are not enough for the ratio")

	require.ErrorIs(t, budgetCut(b), context.DeadlineExceeded)
	assert.Equal(t, "open", b.State(), "100 % of 20 calls: the ratio opens before the limit of 30")
}

func TestRatio80OpensAtEightyPercent(t *testing.T) {
	b := newSafetyNetBreaker(time.Minute, 20)
	for i := 0; i < 4; i++ {
		require.NoError(t, succeed(b))
	}
	for i := 0; i < 15; i++ {
		require.Error(t, fail(b))
	}
	assert.Equal(t, "closed", b.State(), "15 failures out of 19 calls: not enough calls yet")

	require.Error(t, fail(b))
	assert.Equal(t, "open", b.State(), "16 failures out of 20 calls reach 80 %")
}
