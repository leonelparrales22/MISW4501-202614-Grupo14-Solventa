package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

// HU-M3.3-01: the notification of a change of stage of a claim.

var stageAt = time.Date(2026, 9, 5, 15, 2, 0, 0, time.UTC)

func stageChange() domain.ClaimStageChange {
	return domain.ClaimStageChange{ChangeID: 42, ClaimID: "cl-1", FilingNumber: "SIN-2026-01655",
		PolicyID: "HG-2025-880231", Subject: "sub-a", Stage: domain.StageInEvaluation, ChangedAt: stageAt}
}

func TestAChangeOfStageIsOneNotificationOfItsOwn(t *testing.T) {
	c := stageChange()
	n := domain.NewStageNotification(c, stageAt.Add(3*time.Second))

	assert.Equal(t, "ntf-stage-42", n.ID)
	assert.Equal(t, domain.KindClaimStageChanged, n.Kind)
	assert.Equal(t, "sub-a", n.Subject)
	assert.Equal(t, stageAt, n.OccurredAt, "the center shows when the stage changed (criterion 5)")
	require.NotNil(t, n.Stage)
	assert.Equal(t, c, *n.Stage)
	assert.Nil(t, n.Payout)
}

func TestAChangeOfStageNeedsWhatTheNotificationShows(t *testing.T) {
	assert.NoError(t, stageChange().Validate())
	amount := int64(0)
	for name, change := range map[string]func(*domain.ClaimStageChange){
		"no change id":     func(c *domain.ClaimStageChange) { c.ChangeID = 0 },
		"no claim":         func(c *domain.ClaimStageChange) { c.ClaimID = "" },
		"no filing number": func(c *domain.ClaimStageChange) { c.FilingNumber = "" },
		"no subject":       func(c *domain.ClaimStageChange) { c.Subject = "" },
		"an unknown stage": func(c *domain.ClaimStageChange) { c.Stage = "rejected" },
		"no time":          func(c *domain.ClaimStageChange) { c.ChangedAt = time.Time{} },
		"a zero amount":    func(c *domain.ClaimStageChange) { c.AmountMinor = &amount; c.Currency = "COP" },
		"a bad currency":   func(c *domain.ClaimStageChange) { a := int64(1); c.AmountMinor = &a; c.Currency = "PESOS" },
	} {
		t.Run(name, func(t *testing.T) {
			c := stageChange()
			change(&c)
			assert.ErrorIs(t, c.Validate(), domain.ErrInvalidStageChange)
		})
	}
}
