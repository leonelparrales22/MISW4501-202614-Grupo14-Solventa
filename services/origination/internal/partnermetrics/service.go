// Package partnermetrics is the summary of the integration of a commercial
// partner on its dashboard (HU-W3.2-01): the quotes it requested through the
// embedded channel, the policies issued from them and the conversion, in a
// range of civil days of Colombia.
//
// The data is origination's own (offers, acceptances and policies), so the
// summary is read where it lives instead of copying it to partners (team
// decision of HU-W3.2-01).
package partnermetrics

import (
	"context"
	"errors"
	"strings"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// ErrNoPartner means the caller carries no partner: its data can never be
// told apart from another partner's, so nothing is shown (CA-4).
var ErrNoPartner = errors.New("the caller is not bound to a partner")

// Service reads the summary of a partner.
type Service struct {
	activity ports.PartnerActivityQuery
}

// NewService builds the service over the read model of the activity.
func NewService(activity ports.PartnerActivityQuery) *Service { return &Service{activity: activity} }

// Summary returns the summary of partnerID in the range q. partnerID must
// come from the verified token, never from the request (CA-4). A range with
// no activity is a summary of zeros, not an error (CA-3).
func (s *Service) Summary(ctx context.Context, partnerID string, q domain.PartnerMetricsQuery) (domain.PartnerSummary, error) {
	if strings.TrimSpace(partnerID) == "" {
		return domain.PartnerSummary{}, ErrNoPartner
	}
	if err := q.Validate(); err != nil {
		return domain.PartnerSummary{}, err
	}
	start, end := q.Interval()
	a, err := s.activity.CountPartnerActivity(ctx, partnerID, start, end)
	if err != nil {
		return domain.PartnerSummary{}, err
	}
	return domain.NewPartnerSummary(partnerID, q, a), nil
}
