package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestConfigDefaultsLeaveEverythingOptional(t *testing.T) {
	cfg, err := loadConfig(env(nil))

	require.NoError(t, err)
	assert.Equal(t, "8080", cfg.port)
	assert.Empty(t, cfg.databaseURL)
	assert.False(t, cfg.auth.enabled)
	assert.Empty(t, cfg.pushProviderURL)
	assert.Equal(t, 2*time.Second, cfg.pushProviderTimeout)
	assert.Equal(t, time.Second, cfg.pushPollInterval)
	assert.Equal(t, time.Second, cfg.pushRetryBase)
	assert.Equal(t, time.Minute, cfg.pushRetryMax)
}

func TestConfigReadsItsVariables(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		"PORT": "9090", "DATABASE_URL": "postgres://x", "EVENTS_QUEUE_URL": "http://sqs/q", "PUSH_PROVIDER_URL": "http://push:9070",
		"PUSH_PROVIDER_TIMEOUT_MS": "500", "PUSH_POLL_INTERVAL_MS": "200", "PUSH_RETRY_BASE_MS": "100", "PUSH_RETRY_MAX_MS": "1000",
		"AUTH_ISSUER": "https://cognito-idp.us-east-2.amazonaws.com/pool", "AUTH_CLIENT_IDS": "client-1",
	}))

	require.NoError(t, err)
	assert.Equal(t, "9090", cfg.port)
	assert.True(t, cfg.auth.enabled)
	assert.Equal(t, "http://sqs/q", cfg.eventsQueueURL)
	assert.Equal(t, 500*time.Millisecond, cfg.pushProviderTimeout)
	assert.Equal(t, 200*time.Millisecond, cfg.pushPollInterval)
	assert.Equal(t, 100*time.Millisecond, cfg.pushRetryBase)
	assert.Equal(t, time.Second, cfg.pushRetryMax)
}

func TestConfigRefusesWhatCannotWork(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"bad timeout":       {"PUSH_PROVIDER_TIMEOUT_MS": "0"},
		"bad poll":          {"PUSH_POLL_INTERVAL_MS": "soon"},
		"max below base":    {"PUSH_RETRY_BASE_MS": "1000", "PUSH_RETRY_MAX_MS": "10"},
		"incomplete issuer": {"AUTH_ISSUER": "https://issuer"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadConfig(env(vars))
			require.Error(t, err)
		})
	}
}

func TestWithoutAProviderThereIsNoWorker(t *testing.T) {
	w, err := newPushWorker(config{}, nil)

	require.NoError(t, err)
	assert.Nil(t, w)
}

func TestWithAProviderTheWorkerSendsToIt(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{"PUSH_PROVIDER_URL": "http://push:9070"}))
	require.NoError(t, err)

	w, err := newPushWorker(cfg, postgres.NewStore(nil))

	require.NoError(t, err)
	assert.NotNil(t, w)
}

// A service account as Firebase gives it, with a key that is never used
// (HU-M3.3-01): the client only reads it to sign the token of a real send.
const serviceAccount = `{"type":"service_account","project_id":"solventa-94126","client_email":"fcm@solventa-94126.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY-----\n","token_uri":"https://oauth2.googleapis.com/token"}`

func TestWithFCMTheWorkerSendsToFirebaseBeforeTheSimulator(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{"FCM_PROJECT_ID": "solventa-94126", "FCM_CREDENTIALS": serviceAccount,
		"PUSH_PROVIDER_URL": "http://push:9070"}))
	require.NoError(t, err)

	sender, provider, err := pushSender(cfg)

	require.NoError(t, err)
	assert.NotNil(t, sender)
	assert.Equal(t, "Firebase Cloud Messaging (solventa-94126)", provider)
	w, err := newPushWorker(cfg, postgres.NewStore(nil))
	require.NoError(t, err)
	assert.NotNil(t, w)
}

