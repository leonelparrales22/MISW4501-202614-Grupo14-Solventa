package kyc_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pkgsrc "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/kyc"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	adapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/kyc"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

type fakeFetcher struct {
	profile pkgsrc.Profile
	err     error
}

func (f fakeFetcher) FetchProfile(context.Context, string) (pkgsrc.Profile, error) {
	return f.profile, f.err
}

func TestFetchProfileMapsToDomain(t *testing.T) {
	at := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	src := adapter.NewSource(fakeFetcher{profile: pkgsrc.Profile{CustomerID: "other", Score: 0.2, Category: "preferred", UpdatedAt: at}})

	got, err := src.FetchProfile(context.Background(), "c1")

	require.NoError(t, err)
	assert.Equal(t, "c1", got.CustomerID)
	assert.Equal(t, 0.2, got.Score)
	assert.Equal(t, "preferred", got.Category)
	assert.Equal(t, at, got.CalculatedAt)
}

func TestFetchProfileMapsUnavailable(t *testing.T) {
	src := adapter.NewSource(fakeFetcher{err: pkgsrc.ErrSourceUnavailable})

	_, err := src.FetchProfile(context.Background(), "c1")

	assert.ErrorIs(t, err, ports.ErrSourceUnavailable)
}

func TestFetchProfileKeepsOtherErrors(t *testing.T) {
	boom := errors.New("boom")
	src := adapter.NewSource(fakeFetcher{err: boom})

	_, err := src.FetchProfile(context.Background(), "c1")

	require.ErrorIs(t, err, boom)
	assert.NotErrorIs(t, err, ports.ErrSourceUnavailable)
}

func TestFetchProfileMapsOpenCircuit(t *testing.T) {
	src := adapter.NewSource(fakeFetcher{err: fmt.Errorf("%w: %w", pkgsrc.ErrSourceUnavailable, resilience.ErrOpen)})

	_, err := src.FetchProfile(context.Background(), "c1")

	assert.ErrorIs(t, err, ports.ErrSourceUnavailable, "the source is reported unavailable")
	assert.ErrorIs(t, err, ports.ErrCircuitOpen, "HA-L01 reports circuit_open")
}

func TestFetchProfileUnavailableIsNotCircuitOpen(t *testing.T) {
	src := adapter.NewSource(fakeFetcher{err: pkgsrc.ErrSourceUnavailable})

	_, err := src.FetchProfile(context.Background(), "c1")

	assert.NotErrorIs(t, err, ports.ErrCircuitOpen)
}
