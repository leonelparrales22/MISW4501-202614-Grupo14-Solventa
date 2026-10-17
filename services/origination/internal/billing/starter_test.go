package billing_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/billing"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

var t0 = time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)

func demoTerms(domain.Offer) (domain.CoverageTerms, error) {
	return domain.CoverageTerms{InsuredAmount: 250_000_000, TermMonths: 120, Coverages: []domain.Coverage{
		{Code: domain.CoverageDeath, InsuredAmount: 250_000_000},
		{Code: domain.CoverageDisability, InsuredAmount: 250_000_000},
	}}, nil
}

func acceptance(id string) domain.Acceptance {
	return domain.Acceptance{ID: id, OfferID: "demo-active-" + id, CustomerID: "cust-1", Channel: domain.ChannelWeb,
		AcceptedAt: t0.Add(-time.Minute), Status: domain.AcceptanceRegistered}
}

func offer(id string) domain.Offer {
	return domain.Offer{ID: "demo-active-" + id, CustomerID: "cust-1", Premium: 89_500, Currency: "COP",
		Applicant: domain.Applicant{FullName: "Ana Pérez", DocumentID: "demo-document", Email: "ana@example.com"}}
}

// captureLogs sends the default slog output to a buffer for the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestStartStoresOnePendingChargeWithTheFullSnapshotAndNeverCallsTheGateway(t *testing.T) {
	repo := newMemRepo()
	notified := 0
	s := billing.NewStarter(repo, demoTerms, &clock{now: t0}, resilience.DefaultRetryPolicy(), func() { notified++ })

	require.NoError(t, s.Start(context.Background(), acceptance("acc-1"), offer("acc-1")))
	require.NoError(t, s.Start(context.Background(), acceptance("acc-1"), offer("acc-1")), "idempotent by acceptance")

	assert.Len(t, repo.rows, 1)
	c := repo.get("acc-1")
	assert.Equal(t, domain.ChargePending, c.Status)
	assert.Equal(t, "cobro-acc-1", c.IdempotencyKey)
	assert.EqualValues(t, 8_950_000, c.AmountMinor)
	assert.Equal(t, "Ana Pérez", c.Holder.FullName)
	assert.Equal(t, 120, c.Terms.TermMonths)
	assert.Len(t, c.Terms.Coverages, 2)
	assert.Equal(t, t0.Add(24*time.Hour), c.WindowEndsAt, "24-hour retry window")
	assert.Equal(t, 1, notified, "the worker is woken once")
}

func TestStartDoesNotChargeWhatCannotBeActivated(t *testing.T) {
	logs := captureLogs(t)
	repo := newMemRepo()
	s := billing.NewStarter(repo, demoTerms, &clock{now: t0}, resilience.DefaultRetryPolicy(), nil)
	o := offer("acc-1")
	o.Applicant.Email = ""

	err := s.Start(context.Background(), acceptance("acc-1"), o)

	require.Error(t, err, "the acceptance stays registered and is retried")
	assert.Empty(t, repo.rows, "no charge is stored")
	assert.Contains(t, logs.String(), "the coverage could not be activated")
	assert.NotContains(t, logs.String(), "Ana", "no personal data in the logs")
}

func TestStartRejectsAnOfferWithoutPremium(t *testing.T) {
	repo := newMemRepo()
	s := billing.NewStarter(repo, demoTerms, &clock{now: t0}, resilience.DefaultRetryPolicy(), nil)
	o := offer("acc-1")
	o.Premium = 0

	assert.Error(t, s.Start(context.Background(), acceptance("acc-1"), o))
	assert.Empty(t, repo.rows)
}

func TestStartReportsTermsAndStoreFailures(t *testing.T) {
	boom := errors.New("boom")
	repo := newMemRepo()
	noTerms := billing.NewStarter(repo, func(domain.Offer) (domain.CoverageTerms, error) { return domain.CoverageTerms{}, boom },
		&clock{now: t0}, resilience.DefaultRetryPolicy(), nil)
	assert.ErrorIs(t, noTerms.Start(context.Background(), acceptance("acc-1"), offer("acc-1")), boom)

	repo.createErr = boom
	s := billing.NewStarter(repo, demoTerms, &clock{now: t0}, resilience.DefaultRetryPolicy(), nil)
	assert.ErrorIs(t, s.Start(context.Background(), acceptance("acc-1"), offer("acc-1")), boom)
}
