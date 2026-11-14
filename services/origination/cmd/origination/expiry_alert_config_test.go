package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// HU-M3.2-01: the configuration and the wiring of the expiry alerts.

func TestExpiryAlertsSweepEveryHourByDefault(t *testing.T) {
	var cfg expiryAlertConfig

	require.NoError(t, loadExpiryAlertConfig(envOf(nil), &cfg))

	assert.Equal(t, time.Hour, cfg.interval)
}

func TestExpiryAlertsReadTheirInterval(t *testing.T) {
	var cfg expiryAlertConfig

	require.NoError(t, loadExpiryAlertConfig(envOf(map[string]string{"EXPIRY_ALERT_INTERVAL_MS": "5000"}), &cfg))

	assert.Equal(t, 5*time.Second, cfg.interval)
	for _, bad := range []string{"x", "0", "-1"} {
		assert.Error(t, loadExpiryAlertConfig(envOf(map[string]string{"EXPIRY_ALERT_INTERVAL_MS": bad}), &cfg), bad)
	}
	_, err := loadConfig(envOf(map[string]string{"EXPIRY_ALERT_INTERVAL_MS": "x"}))
	assert.Error(t, err, "an invalid interval stops the service")
}

type stubPortfolio struct{}

func (stubPortfolio) HolderPolicies(context.Context, string) ([]domain.WalletPolicy, error) {
	return nil, nil
}
func (stubPortfolio) Holder(context.Context, string) (domain.PolicyHolder, error) {
	return domain.PolicyHolder{}, nil
}

func memoryStores() stores {
	customers := memory.NewCustomerStore()
	return stores{customers: customers, outbox: customers.Outbox(), linked: customers,
		expiryAlerts: memory.NewExpiryAlertStore(customers.Outbox())}
}

func TestWithoutASourceOfPoliciesNothingIsAlerted(t *testing.T) {
	sweeper, err := newExpiryAlertSweeper(memoryStores(), nil)

	require.NoError(t, err)
	assert.Nil(t, sweeper)
	assert.NoError(t, startExpiryAlerts(context.Background(), expiryAlertConfig{interval: time.Hour}, memoryStores(), nil))
}

func TestInMemoryThePolicyCoreIsAlerted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sweeper, err := newExpiryAlertSweeper(memoryStores(), stubPortfolio{})
	require.NoError(t, err)
	assert.NotNil(t, sweeper)
	assert.NoError(t, startExpiryAlerts(ctx, expiryAlertConfig{interval: time.Hour}, memoryStores(), stubPortfolio{}))
}
