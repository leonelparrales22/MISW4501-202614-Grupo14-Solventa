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

type terminosFalsos struct {
	poliza     corepolicies.Policy
	titular    corepolicies.Holder
	errPoliza  error
	errTitular error
	pedida     [2]string
}

func (f *terminosFalsos) HolderPolicy(_ context.Context, holder, policy string) (corepolicies.Policy, error) {
	f.pedida = [2]string{holder, policy}
	return f.poliza, f.errPoliza
}

func (f *terminosFalsos) Holder(_ context.Context, _ string) (corepolicies.Holder, error) {
	return f.titular, f.errTitular
}

func TestPolicyTermsMapsTheCoverages(t *testing.T) {
	desde := time.Date(2025, 11, 1, 5, 0, 0, 0, time.UTC)
	f := &terminosFalsos{poliza: corepolicies.Policy{
		PolicyID: "HG-2025-880231", Line: "home", Product: "Hogar", Status: "active", ValidFrom: desde,
		ValidTo: desde.AddDate(1, 0, 0), CoveredIncidents: []string{"water_damage"},
		Coverages: []corepolicies.Coverage{{Incident: "water_damage", InsuredValueMinor: 32_000_000_000, Currency: "COP",
			DeductibleBasisPoints: 500}},
	}}

	got, err := policies.NewTerms(f).PolicyTerms(context.Background(), "sub-1", "HG-2025-880231")

	require.NoError(t, err)
	assert.Equal(t, [2]string{"sub-1", "HG-2025-880231"}, f.pedida)
	assert.Equal(t, "HG-2025-880231", got.Policy.ID)
	assert.Equal(t, domain.PolicyLine("home"), got.Policy.Line)
	assert.Equal(t, []domain.Coverage{{Incident: domain.IncidentWaterDamage, InsuredValueMinor: 32_000_000_000,
		Currency: "COP", DeductibleBasisPoints: 500}}, got.Coverages)
}

func TestHolderMapsTheProfile(t *testing.T) {
	f := &terminosFalsos{titular: corepolicies.Holder{FullName: "Carolina Pérez Ruiz", DocumentType: "CC",
		DocumentID: "1020345678", Email: "c@correo.example", Phone: "+57 301",
		PayoutAccount: corepolicies.PayoutAccount{BankCode: "1007", BankName: "Banco Aliado", AccountType: "savings",
			AccountNumber: "0123-4567-12"}}}

	got, err := policies.NewTerms(f).Holder(context.Background(), "sub-1")

	require.NoError(t, err)
	assert.Equal(t, domain.Holder{FullName: "Carolina Pérez Ruiz", DocumentType: "CC", DocumentID: "1020345678",
		Email: "c@correo.example", Phone: "+57 301", PayoutAccount: domain.PayoutAccount{BankCode: "1007",
			BankName: "Banco Aliado", AccountType: "savings", AccountNumber: "0123-4567-12"}}, got)
}

func TestTermsTellsNotFoundFromAFailure(t *testing.T) {
	ctx := context.Background()

	_, err := policies.NewTerms(&terminosFalsos{errPoliza: corepolicies.ErrPolicyNotFound}).PolicyTerms(ctx, "s", "p")
	assert.ErrorIs(t, err, ports.ErrPolicyNotFound)
	_, err = policies.NewTerms(&terminosFalsos{errPoliza: errors.New("se cayó")}).PolicyTerms(ctx, "s", "p")
	assert.ErrorIs(t, err, ports.ErrPolicyDirectoryUnavailable)

	_, err = policies.NewTerms(&terminosFalsos{errTitular: corepolicies.ErrHolderNotFound}).Holder(ctx, "s")
	assert.ErrorIs(t, err, ports.ErrHolderNotFound)
	_, err = policies.NewTerms(&terminosFalsos{errTitular: errors.New("se cayó")}).Holder(ctx, "s")
	assert.ErrorIs(t, err, ports.ErrPolicyDirectoryUnavailable)
}
