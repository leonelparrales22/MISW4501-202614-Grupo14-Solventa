package postgres_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

func pendingContract(id string) domain.ContractIssuance {
	return domain.ContractIssuance{
		Event: domain.CoverageActivated{
			EventID: "evt-" + id, PolicyID: "pol-" + id, AcceptanceID: id, CustomerID: "cust-1",
			Holder:  domain.Applicant{FullName: "Ana Pérez", DocumentID: "demo-document", Email: "ana@example.com"},
			Premium: 89_500.5, Currency: "COP", InsuredAmount: 250_000_000,
			Coverages: []domain.Coverage{
				{Code: domain.CoverageDeath, InsuredAmount: 250_000_000},
				{Code: domain.CoverageDisability, InsuredAmount: 125_000_000},
			},
			ActivatedAt: t0, CoverageStart: t0, CoverageEnd: t0.AddDate(10, 0, 0),
		},
		Status: domain.ContractPending, NotificationStatus: domain.NotificationPending, NotificationChannel: domain.ChannelEmail,
		NextAttemptAt: t0, ReceivedAt: t0, UpdatedAt: t0,
	}
}

func TestAContractIssuanceKeepsTheWholeEventAndIsCreatedOnce(t *testing.T) {
	s := postgres.NewContractStore(newDB(t))
	ctx := context.Background()

	stored, created, err := s.Create(ctx, pendingContract("acc-1"))
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, pendingContract("acc-1"), stored, "what is stored is what the document is built from")

	again := pendingContract("acc-1")
	again.Status = domain.ContractCompleted
	existing, created, err := s.Create(ctx, again)
	require.NoError(t, err)
	assert.False(t, created, "a redelivered activation changes nothing")
	assert.Equal(t, domain.ContractPending, existing.Status)

	_, err = s.Get(ctx, "pol-unknown")
	assert.ErrorIs(t, err, ports.ErrContractNotFound)
}

func TestUpdateStoresTheDocumentTheNotificationAndTheFailures(t *testing.T) {
	s := postgres.NewContractStore(newDB(t))
	ctx := context.Background()
	c, _, err := s.Create(ctx, pendingContract("acc-1"))
	require.NoError(t, err)

	c.Document = &domain.ContractDocument{ID: "doc-1", PolicyID: "pol-acc-1", Version: 1, StorageKey: "contracts/pol-acc-1/1.pdf",
		ContentType: "application/pdf", SizeBytes: 1234, Checksum: "abc", GeneratedAt: t0.Add(time.Second)}
	c.Status, c.AvailableAt = domain.ContractDocumentReady, t0.Add(time.Second)
	c.Attempts, c.LastFailureAt, c.LastFailure = 2, t0.Add(2*time.Second), "smtp down"
	c.NextAttemptAt, c.UpdatedAt = t0.Add(10*time.Second), t0.Add(2*time.Second)
	require.NoError(t, s.Update(ctx, c))

	got, err := s.Get(ctx, "pol-acc-1")
	require.NoError(t, err)
	assert.Equal(t, c, got)

	c.Status, c.NotificationStatus, c.NotifiedAt, c.NextAttemptAt = domain.ContractCompleted, domain.NotificationSent, t0.Add(time.Minute), time.Time{}
	require.NoError(t, s.Update(ctx, c))
	got, _ = s.Get(ctx, "pol-acc-1")
	assert.Equal(t, domain.ContractCompleted, got.Status)
	assert.True(t, got.NextAttemptAt.IsZero(), "nothing is due once it is completed")

	c.Event.PolicyID = "pol-unknown"
	assert.ErrorIs(t, s.Update(ctx, c), ports.ErrContractNotFound)
}

func TestListDueClaimsWhatItReturnsSoTwoTasksNeverAdvanceTheSameIssuance(t *testing.T) {
	s := postgres.NewContractStore(newDB(t))
	ctx := context.Background()
	for _, id := range []string{"acc-1", "acc-2", "acc-3"} {
		_, _, err := s.Create(ctx, pendingContract(id))
		require.NoError(t, err)
	}
	later := pendingContract("acc-4")
	later.NextAttemptAt = t0.Add(time.Hour)
	_, _, err := s.Create(ctx, later)
	require.NoError(t, err)
	done := pendingContract("acc-5")
	done.Status, done.NextAttemptAt = domain.ContractCompleted, time.Time{}
	_, _, err = s.Create(ctx, done)
	require.NoError(t, err)

	first, err := s.ListDue(ctx, t0, 2)
	require.NoError(t, err)
	require.Len(t, first, 2)
	assert.Equal(t, []string{"pol-acc-1", "pol-acc-2"}, []string{first[0].Event.PolicyID, first[1].Event.PolicyID}, "oldest first, up to the limit")

	second, err := s.ListDue(ctx, t0, 10)
	require.NoError(t, err)
	require.Len(t, second, 1, "the claimed ones are not handed out again")
	assert.Equal(t, "pol-acc-3", second[0].Event.PolicyID)

	// Update releases the claim: it is due again when its next attempt says so.
	first[0].NextAttemptAt, first[0].UpdatedAt = t0.Add(time.Second), t0
	require.NoError(t, s.Update(ctx, first[0]))
	again, err := s.ListDue(ctx, t0.Add(time.Second), 10)
	require.NoError(t, err)
	require.Len(t, again, 1)
	assert.Equal(t, "pol-acc-1", again[0].Event.PolicyID)

	// A worker that died leaves its lease to expire.
	expired, err := s.ListDue(ctx, t0.Add(10*time.Minute), 10)
	require.NoError(t, err)
	assert.Len(t, expired, 3, "acc-2, acc-3 and acc-1 are due again once their leases ended; acc-4 is due too")
}

