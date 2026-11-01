package templates_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/templates"
)

// The payment of the prototype (M15b): $ 180.000 COP for the flight AV-8421,
// 215 minutes late.
func payout(parameter string) domain.ParametricPayout {
	return domain.ParametricPayout{
		PayoutID: "ppo-1", Subject: "sub-1", AmountMinor: 18_000_000, Currency: "COP",
		Trigger: domain.Trigger{Parameter: parameter, EventReference: "AV-8421", Threshold: 180, Observed: 215, Unit: "minutes"},
		PaidAt:  time.Date(2026, 9, 11, 21, 40, 0, 0, time.UTC),
	}
}

func TestTheFlightDelayPushOfThePrototype(t *testing.T) {
	got := templates.ParametricPayout(payout(templates.ParameterFlightDelay), domain.LocaleSpanish)

	assert.Equal(t, "Pago automático recibido: $ 180.000 COP", got.Title)
	assert.Equal(t, "Tu vuelo AV-8421 se retrasó 215 min. El pago de tu seguro de viaje ya está en tu cuenta. "+
		"Toca para ver el detalle.", got.Body)
}

func TestTheFlightDelayPushInEnglish(t *testing.T) {
	got := templates.ParametricPayout(payout(templates.ParameterFlightDelay), domain.LocaleEnglish)

	assert.Equal(t, "Automatic payment received: $ 180,000 COP", got.Title)
	assert.Contains(t, got.Body, "Your flight AV-8421 was 215 min late.")
}

func TestAnotherParameterStillSaysWhatHappened(t *testing.T) {
	es := templates.ParametricPayout(payout("rainfall"), "")
	en := templates.ParametricPayout(payout("rainfall"), domain.LocaleEnglish)

	assert.Contains(t, es.Body, "(AV-8421)")
	assert.Contains(t, en.Body, "(AV-8421)")
	assert.Contains(t, es.Title, "$ 180.000 COP", "any other locale is Spanish")
}

func TestAmountGroupsTheThousands(t *testing.T) {
	assert.Equal(t, "$ 1.250.000 COP", templates.Amount(125_000_000, "COP", "es"))
	assert.Equal(t, "$ 999 COP", templates.Amount(99_900, "COP", "es"))
	assert.Equal(t, "$ 1,000 USD", templates.Amount(99_999, "USD", "en"), "the cents are rounded")
}
