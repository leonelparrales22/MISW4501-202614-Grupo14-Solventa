package main

import (
	"database/sql"
	"log"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/partnermetrics"
)

// partnerMetricsOptions mounts the summary of the dashboard of a commercial
// partner (HU-W3.2-01). It reads the offers and the policies of PostgreSQL, so
// without DATABASE_URL it is not mounted. Without the premium charge no policy
// is ever issued and the summary counts only the quotes.
func partnerMetricsOptions(db *sql.DB, verifier security.Verifier) []httpadapter.Option {
	if db == nil {
		log.Printf("%s is not set: the summary of the partner dashboard (HU-W3.2-01) is not mounted", envDatabaseURL)
		return nil
	}
	log.Printf("partner dashboard (HU-W3.2-01): GET /v1/partner-metrics/summary for the partner portal")
	return []httpadapter.Option{httpadapter.WithPartnerMetrics(
		partnermetrics.NewService(postgres.NewPartnerActivityStore(db)), verifier)}
}