func TestManyTasksClaimEachDueIssuanceOnce(t *testing.T) {
	s := postgres.NewContractStore(newDB(t))
	ctx := context.Background()
	for i := 0; i < 30; i++ {
		_, _, err := s.Create(ctx, pendingContract("acc-"+randomHex(t, 4)))
		require.NoError(t, err)
	}

	var mu sync.Mutex
	seen := map[string]int{}
	var wg sync.WaitGroup
	for task := 0; task < 6; task++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				due, err := s.ListDue(ctx, t0, 4)
				if err != nil || len(due) == 0 {
					return
				}
				mu.Lock()
				for _, c := range due {
					seen[c.Event.PolicyID]++
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	assert.Len(t, seen, 30)
	for id, n := range seen {
		assert.Equal(t, 1, n, id)
	}
}

func failedContract(id string, withDocument bool) domain.ContractIssuance {
	c := pendingContract(id)
	c.Status, c.NotificationStatus, c.Attempts, c.NextAttemptAt = domain.ContractFailed, domain.NotificationFailed, 12, time.Time{}
	c.LastFailureAt, c.LastFailure = t0.Add(time.Hour), "boom"
	if withDocument {
		c.Document = &domain.ContractDocument{ID: "doc-" + id, PolicyID: "pol-" + id, Version: 1, StorageKey: "k",
			ContentType: "application/pdf", SizeBytes: 1, Checksum: "c", GeneratedAt: t0}
	}
	return c
}

func TestRetryingAFailedContractReopensItAndRecordsTheAction(t *testing.T) {
	db := newDB(t)
	s, charges := postgres.NewContractStore(db), postgres.NewChargeStore(db)
	ctx := context.Background()
	for id, withDoc := range map[string]bool{"acc-nodoc": false, "acc-doc": true} {
		_, _, err := s.Create(ctx, failedContract(id, withDoc))
		require.NoError(t, err)
	}
	at := t0.Add(2 * time.Hour)

	reopened, err := s.ReopenContract(ctx, "acc-nodoc", domain.IssuanceRetry{By: "ops-1", Note: "El generador volvió", At: at})
	require.NoError(t, err)
	assert.Equal(t, domain.ContractPending, reopened.Status, "no document yet: it starts again from the document")
	assert.Zero(t, reopened.Attempts)
	assert.True(t, reopened.NextAttemptAt.Equal(at), "due at once")
	assert.Equal(t, domain.NotificationPending, reopened.NotificationStatus)

	withDoc, err := s.ReopenContract(ctx, "acc-doc", domain.IssuanceRetry{By: "ops-2", Note: "El correo volvió", At: at})
	require.NoError(t, err)
	assert.Equal(t, domain.ContractDocumentReady, withDoc.Status, "the document it has is kept; only the notification is retried")
	require.NotNil(t, withDoc.Document)
	assert.Equal(t, 1, withDoc.Document.Version)

	actions, err := charges.ListActions(ctx, "acc-nodoc")
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.Equal(t, domain.IssuanceActionRetry, actions[0].Action)
	assert.Equal(t, "ops-1", actions[0].Actor)
	assert.Equal(t, "El generador volvió", actions[0].Note)
	assert.True(t, actions[0].CreatedAt.Equal(at))

	_, err = s.ReopenContract(ctx, "acc-nodoc", domain.IssuanceRetry{By: "ops-3", Note: "again", At: at})
	assert.ErrorIs(t, err, ports.ErrContractNotRetryable, "it is not failed any more")
	_, err = s.ReopenContract(ctx, "acc-none", domain.IssuanceRetry{By: "ops-3", Note: "x", At: at})
	assert.ErrorIs(t, err, ports.ErrContractNotFound)
	actions, _ = charges.ListActions(ctx, "acc-nodoc")
	assert.Len(t, actions, 1, "a refused retry leaves no action")
}
