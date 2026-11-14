package push_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

// HU-M3.2-01: an expiry alert is sent like any other notification.
func TestAnExpiryAlertIsSentOnce(t *testing.T) {
	c := &clock{now: paidAt.Add(time.Second)}
	e := domain.ExpiryAlert{PolicyID: "AU-2025-118204", Subject: "sub-a", Source: domain.ExpirySourcePolicyCore,
		Line: "auto", CoverageEnd: paidAt.AddDate(0, 0, 16), MilestoneDays: 30, DaysLeft: 16, DetectedAt: paidAt}
	d := domain.NewDelivery(domain.NewExpiryNotification(e, c.now),
		domain.Device{InstallationID: "inst-1", Token: "tok-inst-1", Locale: "es"}, c.now)
	store, sender := newMemStore(d), &fakeSender{}

	n, err := newWorker(t, store, sender, c).ProcessOnce(context.Background())

	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, idempotency.StatusConfirmed, store.get(d.ID).Status)
}
