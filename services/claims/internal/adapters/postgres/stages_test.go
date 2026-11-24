package postgres_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/stages"
)

// HU-M3.3-01: the notices of the changes of stage, from the history of the
// status to the outbox.

// stageEvents reads the ClaimStageChanged of the outbox, oldest first.
func stageEvents(t *testing.T, s decisionStores) []map[string]any {
	t.Helper()
	rows, err := s.db.Query(`SELECT payload FROM outbox_event WHERE event_type = 'ClaimStageChanged' ORDER BY created_at, event_id`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		require.NoError(t, rows.Scan(&raw))
		e, err := events.Decode(raw)
		require.NoError(t, err)
		var p map[string]any
		require.NoError(t, json.Unmarshal(e.Payload, &p))
		out = append(out, p)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestEveryStageOfTheClaimBecomesItsEventOnce(t *testing.T) {
	s := newDecisionStores(t)
	ctx := context.Background()
	notices := postgres.StageNoticeStoreOf(s.claims)

	// Filed and approved by the advisor (newDecisionStores): active, in_evaluation.
	looked, err := notices.NoticePending(ctx, 100, stages.Event)
	require.NoError(t, err)
	assert.Equal(t, 2, looked)

	// Operations approves it and the gateway pays it.
	_, err = s.decisions.Decide(ctx, approval(t))
	require.NoError(t, err)
	claimed, ok, err := s.decisions.Claim(ctx, decidedClaim, decidedAt, 30*time.Second)
	require.NoError(t, err)
	require.True(t, ok)
	paidAt := decidedAt.Add(2 * time.Second)
	_, err = s.decisions.Confirm(ctx, claimed, paidAt, idempotency.Outcome{Reference: "po-1"})
	require.NoError(t, err)

	looked, err = notices.NoticePending(ctx, 100, stages.Event)
	require.NoError(t, err)
	assert.Equal(t, 2, looked)

	got := stageEvents(t, s)
	require.Len(t, got, 3, "filing notifies nothing; evaluation, approval and payment do")
	assert.Equal(t, []any{"in_evaluation", "approved", "paid"}, []any{got[0]["stage"], got[1]["stage"], got[2]["stage"]})
	for _, p := range got {
		assert.Equal(t, decidedClaim, p["claim_id"])
		assert.Equal(t, "sub-carolina", p["subject"])
		assert.Regexp(t, `^SIN-\d{4}-\d{5}$`, p["filing_number"])
	}
	assert.NotContains(t, got[0], "amount_minor")
	// The approval of approval(t): $ 4.350.000 less 5 % of deductible.
	assert.Equal(t, float64(413_250_000), got[1]["amount_minor"])
	assert.Equal(t, "COP", got[1]["currency"])
	assert.Equal(t, float64(413_250_000), got[2]["amount_minor"])
	assert.Equal(t, paidAt.Format(time.RFC3339), got[2]["paid_at"])

	// Nothing is looked at twice.
	looked, err = notices.NoticePending(ctx, 100, stages.Event)
	require.NoError(t, err)
	assert.Zero(t, looked)
	assert.Len(t, stageEvents(t, s), 3)
}

func TestTheChangesBeforeTheMigrationAreHistory(t *testing.T) {
	s := newDecisionStores(t)
	ctx := context.Background()
	// As if the migration had run after these two changes.
	_, err := s.db.Exec(`UPDATE claim_stage_notice_start SET first_change_id = (SELECT MAX(id) + 1 FROM claim_status_change)`)
	require.NoError(t, err)

	looked, err := postgres.StageNoticeStoreOf(s.claims).NoticePending(ctx, 100, stages.Event)
	require.NoError(t, err)
	assert.Zero(t, looked)
	assert.Empty(t, stageEvents(t, s))
}

func TestAParametricClaimIsNotToldByStages(t *testing.T) {
	ws := newWebStores(t)
	s := decisionStores{webStores: ws, decisions: postgres.DecisionStoreOf(ws.claims)}
	ctx := context.Background()
	c, r, d := resolution(t, parametricClaim, flightObservation())
	_, _, err := ws.claims.Resolve(ctx, c, r, d)
	require.NoError(t, err)

	looked, err := postgres.StageNoticeStoreOf(ws.claims).NoticePending(ctx, 100, stages.Event)
	require.NoError(t, err)
	assert.Equal(t, 2, looked, "in_evaluation and approved are looked at")
	assert.Empty(t, stageEvents(t, s))
}

func TestALimitedPassLeavesTheRestForTheNext(t *testing.T) {
	s := newDecisionStores(t)
	ctx := context.Background()
	notices := postgres.StageNoticeStoreOf(s.claims)

	looked, err := notices.NoticePending(ctx, 1, stages.Event)
	require.NoError(t, err)
	assert.Equal(t, 1, looked)
	assert.Empty(t, stageEvents(t, s), "the first change is the filing")

	looked, err = notices.NoticePending(ctx, 1, stages.Event)
	require.NoError(t, err)
	assert.Equal(t, 1, looked)
	assert.Len(t, stageEvents(t, s), 1)
}
