package domain_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

func TestOfferIsExpiredFromItsExpiryOn(t *testing.T) {
	expiresAt := time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC)
	o := domain.Offer{ExpiresAt: expiresAt}

	assert.False(t, o.IsExpired(expiresAt.Add(-time.Nanosecond)))
	assert.True(t, o.IsExpired(expiresAt))
	assert.True(t, o.IsExpired(expiresAt.Add(time.Second)))
}

func TestParseChannel(t *testing.T) {
	for _, s := range []string{"web", "mobile"} {
		c, ok := domain.ParseChannel(s)
		assert.True(t, ok)
		assert.Equal(t, domain.Channel(s), c)
	}
	for _, s := range []string{"", "Web", "embedded"} {
		_, ok := domain.ParseChannel(s)
		assert.False(t, ok, s)
	}
}
