package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

func TestBaseActuarialProfileIsNeutralAndUndated(t *testing.T) {
	p := domain.BaseActuarialProfile("c1")

	assert.Equal(t, "c1", p.CustomerID)
	assert.Equal(t, 0.5, p.Score)
	assert.Equal(t, domain.BaseCategory, p.Category)
	assert.True(t, p.CalculatedAt.IsZero())
}
