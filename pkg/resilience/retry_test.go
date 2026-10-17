package resilience_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
)

func TestDefaultRetryPolicyMatchesTheValidatedExperiment(t *testing.T) {
	p := resilience.DefaultRetryPolicy()
	assert.Equal(t, 500*time.Millisecond, p.Base)
	assert.Equal(t, time.Minute, p.Max)
	assert.Equal(t, 24*time.Hour, p.Window)
	assert.NoError(t, p.Validate())
}

func TestRetryPolicyValidate(t *testing.T) {
	for name, p := range map[string]resilience.RetryPolicy{
		"zero base":       {Base: 0, Max: time.Second},
		"negative base":   {Base: -time.Second, Max: time.Second},
		"max below base":  {Base: time.Second, Max: time.Millisecond},
		"negative window": {Base: time.Second, Max: time.Second, Window: -time.Hour},
	} {
		t.Run(name, func(t *testing.T) { assert.Error(t, p.Validate()) })
	}
	assert.NoError(t, resilience.RetryPolicy{Base: time.Second, Max: time.Second}.Validate(), "max equal to base and no window")
}

func TestBackoffDoublesFromTheBaseUpToTheCap(t *testing.T) {
	p := resilience.RetryPolicy{Base: 500 * time.Millisecond, Max: time.Minute}
	for attempts, want := range map[int]time.Duration{
		-1:  500 * time.Millisecond, // below 1 behaves like the first failure
		0:   500 * time.Millisecond,
		1:   500 * time.Millisecond,
		2:   time.Second,
		3:   2 * time.Second,
		7:   32 * time.Second,
		8:   time.Minute, // 64 s is capped
		9:   time.Minute,
		500: time.Minute,
	} {
		assert.Equal(t, want, p.Backoff(attempts), "after %d failed attempts", attempts)
	}
}

func TestBackoffNeverOverflowsWithAHugeCap(t *testing.T) {
	p := resilience.RetryPolicy{Base: time.Hour, Max: time.Duration(1<<62 + 1)}
	got := p.Backoff(1_000)
	assert.Positive(t, got)
	assert.LessOrEqual(t, got, p.Max)
}

func TestWindowEnd(t *testing.T) {
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	assert.Equal(t, start.Add(24*time.Hour), resilience.DefaultRetryPolicy().WindowEnd(start))
	assert.True(t, resilience.RetryPolicy{Base: time.Second, Max: time.Second}.WindowEnd(start).IsZero(), "no window")
}

func TestNextSchedulesTheGrowingWaitInsideTheWindow(t *testing.T) {
	p := resilience.DefaultRetryPolicy()
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	end := p.WindowEnd(start)

	next, exhausted := p.Next(1, end, start)
	assert.False(t, exhausted)
	assert.Equal(t, start.Add(500*time.Millisecond), next, "first failure")

	now := start.Add(time.Hour)
	next, exhausted = p.Next(3, end, now)
	assert.False(t, exhausted)
	assert.Equal(t, now.Add(2*time.Second), next)
}

func TestNextMovesTheLastAttemptToTheEndOfTheWindow(t *testing.T) {
	p := resilience.DefaultRetryPolicy()
	end := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	now := end.Add(-10 * time.Second) // the capped wait (60 s) would pass the end

	next, exhausted := p.Next(20, end, now)

	assert.False(t, exhausted)
	assert.Equal(t, end, next, "the window is used up to its last instant")
}

func TestNextIsExhaustedOnceTheWindowIsOver(t *testing.T) {
	p := resilience.DefaultRetryPolicy()
	end := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

	for _, now := range []time.Time{end, end.Add(time.Nanosecond), end.Add(time.Hour)} {
		next, exhausted := p.Next(5, end, now)
		assert.True(t, exhausted, "at %s", now)
		assert.True(t, next.IsZero())
	}
}

func TestNextWithoutWindowNeverExhausts(t *testing.T) {
	p := resilience.RetryPolicy{Base: 500 * time.Millisecond, Max: time.Minute}
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

	next, exhausted := p.Next(10_000, time.Time{}, now)

	assert.False(t, exhausted)
	assert.Equal(t, now.Add(time.Minute), next)
}
