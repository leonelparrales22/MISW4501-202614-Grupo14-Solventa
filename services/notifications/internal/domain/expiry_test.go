package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

// HU-M3.2-01: the notification that a policy is about to expire.

var detectedAt = time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC)

func expiryAlert() domain.ExpiryAlert {
	return domain.ExpiryAlert{PolicyID: "AU-2025-118204", Subject: "sub-a", Source: domain.ExpirySourcePolicyCore,
		Line: "auto", Product: "Autos", InsuredObject: "KJT-482",
		CoverageEnd: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC), MilestoneDays: 30, DaysLeft: 15, DetectedAt: detectedAt}
}

func TestAnExpiryAlertIsOneNotificationPerPolicyPeriodAndMilestone(t *testing.T) {
	e := expiryAlert()
	n := domain.NewExpiryNotification(e, detectedAt.Add(3*time.Second))

	assert.Equal(t, "ntf-expiry-AU-2025-118204-20261129-30", n.ID, "the date is the one in Colombia")
	assert.Equal(t, domain.KindPolicyExpiry, n.Kind)
	assert.Equal(t, "sub-a", n.Subject)
	assert.Equal(t, detectedAt, n.OccurredAt)
	require.NotNil(t, n.Expiry)
	assert.Equal(t, e, *n.Expiry)
	assert.Nil(t, n.Payout)
	assert.Nil(t, n.Stage)
}

func TestAnExpiryAlertNeedsWhatTheNotificationShows(t *testing.T) {
	require.NoError(t, expiryAlert().Validate())
	life := expiryAlert()
	life.Source, life.Line, life.Product, life.InsuredObject = domain.ExpirySourceOrigination, "life", "", ""
	require.NoError(t, life.Validate(), "origination names no product")
	for name, change := range map[string]func(*domain.ExpiryAlert){
		"no policy":         func(e *domain.ExpiryAlert) { e.PolicyID = "" },
		"a long policy":     func(e *domain.ExpiryAlert) { e.PolicyID = strings.Repeat("p", 110) },
		"no subject":        func(e *domain.ExpiryAlert) { e.Subject = "" },
		"an unknown source": func(e *domain.ExpiryAlert) { e.Source = "partner" },
		"no line":           func(e *domain.ExpiryAlert) { e.Line = "" },
		"no end":            func(e *domain.ExpiryAlert) { e.CoverageEnd = time.Time{} },
		"no detection":      func(e *domain.ExpiryAlert) { e.DetectedAt = time.Time{} },
		"not a milestone":   func(e *domain.ExpiryAlert) { e.MilestoneDays = 10 },
		"beyond it":         func(e *domain.ExpiryAlert) { e.DaysLeft = 31 },
		"negative days":     func(e *domain.ExpiryAlert) { e.DaysLeft = -1 },
	} {
		t.Run(name, func(t *testing.T) {
			e := expiryAlert()
			change(&e)
			assert.ErrorIs(t, e.Validate(), domain.ErrInvalidExpiryAlert)
		})
	}
}
