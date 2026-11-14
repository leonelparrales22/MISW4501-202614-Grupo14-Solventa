package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// HU-M3.2-01: the alerts before a policy expires, without DATABASE_URL.

func expiryAlert(t *testing.T, end time.Time) domain.PolicyExpiryApproaching {
	t.Helper()
	e, ok := domain.NewPolicyExpiryApproaching(domain.WalletPolicy{ID: "AU-2025-118204", Source: domain.SourcePolicyCore,
		Line: domain.LineAuto, Status: domain.PolicyActive, CoverageEnd: end}, "sub-a", outboxNow)
	require.True(t, ok)
	return e
}

func TestAnExpiryAlertIsRecordedOnceWithItsEvent(t *testing.T) {
	outbox := memory.NewOutbox()
	s := memory.NewExpiryAlertStore(outbox)
	ctx := context.Background()
	e := expiryAlert(t, outboxNow.AddDate(0, 0, 10))

	created, err := s.RecordExpiryAlert(ctx, e)
	require.NoError(t, err)
	assert.True(t, created)
	again, err := s.RecordExpiryAlert(ctx, e)
	require.NoError(t, err)
	assert.False(t, again, "the same policy, period and milestone again")

	pending := outbox.Pending()
	require.Len(t, pending, 1)
	assert.Equal(t, e.EventID(), pending[0].EventID)
	assert.Equal(t, "PolicyExpiryApproaching", pending[0].EventType)

	renewed := expiryAlert(t, outboxNow.AddDate(0, 0, 11))
	created, err = s.RecordExpiryAlert(ctx, renewed)
	require.NoError(t, err)
	assert.True(t, created, "another period is another alert")
}

func TestAnInvalidExpiryAlertIsNotRecorded(t *testing.T) {
	outbox := memory.NewOutbox()
	e := expiryAlert(t, outboxNow.AddDate(0, 0, 10))
	e.Subject = ""

	_, err := memory.NewExpiryAlertStore(outbox).RecordExpiryAlert(context.Background(), e)

	assert.ErrorIs(t, err, domain.ErrInvalidExpiryAlert)
	assert.Empty(t, outbox.Pending())
}

func TestTheLinkedSubjectsAreTheCustomersWithAnAccount(t *testing.T) {
	s := memory.NewCustomerStore()
	ctx := context.Background()
	_, err := s.Save(ctx, registeredCustomer("c2", "2"))
	require.NoError(t, err)
	_, err = s.Save(ctx, registeredCustomer("c1", "1"))
	require.NoError(t, err)
	_, err = s.Save(ctx, domain.Customer{ID: "c3", DocumentID: "3", FullName: "Sin cuenta"})
	require.NoError(t, err)

	got, err := s.LinkedSubjects(ctx)

	require.NoError(t, err)
	assert.Equal(t, []string{"sub-c1", "sub-c2"}, got)
}