func TestFCMNeedsItsProjectAndItsServiceAccount(t *testing.T) {
	_, err := loadConfig(env(map[string]string{"FCM_PROJECT_ID": "solventa-94126"}))
	assert.Error(t, err, "a project without credentials")
	_, err = loadConfig(env(map[string]string{"FCM_CREDENTIALS": serviceAccount}))
	assert.Error(t, err, "credentials without a project")

	cfg, err := loadConfig(env(map[string]string{"FCM_PROJECT_ID": "solventa-94126", "FCM_CREDENTIALS": `{"type":"user"}`}))
	require.NoError(t, err)
	_, err = newPushWorker(cfg, postgres.NewStore(nil))
	assert.Error(t, err, "credentials that are not a service account stop the service")
}

func TestWithoutAQueueThereIsNoConsumer(t *testing.T) {
	c, err := newEventConsumer(context.Background(), config{}, nil)

	require.NoError(t, err)
	assert.Nil(t, c)
}

type recorder struct {
	got      []domain.ParametricPayout
	stages   []domain.ClaimStageChange
	expiries []domain.ExpiryAlert
}

func (r *recorder) RecordPayout(_ context.Context, p domain.ParametricPayout) error {
	r.got = append(r.got, p)
	return nil
}

func (r *recorder) RecordStageChange(_ context.Context, c domain.ClaimStageChange) error {
	r.stages = append(r.stages, c)
	return nil
}

func (r *recorder) RecordExpiryAlert(_ context.Context, e domain.ExpiryAlert) error {
	r.expiries = append(r.expiries, e)
	return nil
}

func TestTheRouterHandlesTheAutomaticPayments(t *testing.T) {
	rec := &recorder{}
	router, err := eventRouter(rec)
	require.NoError(t, err)
	e, err := events.New("e-1", "ParametricPayoutCompleted", "claims", 1, time.Now(), map[string]any{
		"payout_id": "ppo-1", "subject": "sub-a", "amount_minor": 100, "currency": "COP",
		"trigger": map[string]any{"parameter": "flight_delay", "event_reference": "AV-8421"},
	})
	require.NoError(t, err)

	require.NoError(t, router.Handle(context.Background(), e))

	assert.Len(t, rec.got, 1)
}

func TestTheRouterHandlesTheChangesOfStage(t *testing.T) {
	rec := &recorder{}
	router, err := eventRouter(rec)
	require.NoError(t, err)
	e, err := events.New("claim-stage-changed-7", "ClaimStageChanged", "claims", 1, time.Now(), map[string]any{
		"change_id": 7, "claim_id": "cl-1", "filing_number": "SIN-2026-01655", "subject": "sub-a",
		"stage": "in_evaluation", "changed_at": "2026-09-05T15:02:00Z",
	})
	require.NoError(t, err)

	require.NoError(t, router.Handle(context.Background(), e))

	require.Len(t, rec.stages, 1)
	assert.Equal(t, "in_evaluation", rec.stages[0].Stage)
}

func TestTheRouterHandlesTheExpiryAlerts(t *testing.T) {
	rec := &recorder{}
	router, err := eventRouter(rec)
	require.NoError(t, err)
	e, err := events.New("policy-expiry-approaching-AU-2025-118204-2026-11-30-30", "PolicyExpiryApproaching",
		"origination", 1, time.Now(), map[string]any{
			"policy_id": "AU-2025-118204", "subject": "sub-a", "source": "policy_core", "line": "auto",
			"product": "Autos", "insured_object": "KJT-482", "coverage_end": "2026-11-30T00:00:00Z",
			"milestone_days": 30, "days_left": 15, "detected_at": "2026-11-14T15:00:00Z",
		})
	require.NoError(t, err)

	require.NoError(t, router.Handle(context.Background(), e))

	require.Len(t, rec.expiries, 1)
	assert.Equal(t, 30, rec.expiries[0].MilestoneDays)
}

func TestWithoutTheIssuerTheCenterIsNotMounted(t *testing.T) {
	assert.Empty(t, centerOptions(context.Background(), config{}, nil))
}
