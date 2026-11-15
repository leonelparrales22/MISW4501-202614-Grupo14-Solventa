package ports

import "context"

// OpenClaimsReader counts the claims in progress of a policy (HU-W1.4-02): the
// ones that are not closed (domain.Closed), whoever filed them. Origination
// asks it, inside the VPC, before an operations analyst applies an
// endorsement that a claim in progress blocks.
type OpenClaimsReader interface {
	OpenClaims(ctx context.Context, policyID string) (int, error)
}
