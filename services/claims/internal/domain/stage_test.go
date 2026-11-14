package domain_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// HU-M3.3-01: which changes of stage the insured is told about.

func TestTheStagesTheInsuredWaitsForNotify(t *testing.T) {
	for name, tc := range map[string]struct {
		channel        domain.Channel
		status, before domain.Status
		want           bool
	}{
		"in evaluation":                       {domain.ChannelWeb, domain.StatusInEvaluation, domain.StatusActive, true},
		"approved from evaluation":            {domain.ChannelMobile, domain.StatusApproved, domain.StatusInEvaluation, true},
		"paid":                                {domain.ChannelWeb, domain.StatusPaid, domain.StatusApproved, true},
		"approved again after a payout retry": {domain.ChannelWeb, domain.StatusApproved, domain.StatusPaymentException, false},
		"filed":                               {domain.ChannelWeb, domain.StatusActive, "", false},
		"returned":                            {domain.ChannelWeb, domain.StatusReturned, domain.StatusActive, false},
		"rejected":                            {domain.ChannelWeb, domain.StatusRejected, domain.StatusInEvaluation, false},
		"payment exception":                   {domain.ChannelWeb, domain.StatusPaymentException, domain.StatusApproved, false},
		"a parametric claim":                  {domain.ChannelParametric, domain.StatusPaid, domain.StatusApproved, false},
	} {
		t.Run(name, func(t *testing.T) {
			c := domain.StageChange{Channel: tc.channel, Status: tc.status, PreviousStatus: tc.before}
			assert.Equal(t, tc.want, c.Notifies())
		})
	}
}
