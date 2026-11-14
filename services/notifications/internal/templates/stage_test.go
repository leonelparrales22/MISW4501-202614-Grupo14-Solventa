package templates_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/templates"
)

// HU-M3.3-01: the push of each change of stage, in both languages. The time
// is written in Colombia: 15:02 UTC is 10:02.

func change(stage string, amount *int64, paidAt *time.Time) domain.ClaimStageChange {
	return domain.ClaimStageChange{ChangeID: 42, ClaimID: "cl-1", FilingNumber: "SIN-2026-01655", Subject: "sub-a",
		Stage: stage, ChangedAt: time.Date(2026, 9, 5, 15, 2, 0, 0, time.UTC), AmountMinor: amount, Currency: "COP",
		PaidAt: paidAt}
}

func TestThePushOfEachStage(t *testing.T) {
	amount := int64(413_250_000)
	paid := time.Date(2026, 9, 20, 14, 40, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		change domain.ClaimStageChange
		locale string
		want   templates.Push
	}{
		"evaluation": {change(domain.StageInEvaluation, nil, nil), domain.LocaleSpanish, templates.Push{
			Title: "Tu siniestro está en evaluación",
			Body:  "SIN-2026-01655 pasó a evaluación el 05/09/2026 a las 10:02. Toca para ver la línea de tiempo."}},
		"approval with its amount": {change(domain.StageApproved, &amount, nil), domain.LocaleSpanish, templates.Push{
			Title: "Tu siniestro fue aprobado: $ 4.132.500 COP",
			Body: "SIN-2026-01655 fue aprobado el 05/09/2026 a las 10:02. Te avisaremos cuando se haga el pago. " +
				"Toca para ver la línea de tiempo."}},
		"approval without amount": {change(domain.StageApproved, nil, nil), domain.LocaleSpanish, templates.Push{
			Title: "Tu siniestro fue aprobado",
			Body: "SIN-2026-01655 fue aprobado el 05/09/2026 a las 10:02. Te avisaremos cuando se haga el pago. " +
				"Toca para ver la línea de tiempo."}},
		"payment with amount and date": {change(domain.StagePaid, &amount, &paid), domain.LocaleSpanish, templates.Push{
			Title: "Pago de tu siniestro realizado: $ 4.132.500 COP",
			Body: "El pago de SIN-2026-01655 se hizo el 20/09/2026 a las 09:40 a tu medio registrado. " +
				"Toca para ver la línea de tiempo."}},
		"payment without date": {change(domain.StagePaid, nil, nil), domain.LocaleSpanish, templates.Push{
			Title: "Pago de tu siniestro realizado",
			Body: "El pago de SIN-2026-01655 se hizo el 05/09/2026 a las 10:02 a tu medio registrado. " +
				"Toca para ver la línea de tiempo."}},
		"evaluation in English": {change(domain.StageInEvaluation, nil, nil), domain.LocaleEnglish, templates.Push{
			Title: "Your claim is under evaluation",
			Body:  "SIN-2026-01655 moved to evaluation on 09/05/2026 at 10:02 AM. Tap to see the timeline."}},
		"approval in English": {change(domain.StageApproved, &amount, nil), domain.LocaleEnglish, templates.Push{
			Title: "Your claim was approved: $ 4,132,500 COP",
			Body:  "SIN-2026-01655 was approved on 09/05/2026 at 10:02 AM. We will let you know when it is paid. Tap to see the timeline."}},
		"payment in English": {change(domain.StagePaid, &amount, &paid), domain.LocaleEnglish, templates.Push{
			Title: "Your claim was paid: $ 4,132,500 COP",
			Body:  "The payment of SIN-2026-01655 was made on 09/20/2026 at 9:40 AM to your registered account. Tap to see the timeline."}},
		"payment in English without amount": {change(domain.StagePaid, nil, nil), domain.LocaleEnglish, templates.Push{
			Title: "Your claim was paid",
			Body:  "The payment of SIN-2026-01655 was made on 09/05/2026 at 10:02 AM to your registered account. Tap to see the timeline."}},
		"approval in English without amount": {change(domain.StageApproved, nil, nil), domain.LocaleEnglish, templates.Push{
			Title: "Your claim was approved",
			Body:  "SIN-2026-01655 was approved on 09/05/2026 at 10:02 AM. We will let you know when it is paid. Tap to see the timeline."}},
	} {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, templates.StageChange(tc.change, tc.locale))
		})
	}
}
