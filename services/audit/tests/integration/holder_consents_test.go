// This file checks the holder's view of the consents (HU-M1.3-01) against a
// real PostgreSQL: two holders, events inserted by SQL as HU-M1.3-02 will
// write them, a document stored in another case, the append-only tables and
// the idempotent link.
package integration_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

// grantFor records a consent of the holder with the document through the
// partner route, as screen E1 does.
func (e *env) grantFor(t *testing.T, customer, application, document string) string {
	t.Helper()
	body := `{"customer_id":"` + customer + `","document_id":"` + document + `","full_name":"Ana María Pérez","terms_version":"v1",
"accepted_sources":["open_finance","open_data","kyc"],"accepted_data_policy":true,"channel":"embedded",
"partner_id":"aliado-banco-1","application_id":"` + application + `"}`
	status, out := e.do(http.MethodPost, "/v1/audit/consents", body, "")
	require.Equal(t, http.StatusCreated, status)
	return out["consent_id"].(string)
}

func TestTheHolderStoresSeparateTheHoldersAndReadTheHistory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	start := e.clock.Now()
	consents, events, links := postgres.NewConsentStore(e.db), postgres.NewConsentEventStore(e.db), postgres.NewHolderLinkStore(e.db)

	a1 := e.grantFor(t, "cust-a", "app-a1", "ab12345")
	e.clock.set(start.Add(time.Hour))
	a2 := e.grantFor(t, "cust-a", "app-a2", "AB12345")
	b1 := e.grantFor(t, "cust-b", "app-b1", "9988776655")
	revokedAt := start.Add(2 * time.Hour)
	_, err := e.db.ExecContext(ctx, `INSERT INTO consent_event (event_id, consent_id, status, occurred_at, receipt_id)
		VALUES ('ev-rev-1', $1, 'revoked', $2, 'REV-0001')`, a1, revokedAt)
	require.NoError(t, err)

	for _, l := range []domain.HolderLink{
		{Subject: "sub-a", DocumentID: domain.NormalizeDocument("ab12345"), CustomerID: "cus-a", LinkedAt: start, EventID: "customer-linked-cus-a"},
		{Subject: "sub-b", DocumentID: "9988776655", CustomerID: "cus-b", LinkedAt: start, EventID: "customer-linked-cus-b"},
	} {
		_, created, err := links.Link(ctx, l)
		require.NoError(t, err)
		require.True(t, created)
		_, created, err = links.Link(ctx, l)
		require.NoError(t, err)
		assert.False(t, created, "a repeated link does not duplicate")
	}

	docA, err := links.DocumentOf(ctx, "sub-a")
	require.NoError(t, err)
	ownA, err := consents.ListByHolder(ctx, docA)
	require.NoError(t, err)
	require.Len(t, ownA, 2, "the document stored in lower case is found")
	assert.ElementsMatch(t, []string{a1, a2}, []string{ownA[0].ID, ownA[1].ID})

	docB, err := links.DocumentOf(ctx, "sub-b")
	require.NoError(t, err)
	ownB, err := consents.ListByHolder(ctx, docB)
	require.NoError(t, err)
	require.Len(t, ownB, 1)
	assert.Equal(t, b1, ownB[0].ID, "B never gets A's consents")

	byConsent, err := events.ListByConsents(ctx, []string{a1, a2})
	require.NoError(t, err)
	now := revokedAt.Add(time.Hour)
	views := make([]domain.ConsentView, 0, len(ownA))
	for _, c := range ownA {
		views = append(views, domain.View(c, byConsent[c.ID], now, domain.EntityNames{"aliado-banco-1": "Banco Aliado"}))
	}
	domain.SortForHolder(views)
	assert.Equal(t, a2, views[0].Consent.ID)
	assert.Equal(t, domain.StatusActive, views[0].Status)
	assert.Equal(t, a1, views[1].Consent.ID)
	assert.Equal(t, domain.StatusRevoked, views[1].Status)
	assert.Equal(t, "REV-0001", views[1].RevocationReceiptID)
	assert.True(t, revokedAt.Equal(views[1].EndedAt))
	assert.Equal(t, "Banco Aliado", views[1].Entities[0].Name)

	for _, stmt := range []string{
		`UPDATE consent_event SET status = 'active'`, `DELETE FROM consent_event`, `TRUNCATE consent_event`,
		`UPDATE holder_link SET document_id = 'X'`, `DELETE FROM holder_link`, `TRUNCATE holder_link`,
		`UPDATE consent_record SET purpose = 'x'`, `DELETE FROM consent_record`, `TRUNCATE consent_record CASCADE`,
	} {
		_, err := e.db.ExecContext(ctx, stmt)
		assert.Error(t, err, stmt)
	}
}
