// Package templates writes the text of each push, in the language of the
// device (HU-M3.1-01). The notification center does not use it: the app
// writes its own texts from the data of the notification.
package templates

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

// ParameterFlightDelay is the parameter of a flight delay policy.
const ParameterFlightDelay = "flight_delay"

// Push is the title and body of one push.
type Push struct {
	Title string
	Body  string
}

// ParametricPayout writes the push of an automatic payment (criterion 2): the
// amount, the currency and what happened, so the insured knows without
// opening the app. Any locale other than English is Spanish, the default
// language of the app.
func ParametricPayout(p domain.ParametricPayout, locale string) Push {
	amount := Amount(p.AmountMinor, p.Currency, locale)
	t := p.Trigger
	observed := strconv.FormatFloat(t.Observed, 'f', -1, 64)
	if locale == domain.LocaleEnglish {
		body := fmt.Sprintf("The condition of your policy was met (%s). The payment is already in your account. Tap to see the details.",
			t.EventReference)
		if t.Parameter == ParameterFlightDelay {
			body = fmt.Sprintf("Your flight %s was %s min late. The payment of your travel insurance is already in your account. "+
				"Tap to see the details.", t.EventReference, observed)
		}
		return Push{Title: "Automatic payment received: " + amount, Body: body}
	}
	body := fmt.Sprintf("Se cumplió la condición de tu seguro (%s). El pago ya está en tu cuenta. Toca para ver el detalle.",
		t.EventReference)
	if t.Parameter == ParameterFlightDelay {
		body = fmt.Sprintf("Tu vuelo %s se retrasó %s min. El pago de tu seguro de viaje ya está en tu cuenta. "+
			"Toca para ver el detalle.", t.EventReference, observed)
	}
	return Push{Title: "Pago automático recibido: " + amount, Body: body}
}

// Amount writes an amount in minor units the way the app does: «$ 180.000
// COP» in Spanish and «$ 180,000 COP» in English, without cents.
func Amount(minor int64, currency, locale string) string {
	sep := "."
	if locale == domain.LocaleEnglish {
		sep = ","
	}
	digits := strconv.FormatInt((minor+50)/100, 10)
	var b strings.Builder
	for i, r := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteString(sep)
		}
		b.WriteRune(r)
	}
	return "$ " + b.String() + " " + currency
}
