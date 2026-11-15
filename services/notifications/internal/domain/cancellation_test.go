package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

// HU-W1.6-02: the notice that a policy was cancelled for non-payment.

func cancellation() domain.PolicyCancellation {
	due := time.Date(2026, 8, 12, 5, 0, 0, 0, time.UTC)
	return domain.PolicyCancellation{PolicyID: "VH-2026-004512", Subject: "sub-a", Reason: domain.ReasonNonPayment,
		Line: "life", InstallmentSeq: 2, DueAt: due, GraceEndsAt: due.AddDate(0, 0, 30), CancelledAt: due.AddDate(0, 0, 31),
		AmountMinor: 14_230_000, Currency: "COP"}
}

func TestACancellationIsOneNotificationPerPolicy(t *testing.T) {
	c := cancellation()
	require.NoError(t, c.Validate())

	n := domain.NewCancellationNotification(c, c.CancelledAt.Add(2*time.Second))

	assert.Equal(t, "ntf-cancelled-VH-2026-004512", n.ID)
	assert.Equal(t, domain.KindPolicyCancelled, n.Kind)
	assert.Equal(t, "sub-a", n.Subject)
	assert.Equal(t, c.CancelledAt, n.OccurredAt)
	require.NotNil(t, n.Cancellation)
	assert.Equal(t, 2, n.Cancellation.InstallmentSeq)
}

func TestACancellationMissingWhatTheNoticeSaysIsRefused(t *testing.T) {
	for name, change := range map[string]func(*domain.PolicyCancellation){
		"no policy":         func(c *domain.PolicyCancellation) { c.PolicyID = "" },
		"no subject":        func(c *domain.PolicyCancellation) { c.Subject = "" },
		"another reason":    func(c *domain.PolicyCancellation) { c.Reason = "requested" },
		"the issuance":      func(c *domain.PolicyCancellation) { c.InstallmentSeq = 1 },
		"before it was due": func(c *domain.PolicyCancellation) { c.CancelledAt = c.DueAt },
		"no amount":         func(c *domain.PolicyCancellation) { c.AmountMinor = 0 },
		"no currency":       func(c *domain.PolicyCancellation) { c.Currency = "" },
		"id too long":       func(c *domain.PolicyCancellation) { c.PolicyID = strings.Repeat("x", 120) },
	} {
		c := cancellation()
		change(&c)
		assert.ErrorIs(t, c.Validate(), domain.ErrInvalidCancellation, name)
	}
}
