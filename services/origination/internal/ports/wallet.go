package ports

import (
	"context"
	"errors"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// ErrPolicyPortfolioUnavailable is returned by a PolicyPortfolio whose source
// did not give a usable answer (HU-M1.2-01).
var ErrPolicyPortfolioUnavailable = errors.New("the policy core is unavailable")

// PolicyPortfolio is the policy core as the wallet reads it (HU-M1.2-01): the
// policies of a holder and who the holder is. The holder id is the subject of
// the token.
type PolicyPortfolio interface {
	HolderPolicies(ctx context.Context, holderID string) ([]domain.WalletPolicy, error)
	Holder(ctx context.Context, holderID string) (domain.PolicyHolder, error)
}
