package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

var paidAt = time.Date(2026, 9, 11, 21, 40, 0, 0, time.UTC)

func payout() domain.ParametricPayout {
	return domain.ParametricPayout{
		PayoutID: "ppo-1", ClaimID: "SIN-2026-01702", Subject: "sub-a", AmountMinor: 18_000_000, Currency: "COP",
		Trigger: domain.Trigger{Parameter: "flight_delay", EventReference: "AV-8421", Threshold: 180, Observed: 215},
		PaidAt:  paidAt,
	}
}

func TestAValidPayoutBecomesItsNotification(t *testing.T) {
	p := payout()
	require.NoError(t, p.Validate())

	n := domain.NewPayoutNotification(p, paidAt.Add(3*time.Second))

	assert.Equal(t, "ntf-ppo-1", n.ID)
	assert.Equal(t, "sub-a", n.Subject)
	assert.Equal(t, domain.KindParametricPayout, n.Kind)
	assert.Equal(t, paidAt, n.OccurredAt)
	assert.Equal(t, paidAt.Add(3*time.Second), n.CreatedAt)
	assert.Nil(t, n.ReadAt)
	p.AmountMinor = 1
	assert.Equal(t, int64(18_000_000), n.Payout.AmountMinor, "the notification keeps its own copy")
}

func TestAnIncompletePayoutIsRefused(t *testing.T) {
	for name, change := range map[string]func(*domain.ParametricPayout){
		"no payout id":       func(p *domain.ParametricPayout) { p.PayoutID = "" },
		"no subject":         func(p *domain.ParametricPayout) { p.Subject = "" },
		"long id":            func(p *domain.ParametricPayout) { p.PayoutID = strings.Repeat("x", 129) },
		"padded id":          func(p *domain.ParametricPayout) { p.Subject = " sub-a" },
		"no amount":          func(p *domain.ParametricPayout) { p.AmountMinor = 0 },
		"bad currency":       func(p *domain.ParametricPayout) { p.Currency = "PESOS" },
		"no parameter":       func(p *domain.ParametricPayout) { p.Trigger.Parameter = "" },
		"no event":           func(p *domain.ParametricPayout) { p.Trigger.EventReference = "" },
		"no payment instant": func(p *domain.ParametricPayout) { p.PaidAt = time.Time{} },
	} {
		t.Run(name, func(t *testing.T) {
			p := payout()
			change(&p)
			require.ErrorIs(t, p.Validate(), domain.ErrInvalidPayout)
		})
	}
}

func device() domain.Device {
	return domain.Device{InstallationID: "inst-1", Subject: "sub-a", Platform: "android", Token: "tok", PushEnabled: true,
		Locale: domain.LocaleSpanish}
}

func TestADeviceIsReachableOnlyWithThePermissionAndAToken(t *testing.T) {
	d := device()
	require.NoError(t, d.Validate())
	assert.True(t, d.Reachable())

	d.PushEnabled = false
	assert.False(t, d.Reachable())
	d.PushEnabled, d.Token = true, ""
	assert.False(t, d.Reachable())
}

func TestAnInvalidDeviceIsRefused(t *testing.T) {
	for name, change := range map[string]func(*domain.Device){
		"no installation": func(d *domain.Device) { d.InstallationID = "" },
		"other platform":  func(d *domain.Device) { d.Platform = "ios" },
		"other locale":    func(d *domain.Device) { d.Locale = "fr" },
		"long token":      func(d *domain.Device) { d.Token = strings.Repeat("t", 4097) },
		"push no token":   func(d *domain.Device) { d.Token = "" },
	} {
		t.Run(name, func(t *testing.T) {
			d := device()
			change(&d)
			require.ErrorIs(t, d.Validate(), domain.ErrInvalidDevice)
		})
	}
}

func TestADeliveryIsDueNowAndRetriedForADay(t *testing.T) {
	n := domain.NewPayoutNotification(payout(), paidAt)
	now := paidAt.Add(time.Second)

	d := domain.NewDelivery(n, device(), now)

	assert.Equal(t, "ntf-ppo-1:inst-1", d.ID)
	assert.Equal(t, d.ID, d.Key, "the delivery id is the idempotency key")
	assert.Equal(t, idempotency.StatusPending, d.Status)
	assert.Equal(t, now, d.NextAttemptAt)
	assert.Equal(t, now.Add(24*time.Hour), d.WindowEndsAt)
	assert.Equal(t, "tok", d.Token)
	assert.Equal(t, d.Attempt, d.AttemptState())
}
