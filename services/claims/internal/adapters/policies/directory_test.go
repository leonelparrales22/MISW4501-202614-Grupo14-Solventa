package policies_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	corepolicies "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/policies"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

type fakeCore struct {
	policies []corepolicies.Policy
	err      error
	got      string
}

func (f *fakeCore) HolderPolicies(_ context.Context, holderID string) ([]corepolicies.Policy, error) {
	f.got = holderID
	return f.policies, f.err
}

var (
	from = time.Date(2025, 11, 1, 0, 0, 0, 0, time.FixedZone("UTC-5", -5*60*60))
	to   = from.AddDate(1, 0, 0)
)

func TestThePoliciesOfTheCoreBecomeDomainPolicies(t *testing.T) {
	core := &fakeCore{policies: []corepolicies.Policy{{
		PolicyID: "AU-2025-118204", Line: "auto", Product: "Autos", InsuredObject: "KJT-482", Status: "active",
		ValidFrom: from, ValidTo: to, CoveredIncidents: []string{"collision", "glass"},
	}}}

	got, err := policies.NewDirectory(core).HolderPolicies(context.Background(), "sub-carolina")

	require.NoError(t, err)
	assert.Equal(t, "sub-carolina", core.got, "the holder is the subject of the token")
	assert.Equal(t, []domain.Policy{{
		ID: "AU-2025-118204", Line: domain.LineAuto, Product: "Autos", InsuredObject: "KJT-482",
		Status: domain.PolicyActive, ValidFrom: from.UTC(), ValidTo: to.UTC(),
		CoveredIncidents: []domain.IncidentType{domain.IncidentCollision, domain.IncidentGlass},
	}}, got)
}

func TestAPolicyTheCoreDescribesBadlyIsLeftOut(t *testing.T) {
	core := &fakeCore{policies: []corepolicies.Policy{
		{PolicyID: "", Status: "active", ValidFrom: from, ValidTo: to},
		{PolicyID: "AU-1", Status: "active", ValidFrom: to, ValidTo: from},
		{PolicyID: "AU-2", Status: "active", ValidFrom: from, ValidTo: from},
		{PolicyID: "AU-3", Status: "active", ValidFrom: from, ValidTo: to},
	}}

	got, err := policies.NewDirectory(core).HolderPolicies(context.Background(), "sub-carolina")

	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "AU-3", got[0].ID)
}

func TestAnyFailureOfTheCoreIsUnavailable(t *testing.T) {
	for _, err := range []error{corepolicies.ErrSourceUnavailable, corepolicies.ErrInvalidHolder, errors.New("boom")} {
		_, got := policies.NewDirectory(&fakeCore{err: err}).HolderPolicies(context.Background(), "sub-carolina")

		assert.ErrorIs(t, got, ports.ErrPolicyDirectoryUnavailable)
		assert.ErrorIs(t, got, err, "the cause is kept for the logs")
	}
}

func TestAHolderWithoutPoliciesHasAnEmptyList(t *testing.T) {
	got, err := policies.NewDirectory(&fakeCore{policies: []corepolicies.Policy{}}).
		HolderPolicies(context.Background(), "sub-nobody")

	require.NoError(t, err)
	assert.NotNil(t, got)
	assert.Empty(t, got)
}
