package ports_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

func TestSourcesUnavailableErrorWrapsSourceUnavailable(t *testing.T) {
	outcomes := []domain.SourceOutcome{
		{Source: domain.ExternalSourceOpenFinance, Status: domain.SourceStatusTimeout, Latency: 120 * time.Millisecond},
		{Source: domain.ExternalSourceKYC, Status: domain.SourceStatusOK, Latency: 40 * time.Millisecond},
	}
	err := fmt.Errorf("profiling: %w", &ports.SourcesUnavailableError{Outcomes: outcomes})

	assert.ErrorIs(t, err, ports.ErrSourceUnavailable, "the HA-D01 degradation keeps matching")
	var unavailable *ports.SourcesUnavailableError
	require.ErrorAs(t, err, &unavailable)
	assert.Equal(t, outcomes, unavailable.Outcomes)
	assert.Contains(t, err.Error(), "open_finance=timeout, kyc=ok")
}

func TestErrCircuitOpenIsDistinct(t *testing.T) {
	err := fmt.Errorf("%w: %w", ports.ErrSourceUnavailable, ports.ErrCircuitOpen)

	assert.ErrorIs(t, err, ports.ErrSourceUnavailable)
	assert.ErrorIs(t, err, ports.ErrCircuitOpen)
	assert.False(t, errors.Is(ports.ErrSourceUnavailable, ports.ErrCircuitOpen))
}
