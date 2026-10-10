package resilience_test

import (
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
