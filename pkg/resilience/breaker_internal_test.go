package resilience

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The failures in a row only matter while the circuit is closed, and the way
// back to closed always goes through successful probes, so the reset on a
// state change is checked on the counter itself.
func TestStateChangeStartsTheFailuresInARowOver(t *testing.T) {
	b := NewBreaker("internal", Config{
		OpenTimeout: time.Minute, HalfOpenSuccesses: 3,
		FailureRatio: 0.8, MinRequests: 20, Window: time.Minute, ConsecutiveFailureLimit: 30,
	})
	boom := errors.New("boom")
	for i := 0; i < 19; i++ {
		_, _ = Execute(b, func() (int, error) { return 0, boom })
	}
	require.Equal(t, uint32(19), b.failuresInARow.Load())

	_, _ = Execute(b, func() (int, error) { return 0, boom })
	require.Equal(t, "open", b.State(), "20 failed calls open by ratio")
	assert.Zero(t, b.failuresInARow.Load(), "the count starts over when the state changes")
}
