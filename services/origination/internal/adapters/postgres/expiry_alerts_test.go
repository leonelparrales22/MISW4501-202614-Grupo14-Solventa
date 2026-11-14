package postgres_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// HU-M3.2-01: the alerts 30, 15 and 5 days before a policy expires, in
// PostgreSQL.

var alertAt = time.Date(2026, 11, 14, 15, 0, 0, 0, time.UTC)

func coreAlert(t *testing.T, end time.Time) domain.PolicyExpiryApproaching {
	t.Helper()
	e, ok := domain.NewPolicyExpiryApproaching(domain.WalletPolicy{ID: "AU-2025-118204", Source: domain.SourcePolicyCore,
		Line: domain.LineAuto, Product: "Autos", InsuredObject: "KJT-482", Status: domain.PolicyActive, CoverageEnd: end},
		"sub-a", alertAt)
	require.True(t, ok)
	return e
}

func TestTheExpiringPoliciesLeaveOutTheOnesTheInsuredAlreadyActedOn(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	policies, renewals := postgres.NewPolicyStore(db), postgres.NewRenewalStore(db)
	ctx := context.Background()
	soon := alertAt.AddDate(0, 0, 10)
	for _, p := range []domain.Policy{
		samplePolicy("pol-open", "sub-1", soon),
		samplePolicy("pol-accepted", "sub-1", soon),
		samplePolicy("pol-lapsed", "sub-1", soon),
		samplePolicy("pol-renewed", "sub-1", soon),
		samplePolicy("pol-ended", "sub-1", alertAt.Add(-time.Hour)),
		samplePolicy("pol-far", "sub-1", alertAt.AddDate(0, 0, 45)),
	} {
		require.NoError(t, policies.Create(ctx, p))
	}
	successor := samplePolicy("pol-next", "sub-1", soon.AddDate(1, 0, 0))
	successor.RenewalOf = "pol-renewed"
	require.NoError(t, policies.Create(ctx, successor))
	_, _, err := renewals.Create(ctx, sampleRenewal(samplePolicy("pol-accepted", "sub-1", soon), domain.RenewalAccepted))
	require.NoError(t, err)
	lapsed := sampleRenewal(samplePolicy("pol-lapsed", "sub-1", soon), domain.RenewalNotRenewed)
	lapsed.Reason, lapsed.ClosedAt = domain.NotRenewedNoResponse, alertAt
	_, _, err = renewals.Create(ctx, lapsed)
	require.NoError(t, err)

	got, err := policies.ListExpiringUnrenewed(ctx, alertAt, alertAt.AddDate(0, 0, 31))

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "pol-open", got[0].ID)
	assert.Equal(t, "sub-1", got[0].CustomerID)
}

func TestAnExpiryAlertIsRecordedOnceWithItsEventInOneTransaction(t *testing.T) {
	requireOfferDocker(t)
	db := newDB(t)
	s := postgres.NewExpiryAlertStore(db)
	ctx := context.Background()
	e := coreAlert(t, alertAt.AddDate(0, 0, 16))

	created, err := s.RecordExpiryAlert(ctx, e)
	require.NoError(t, err)
	assert.True(t, created)
	again, err := s.RecordExpiryAlert(ctx, e)
	require.NoError(t, err)
	assert.False(t, again)

	var (
		eventType string
		payload   []byte
		notices   int
	)
	require.NoError(t, db.QueryRowContext(ctx, `SELECT event_type, payload FROM outbox_event WHERE event_id = $1`,
		e.EventID()).Scan(&eventType, &payload))
	assert.Equal(t, "PolicyExpiryApproaching", eventType)
	env, err := events.Decode(payload)
	require.NoError(t, err)
	var p map[string]any
	require.NoError(t, json.Unmarshal(env.Payload, &p))
	assert.Equal(t, "AU-2025-118204", p["policy_id"])
	assert.Equal(t, float64(30), p["milestone_days"])
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM policy_expiry_notice`).Scan(&notices))
	assert.Equal(t, 1, notices)
	var written int
	require.NoError(t, db.QueryRowContext(ctx, `SELECT count(*) FROM outbox_event WHERE event_type = 'PolicyExpiryApproaching'`).
		Scan(&written))
	assert.Equal(t, 1, written)
}

func TestOfTwoTasksAlertingTheSameMilestoneExactlyOneRecordsIt(t *testing.T) {
	requireOfferDocker(t)
	s := postgres.NewExpiryAlertStore(newDB(t))
	e := coreAlert(t, alertAt.AddDate(0, 0, 4))
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
	)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := s.RecordExpiryAlert(context.Background(), e)
			assert.NoError(t, err)
			if ok {
				mu.Lock()
				created++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, created)
}

func TestTheLinkedSubjectsAreTheCustomersWithAnAccountInPostgres(t *testing.T) {
	requireOfferDocker(t)
	s := postgres.NewCustomerStore(newDB(t))
	ctx := context.Background()
	for i, id := range []string{"cus-b", "cus-a"} {
		c := cliente(id, string(rune('1'+i))+"020345678", id+"@example.com")
		c.CognitoSub = "sub-" + id
		_, err := s.Save(ctx, c)
		require.NoError(t, err)
	}
	_, err := s.Save(ctx, cliente("cus-c", "3020345678", "c@example.com"))
	require.NoError(t, err)

	got, err := s.LinkedSubjects(ctx)

	require.NoError(t, err)
	assert.Equal(t, []string{"sub-cus-a", "sub-cus-b"}, got)
}
