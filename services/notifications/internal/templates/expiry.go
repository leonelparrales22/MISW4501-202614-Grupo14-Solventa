package templates

import (
	"fmt"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
)

// lineNames are the names of the lines of insurance, by locale, used when the
// source does not name the product (origination's life policies).
var lineNames = map[string][2]string{
	"life":   {"Vida", "life"},
	"auto":   {"Autos", "auto"},
	"home":   {"Hogar", "home"},
	"travel": {"Viaje", "travel"},
}

// PolicyExpiry writes the push of a policy about to expire (HU-M3.2-01): the
// policy, its exact expiry date in Colombia and the days left (criterion 1).
// Any locale other than English is Spanish.
func PolicyExpiry(e domain.ExpiryAlert, locale string) Push {
	english := locale == domain.LocaleEnglish
	end := e.CoverageEnd.In(colombia)
	name := policyName(e, english)
	if english {
		return Push{Title: fmt.Sprintf("Your %s policy expires on %s", name, end.Format("01/02/2006")),
			Body: fmt.Sprintf("%s expires %s. Renew it to keep your coverage. Tap to see the policy.",
				e.PolicyID, daysLeft(e.DaysLeft, true))}
	}
	return Push{Title: fmt.Sprintf("Tu póliza %s vence el %s", name, end.Format("02/01/2006")),
		Body: fmt.Sprintf("%s vence %s. Renuévala para no perder tu cobertura. Toca para ver la póliza.",
			e.PolicyID, daysLeft(e.DaysLeft, false))}
}

// policyName is the product of the policy, or the name of its line when the
// source does not name it.
func policyName(e domain.ExpiryAlert, english bool) string {
	if e.Product != "" {
		return e.Product
	}
	names, ok := lineNames[e.Line]
	if !ok {
		return e.Line
	}
	if english {
		return names[1]
	}
	return names[0]
}

// daysLeft writes «en 15 días», «mañana» or «hoy» / «in 15 days»,
// «tomorrow» or «today».
func daysLeft(days int, english bool) string {
	switch {
	case english && days == 0:
		return "today"
	case english && days == 1:
		return "tomorrow"
	case english:
		return fmt.Sprintf("in %d days", days)
	case days == 0:
		return "hoy"
	case days == 1:
		return "mañana"
	}
	return fmt.Sprintf("en %d días", days)
}
