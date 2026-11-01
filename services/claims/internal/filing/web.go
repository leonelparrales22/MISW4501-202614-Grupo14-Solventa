package filing

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Reasons a policy does not take a claim, as the API names them (HU-W2.1-01
// criterion 3).
const (
	ReasonPolicyNotFound          = "policy_not_found"
	ReasonPolicyNotActive         = "policy_not_active"
	ReasonIncidentNotCovered      = "incident_not_covered"
	ReasonOccurredOutsideValidity = "occurred_outside_validity"
)

// PolicyNotEligibleError says why the policy does not take the claim. Policy is
// nil when the policy is not one of the insured's: a policy of someone else is
// reported as one that does not exist.
type PolicyNotEligibleError struct {
	Reason string
	Policy *domain.Policy
}

func (e *PolicyNotEligibleError) Error() string {
	return "claims: the policy does not take the claim: " + e.Reason
}

// WebService files a claim with its supports from the web portal and tells the
// portal what can be filed (HU-W2.1-01). It shares the claims, the filing
// numbers and the filing keys with the mobile filing (Service).
type WebService struct {
	claims   ports.ClaimRecords
	supports ports.SupportStore
	policies ports.PolicyDirectory
	clock    ports.Clock
	catalog  domain.Catalog
	newID    func() (string, error)
}

// NewWebService builds the service over catalog claim-catalog-v1.
func NewWebService(claims ports.ClaimRecords, supports ports.SupportStore, policies ports.PolicyDirectory,
	clock ports.Clock) *WebService {
	return &WebService{claims: claims, supports: supports, policies: policies, clock: clock,
		catalog: domain.CatalogV1(), newID: newID}
}

// PolicyOptions is a policy the insured can file on and its claim types.
type PolicyOptions struct {
	Policy    domain.Policy
	Incidents []domain.IncidentOption
}

// FilingOptions is what the filing form offers (criterion 1).
type FilingOptions struct {
	CatalogVersion string
	Policies       []PolicyOptions
}

// Options returns the insured's policies that are active now and have claim
// types that are filed, each with the types it covers and their supports.
func (s *WebService) Options(ctx context.Context, subject string) (FilingOptions, error) {
	policies, err := s.policies.HolderPolicies(ctx, subject)
	if err != nil {
		return FilingOptions{}, err
	}
	now := s.clock.Now().UTC()
	out := FilingOptions{CatalogVersion: s.catalog.Version, Policies: []PolicyOptions{}}
	for _, p := range policies {
		if !p.ActiveAt(now) {
			continue
		}
		if incidents := s.catalog.OptionsFor(p); len(incidents) > 0 {
			out.Policies = append(out.Policies, PolicyOptions{Policy: p, Incidents: incidents})
		}
	}
	return out, nil
}

// File validates the form and the supports, checks the policy with the policy
// core and stores the claim with its supports. created tells whether this call
// created it: a retry with the same key and the same filing returns the same
// claim with false, even if the policy core is down by then.
//
// Errors: *domain.ValidationError (criteria 2 and 5), *PolicyNotEligibleError
// (criteria 3 and 6), ports.ErrPolicyDirectoryUnavailable and
// ports.ErrFilingKeyReused.
func (s *WebService) File(ctx context.Context, subject, key string, r domain.WebFilingRequest) (domain.ClaimRecord, bool, error) {
	now := s.clock.Now().UTC()

	uploaded, err := s.supports.Find(ctx, supportIDs(r))
	if err != nil {
		return domain.ClaimRecord{}, false, fmt.Errorf("read the supports: %w", err)
	}
	c, err := r.Validate(now, s.catalog, subject, uploaded)
	if err != nil {
		return domain.ClaimRecord{}, false, err
	}

	// A retry finds its claim before asking the policy core again.
	existing, err := s.claims.FindByKey(ctx, subject, key)
	switch {
	case err == nil:
		return sameFiling(existing, c)
	case !errors.Is(err, ports.ErrClaimNotFound):
		return domain.ClaimRecord{}, false, fmt.Errorf("look up the filing key: %w", err)
	}

	policy, err := s.eligiblePolicy(ctx, subject, c, now)
	if err != nil {
		return domain.ClaimRecord{}, false, err
	}

	c.Subject = subject
	c.FilingKey = key
	c.FiledAt = now.Truncate(time.Microsecond)
	c.Policy = &policy
	if c.ID, err = s.newID(); err != nil {
		return domain.ClaimRecord{}, false, fmt.Errorf("could not generate the claim id: %w", err)
	}

	stored, created, err := s.claims.CreateWeb(ctx, c)
	if errors.Is(err, ports.ErrSupportAlreadyAttached) {
		// Another filing took one of the supports in the meantime.
		return domain.ClaimRecord{}, false, &domain.ValidationError{
			Fields: []domain.FieldError{{Field: "supports", Code: domain.CodeNotFound}},
		}
	}
	if err != nil {
		return domain.ClaimRecord{}, false, err
	}
	return stored, created, nil
}

// eligiblePolicy finds the policy among the subject's own and checks that it
// takes the claim.
func (s *WebService) eligiblePolicy(ctx context.Context, subject string, c domain.ClaimRecord,
	now time.Time) (domain.Policy, error) {
	policies, err := s.policies.HolderPolicies(ctx, subject)
	if err != nil {
		return domain.Policy{}, err
	}
	for _, p := range policies {
		if p.ID != c.PolicyID {
			continue
		}
		p = p.Copy()
		switch err := p.EligibleFor(s.catalog, c.IncidentType, c.OccurredAt, now); {
		case errors.Is(err, domain.ErrPolicyNotActive):
			return domain.Policy{}, &PolicyNotEligibleError{Reason: ReasonPolicyNotActive, Policy: &p}
		case errors.Is(err, domain.ErrIncidentNotCovered):
			return domain.Policy{}, &PolicyNotEligibleError{Reason: ReasonIncidentNotCovered, Policy: &p}
		case errors.Is(err, domain.ErrOccurredOutsideValidity):
			return domain.Policy{}, &PolicyNotEligibleError{Reason: ReasonOccurredOutsideValidity, Policy: &p}
		case err != nil:
			return domain.Policy{}, err
		}
		return p, nil
	}
	return domain.Policy{}, &PolicyNotEligibleError{Reason: ReasonPolicyNotFound}
}

// sameFiling answers a retry: the stored claim when the filing is the same,
// ports.ErrFilingKeyReused when the key came with another one.
func sameFiling(stored, candidate domain.ClaimRecord) (domain.ClaimRecord, bool, error) {
	if stored.Channel != domain.ChannelWeb || stored.WebFingerprint() != candidate.WebFingerprint() {
		return domain.ClaimRecord{}, false, ports.ErrFilingKeyReused
	}
	return stored, false, nil
}

// supportIDs lists the distinct ids of the request that have the shape of a
// support id: nothing else is worth reading.
func supportIDs(r domain.WebFilingRequest) []string {
	seen := map[string]bool{}
	var ids []string
	for _, g := range r.Supports {
		for _, id := range g.SupportIDs {
			id = strings.TrimSpace(id)
			if domain.ValidSupportID(id) && !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}
