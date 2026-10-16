package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

func issuanceFor(policy string, status domain.ContractStatus, received, next time.Time) domain.ContractIssuance {
	return domain.ContractIssuance{
		Event:  domain.CoverageActivated{PolicyID: policy, Coverages: []domain.Coverage{{Code: domain.CoverageDeath}}},
		Status: status, ReceivedAt: received, NextAttemptAt: next,
	}
}

func TestContractStoreCreateIsIdempotentByPolicy(t *testing.T) {
	s := memory.NewContractStore()
	ctx := context.Background()

	_, created, err := s.Create(ctx, issuanceFor("pol-1", domain.ContractPending, t0, t0))
	require.NoError(t, err)
	assert.True(t, created)

	other := issuanceFor("pol-1", domain.ContractPending, t0.Add(time.Hour), t0)
	other.Event.EventID = "evt-other"
	stored, created, err := s.Create(ctx, other)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Empty(t, stored.Event.EventID, "the first issuance wins")
}

func TestContractStoreGetUpdateAndCopies(t *testing.T) {
	s := memory.NewContractStore()
	ctx := context.Background()
	_, err := s.Get(ctx, "pol-1")
	assert.ErrorIs(t, err, ports.ErrContractNotFound)
	assert.ErrorIs(t, s.Update(ctx, issuanceFor("pol-1", domain.ContractPending, t0, t0)), ports.ErrContractNotFound)

	c := issuanceFor("pol-1", domain.ContractPending, t0, t0)
	c.Document = &domain.ContractDocument{ID: "d1", Version: 1}
	_, _, err = s.Create(ctx, c)
	require.NoError(t, err)

	got, err := s.Get(ctx, "pol-1")
	require.NoError(t, err)
	got.Document.Version = 99
	got.Event.Coverages[0].Code = "mutated"
	again, _ := s.Get(ctx, "pol-1")
	assert.Equal(t, 1, again.Document.Version, "the store hands out copies")
	assert.Equal(t, domain.CoverageDeath, again.Event.Coverages[0].Code)

	again.Status = domain.ContractCompleted
	require.NoError(t, s.Update(ctx, again))
	updated, _ := s.Get(ctx, "pol-1")
	assert.Equal(t, domain.ContractCompleted, updated.Status)
}

func TestContractStoreListDue(t *testing.T) {
	s := memory.NewContractStore()
	ctx := context.Background()
	for _, c := range []domain.ContractIssuance{
		issuanceFor("late", domain.ContractPending, t0.Add(2*time.Second), t0),
		issuanceFor("early", domain.ContractDocumentReady, t0, t0),
		issuanceFor("future", domain.ContractPending, t0, t0.Add(time.Hour)),
		issuanceFor("done", domain.ContractCompleted, t0, time.Time{}),
		issuanceFor("failed", domain.ContractFailed, t0, time.Time{}),
	} {
		_, _, err := s.Create(ctx, c)
		require.NoError(t, err)
	}

	due, err := s.ListDue(ctx, t0.Add(5*time.Second), 10)
	require.NoError(t, err)
	require.Len(t, due, 2)
	assert.Equal(t, "early", due[0].Event.PolicyID, "oldest first")
	assert.Equal(t, "late", due[1].Event.PolicyID)

	limited, _ := s.ListDue(ctx, t0.Add(5*time.Second), 1)
	assert.Len(t, limited, 1)
}

func TestIssuanceAuditLogIsAppendOnlyPerPolicy(t *testing.T) {
	l := memory.NewIssuanceAuditLog()
	ctx := context.Background()
	require.NoError(t, l.Record(ctx, domain.IssuanceAuditEntry{PolicyID: "pol-1", Event: domain.AuditDocumentGenerated}))
	require.NoError(t, l.Record(ctx, domain.IssuanceAuditEntry{PolicyID: "pol-2", Event: domain.AuditDocumentGenerated}))
	require.NoError(t, l.Record(ctx, domain.IssuanceAuditEntry{PolicyID: "pol-1", Event: domain.AuditNotificationSent}))

	got, err := l.List(ctx, "pol-1")
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Equal(t, domain.AuditDocumentGenerated, got[0].Event)
	assert.Equal(t, domain.AuditNotificationSent, got[1].Event)

	got[0].Event = "tampered"
	again, _ := l.List(ctx, "pol-1")
	assert.Equal(t, domain.AuditDocumentGenerated, again[0].Event)

	none, err := l.List(ctx, "pol-404")
	require.NoError(t, err)
	assert.Empty(t, none)
}

type recordingHandler struct {
	events []domain.CoverageActivated
	err    error
}

func (r *recordingHandler) Receive(_ context.Context, ev domain.CoverageActivated) error {
	r.events = append(r.events, ev)
	return r.err
}

func TestActivationStubPublishesTheCoverageActivationOfTheAcceptance(t *testing.T) {
	h := &recordingHandler{}
	s := memory.NewActivationStub(h, &fixedClock{now: t0})
	acc := domain.Acceptance{ID: "acc-1"}
	offer := domain.Offer{
		ID: "o1", CustomerID: "cust-1", Premium: 89_500, Currency: "COP",
		Applicant: domain.Applicant{FullName: "Ana", DocumentID: "123", Email: "a@x.co"},
	}

	require.NoError(t, s.Start(context.Background(), acc, offer))

	assert.True(t, s.Started("acc-1"))
	require.Len(t, h.events, 1)
	ev := h.events[0]
	assert.Equal(t, "evt-coverage-activated-acc-1", ev.EventID, "deterministic, so a redelivery is recognized")
	assert.Equal(t, "pol-acc-1", ev.PolicyID)
	assert.Equal(t, "cust-1", ev.CustomerID)
	assert.Equal(t, offer.Applicant, ev.Holder)
	assert.EqualValues(t, 89_500, ev.Premium)
	assert.Equal(t, t0, ev.ActivatedAt)
	assert.Equal(t, t0.AddDate(0, memory.DemoTermMonths, 0), ev.CoverageEnd)
	assert.NoError(t, ev.Validate(), "the demo event is a valid one")
}

func TestActivationStubReportsAHandlerFailureSoTheStartIsRetried(t *testing.T) {
	h := &recordingHandler{err: assert.AnError}
	s := memory.NewActivationStub(h, &fixedClock{now: t0})

	err := s.Start(context.Background(), domain.Acceptance{ID: "acc-1"}, domain.Offer{CustomerID: "c"})

	assert.ErrorIs(t, err, assert.AnError)
}
