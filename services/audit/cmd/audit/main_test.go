package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(env(nil))

	require.NoError(t, err)
	assert.Equal(t, "8080", cfg.port)
	assert.Empty(t, cfg.databaseURL, "without DATABASE_URL the consent routes are not mounted")
	assert.False(t, cfg.auth.enabled, "without AUTH_ISSUER the evidence lookup is not mounted")
}

func TestLoadConfigReadsThePortAndTheDatabase(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{"PORT": "9090", "DATABASE_URL": "postgres://h/db"}))

	require.NoError(t, err)
	assert.Equal(t, "9090", cfg.port)
	assert.Equal(t, "postgres://h/db", cfg.databaseURL)
}

func TestLoadConfigEnablesTheHumanRoutesWithAValidAuthConfig(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		"AUTH_ISSUER":     "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_abc",
		"AUTH_CLIENT_IDS": "web-client, mobile-client",
	}))

	require.NoError(t, err)
	assert.True(t, cfg.auth.enabled)
	assert.Equal(t, []string{"web-client", "mobile-client"}, cfg.auth.config.ClientIDs)
}

func TestLoadConfigRejectsAnIncompleteAuthConfig(t *testing.T) {
	_, err := loadConfig(env(map[string]string{"AUTH_ISSUER": "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_abc"}))

	assert.ErrorContains(t, err, "AUTH_ISSUER")
}

// HU-M1.3-02: the configuration of the revocation and its notice worker.

func TestLoadConfigRevocationDefaults(t *testing.T) {
	cfg, err := loadConfig(env(nil))

	require.NoError(t, err)
	assert.Empty(t, cfg.openFinanceURL, "without OPEN_FINANCE_URL no entity is notified")
	assert.Equal(t, 700*time.Millisecond, cfg.openFinanceTimeout)
	assert.Equal(t, 5*time.Minute, cfg.noticeDeadline, "RN-02")
	assert.Equal(t, time.Second, cfg.noticePollInterval)
}

func TestLoadConfigReadsTheRevocationVariables(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		"OPEN_FINANCE_URL": "http://openfinance.staging.local:9000", "OPEN_FINANCE_TIMEOUT_MS": "500",
		"REVOCATION_NOTICE_DEADLINE_SECONDS": "5", "REVOCATION_NOTICE_POLL_INTERVAL_MS": "250",
	}))

	require.NoError(t, err)
	assert.Equal(t, "http://openfinance.staging.local:9000", cfg.openFinanceURL)
	assert.Equal(t, 500*time.Millisecond, cfg.openFinanceTimeout)
	assert.Equal(t, 5*time.Second, cfg.noticeDeadline)
	assert.Equal(t, 250*time.Millisecond, cfg.noticePollInterval)
}

func TestLoadConfigRefusesInvalidRevocationVariables(t *testing.T) {
	for _, name := range []string{"OPEN_FINANCE_TIMEOUT_MS", "REVOCATION_NOTICE_DEADLINE_SECONDS", "REVOCATION_NOTICE_POLL_INTERVAL_MS"} {
		for _, raw := range []string{"0", "-1", "five"} {
			_, err := loadConfig(env(map[string]string{name: raw}))
			assert.ErrorContains(t, err, name, raw)
		}
	}
}

func TestWithoutOpenFinanceTheNoticeWorkerDoesNotStart(t *testing.T) {
	buf := captureLog(t)

	worker, err := newNoticeWorker(config{}, nil)

	require.NoError(t, err)
	assert.Nil(t, worker)
	assert.Contains(t, buf.String(), "OPEN_FINANCE_URL is not set: revocations are recorded and block the queries, but no entity is notified")
}

func TestWithOpenFinanceTheNoticeWorkerRunsAndStops(t *testing.T) {
	buf := captureLog(t)
	cfg, err := loadConfig(env(map[string]string{"OPEN_FINANCE_URL": "http://127.0.0.1:9", "REVOCATION_NOTICE_POLL_INTERVAL_MS": "10"}))
	require.NoError(t, err)

	store := &failingNotices{}
	worker, err := newNoticeWorker(cfg, store)
	require.NoError(t, err)
	require.NotNil(t, worker)
	assert.Contains(t, buf.String(), "revocation notices (HU-M1.3-02): to http://127.0.0.1:9 within 5m0s, polling every 10ms")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		worker.Run(ctx)
		close(done)
	}()
	require.Eventually(t, func() bool { return store.calls.Load() >= 2 }, 2*time.Second, 5*time.Millisecond,
		"a failing store is logged and the worker keeps polling")
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the worker did not stop with the context")
	}
}

// failingNotices is a notice store whose database is down.
type failingNotices struct{ calls atomic.Int32 }

var errDown = errors.New("database down")

func (f *failingNotices) Claim(context.Context, string, time.Time, time.Duration) (ports.NoticeRecord, bool, error) {
	return ports.NoticeRecord{}, false, errDown
}

func (f *failingNotices) Confirm(context.Context, ports.NoticeRecord, time.Time, idempotency.Outcome) (ports.NoticeRecord, error) {
	return ports.NoticeRecord{}, errDown
}

func (f *failingNotices) Reschedule(context.Context, ports.NoticeRecord, time.Time, time.Time, string) (ports.NoticeRecord, error) {
	return ports.NoticeRecord{}, errDown
}

func (f *failingNotices) Fail(context.Context, ports.NoticeRecord, time.Time, idempotency.FailureReason, string) (ports.NoticeRecord, error) {
	return ports.NoticeRecord{}, errDown
}

func (f *failingNotices) Due(context.Context, time.Time, int) ([]string, error) {
	f.calls.Add(1)
	return nil, errDown
}

func (f *failingNotices) Resume(context.Context, string, []string, time.Time, time.Duration, domain.Actor) ([]string, error) {
	return nil, errDown
}
