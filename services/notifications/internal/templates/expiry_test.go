package templates_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/templates"
)

// HU-M3.2-01: the push of a policy about to expire, in both languages, with
// the exact date in Colombia: 30/11 at 00:00 UTC is still 29/11 there.

func alert(product, line string, days int) domain.ExpiryAlert {
	return domain.ExpiryAlert{PolicyID: "AU-2025-118204", Subject: "sub-a", Source: domain.ExpirySourcePolicyCore,
		Line: line, Product: product, CoverageEnd: time.Date(2026, 11, 30, 0, 0, 0, 0, time.UTC), MilestoneDays: 30,
		DaysLeft: days, DetectedAt: time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC)}
}

func TestThePushOfAPolicyAboutToExpire(t *testing.T) {
	for name, tc := range map[string]struct {
		alert  domain.ExpiryAlert
		locale string
		want   templates.Push
	}{
		"spanish": {alert("Autos", "auto", 15), domain.LocaleSpanish, templates.Push{
			Title: "Tu póliza Autos vence el 29/11/2026",
			Body:  "AU-2025-118204 vence en 15 días. Renuévala para no perder tu cobertura. Toca para ver la póliza."}},
		"english": {alert("Autos", "auto", 15), domain.LocaleEnglish, templates.Push{
			Title: "Your Autos policy expires on 11/29/2026",
			Body:  "AU-2025-118204 expires in 15 days. Renew it to keep your coverage. Tap to see the policy."}},
		"life without product": {alert("", "life", 1), domain.LocaleSpanish, templates.Push{
			Title: "Tu póliza Vida vence el 29/11/2026",
			Body:  "AU-2025-118204 vence mañana. Renuévala para no perder tu cobertura. Toca para ver la póliza."}},
		"life in english, today": {alert("", "life", 0), domain.LocaleEnglish, templates.Push{
			Title: "Your life policy expires on 11/29/2026",
			Body:  "AU-2025-118204 expires today. Renew it to keep your coverage. Tap to see the policy."}},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, templates.PolicyExpiry(tc.alert, tc.locale))
		})
	}
}

func TestTheOtherDaysAndLines(t *testing.T) {
	assert.Contains(t, templates.PolicyExpiry(alert("", "pet", 0), domain.LocaleSpanish).Title, "póliza pet")
	assert.Contains(t, templates.PolicyExpiry(alert("", "home", 1), domain.LocaleEnglish).Body, "expires tomorrow")
	assert.Contains(t, templates.PolicyExpiry(alert("", "travel", 0), domain.LocaleSpanish).Body, "vence hoy")
}
