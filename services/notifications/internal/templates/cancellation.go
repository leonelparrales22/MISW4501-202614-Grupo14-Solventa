package templates

import (
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

// PolicyCancelled writes the push of a policy cancelled for non-payment
// (HU-W1.6-02): the policy, the reason, that there is no refund and that the
// insured can quote again. Any locale other than English is Spanish.
func PolicyCancelled(c domain.PolicyCancellation, locale string) Push {
	when := c.CancelledAt.In(colombia)
	if locale == domain.LocaleEnglish {
		return Push{Title: "Your policy was cancelled for non-payment",
			Body: fmt.Sprintf("%s was cancelled on %s: the premium of month %d was not paid within the grace period. There is no refund. Tap to see the details.",
				c.PolicyID, when.Format("01/02/2006"), c.InstallmentSeq)}
	}
	return Push{Title: "Tu póliza fue cancelada por falta de pago",
		Body: fmt.Sprintf("%s se canceló el %s: no recibimos el pago de la cuota %d dentro del periodo de gracia. No hay reembolso. Toca para ver el detalle.",
			c.PolicyID, when.Format("02/01/2006"), c.InstallmentSeq)}
}
