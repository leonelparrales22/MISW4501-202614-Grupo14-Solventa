package ports_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The version of a request of the inbox of assistance (HU-W2.3-03) is how
// many decisions it had when it was read: every write names it.

func TestTheVersionOfARequestIsHowManyDecisionsItHad(t *testing.T) {
	assert.Zero(t, ports.AssistanceRecord{}.Version(), "a request nobody attended")

	r := ports.AssistanceRecord{Case: domain.AssistanceCase{History: []domain.AssistanceAction{
		{Seq: 1, Code: domain.ActionAttended}, {Seq: 2, Code: domain.ActionNeedRecorded}}}}
	assert.Equal(t, 2, r.Version())
}
