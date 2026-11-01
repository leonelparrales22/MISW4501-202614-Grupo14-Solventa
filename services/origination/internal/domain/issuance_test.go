package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

func TestIssuanceStatusOfACharge(t *testing.T) {
	for charge, want := range map[domain.ChargeStatus]domain.IssuanceStatus{
		domain.ChargePending:    domain.IssuanceCharging,
		domain.ChargeInProgress: domain.IssuanceCharging,
		domain.ChargeConfirmed:  domain.IssuanceActive,
		domain.ChargeFailed:     domain.IssuanceException,
	} {
		assert.Equal(t, want, domain.IssuanceStatusOf(charge), charge)
	}
}

func TestOnlyAnExceptionWaitingLongEnoughIsUnattended(t *testing.T) {
	failedAt := time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)
	exception := domain.Issuance{Status: domain.IssuanceException, FailedAt: failedAt}

	assert.False(t, exception.IsUnattended(failedAt.Add(domain.EscalationAfter-time.Second)))
	assert.True(t, exception.IsUnattended(failedAt.Add(domain.EscalationAfter)))
	assert.False(t, domain.Issuance{Status: domain.IssuanceActive, FailedAt: failedAt}.IsUnattended(failedAt.Add(time.Hour)),
		"an issuance without an exception needs no action")
	assert.False(t, domain.Issuance{Status: domain.IssuanceException}.IsUnattended(failedAt.Add(time.Hour)),
		"no failure date, nothing to count from")
}

func TestIssuanceFilterValidate(t *testing.T) {
	t0 := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	assert.NoError(t, domain.IssuanceFilter{}.Validate())
	assert.NoError(t, domain.IssuanceFilter{Status: domain.IssuanceActive, From: t0, To: t0}.Validate())
	assert.NoError(t, domain.IssuanceFilter{Status: domain.IssuanceNotified, Channel: domain.ChannelMobile}.Validate())
	assert.Error(t, domain.IssuanceFilter{Status: "other"}.Validate())
	assert.Error(t, domain.IssuanceFilter{Channel: "fax"}.Validate())
	assert.Error(t, domain.IssuanceFilter{From: t0.Add(time.Hour), To: t0}.Validate())
}
