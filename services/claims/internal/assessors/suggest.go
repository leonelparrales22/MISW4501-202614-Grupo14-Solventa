package assessors

import (
	"context"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Suggest searches the providers of the specialty around the point through
// every radius of the policy, as the search of a claim does, without a claim
// and without recording it (decision D5). It is how the assistance of
// HU-W2.3-03 finds its suggested provider: the distance and the rules of
// eligibility are these, not another copy.
func Suggest(ctx context.Context, providers ports.ProviderReader, at domain.Coordinates, specialty domain.Specialty,
	policy domain.AssignmentPolicy, now time.Time) (domain.SearchResult, error) {
	list, err := providers.WithSpecialty(ctx, specialty)
	if err != nil {
		return domain.SearchResult{}, err
	}
	ladder, _ := policy.Ladder(policy.InitialRadiusKm())
	return domain.Search(at, specialty, list, ladder, now), nil
}
