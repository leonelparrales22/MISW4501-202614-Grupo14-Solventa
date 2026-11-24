package templates

import (
	"fmt"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

// colombia is the zone the dates of the pushes are written in: the insured
// reads the time the stage changed where they are.
var colombia = time.FixedZone("COT", -5*60*60)

// StageChange writes the push of a change of stage of a claim (HU-M3.3-01):
// the stage, the claim, the date and time of the change (criterion 5) and,
// for the approval and the payment, the amount (criteria 2 and 3). Any locale
// other than English is Spanish.
func StageChange(c domain.ClaimStageChange, locale string) Push {
	english := locale == domain.LocaleEnglish
	when := dateTime(c.ChangedAt, english)
	amount := ""
	if c.AmountMinor != nil {
		amount = Amount(*c.AmountMinor, c.Currency, locale)
	}
	if english {
		return stageInEnglish(c, when, amount)
	}
	switch c.Stage {
	case domain.StageInEvaluation:
		return Push{Title: "Tu siniestro está en evaluación",
			Body: fmt.Sprintf("%s pasó a evaluación el %s. Toca para ver la línea de tiempo.", c.FilingNumber, when)}
	case domain.StageApproved:
		title := "Tu siniestro fue aprobado"
		if amount != "" {
			title += ": " + amount
		}
		return Push{Title: title, Body: fmt.Sprintf("%s fue aprobado el %s. Te avisaremos cuando se haga el pago. "+
			"Toca para ver la línea de tiempo.", c.FilingNumber, when)}
	}
	title := "Pago de tu siniestro realizado"
	if amount != "" {
		title += ": " + amount
	}
	paid := when
	if c.PaidAt != nil {
		paid = dateTime(*c.PaidAt, false)
	}
	return Push{Title: title, Body: fmt.Sprintf("El pago de %s se hizo el %s a tu medio registrado. "+
		"Toca para ver la línea de tiempo.", c.FilingNumber, paid)}
}

func stageInEnglish(c domain.ClaimStageChange, when, amount string) Push {
	switch c.Stage {
	case domain.StageInEvaluation:
		return Push{Title: "Your claim is under evaluation",
			Body: fmt.Sprintf("%s moved to evaluation on %s. Tap to see the timeline.", c.FilingNumber, when)}
	case domain.StageApproved:
		title := "Your claim was approved"
		if amount != "" {
			title += ": " + amount
		}
		return Push{Title: title, Body: fmt.Sprintf("%s was approved on %s. We will let you know when it is paid. "+
			"Tap to see the timeline.", c.FilingNumber, when)}
	}
	title := "Your claim was paid"
	if amount != "" {
		title += ": " + amount
	}
	paid := when
	if c.PaidAt != nil {
		paid = dateTime(*c.PaidAt, true)
	}
	return Push{Title: title, Body: fmt.Sprintf("The payment of %s was made on %s to your registered account. "+
		"Tap to see the timeline.", c.FilingNumber, paid)}
}

// dateTime writes «05/09/2026 a las 10:02» or «09/05/2026 at 10:02 AM».
func dateTime(t time.Time, english bool) string {
	t = t.In(colombia)
	if english {
		return t.Format("01/02/2006") + " at " + t.Format("3:04 PM")
	}
	return t.Format("02/01/2006") + " a las " + t.Format("15:04")
}
