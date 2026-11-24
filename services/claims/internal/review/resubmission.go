package review

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// WithResubmissions lets the insured correct a returned filing and send it
// again (HU-W2.1-04). supports reads the new files the insured uploaded.
func (s *Service) WithResubmissions(store ports.ResubmissionStore, supports ports.SupportStore) *Service {
	s.resubmissions, s.supports = store, supports
	return s
}

// Resubmit corrects a returned filing of the subject with what the request
// sends and makes it active for a new review of the advisor, with the same
// filing number. created is false when the request is a retry of a
// resubmission that already went through: it answers with that one.
//
// Errors: ErrNotOwner (the claim is not the subject's), ports.ErrClaimNotFound,
// domain.ErrNotReturned, domain.ErrStaleReturn and *domain.ValidationError
// with everything still missing.
func (s *Service) Resubmit(ctx context.Context, claimID, subject string,
	in domain.ResubmissionRequest) (domain.Resubmission, bool, error) {
	if s.resubmissions == nil {
		return domain.Resubmission{}, false, fmt.Errorf("claims: the resubmission is not configured")
	}
	c, err := s.claims.Get(ctx, claimID)
	if err != nil {
		return domain.Resubmission{}, false, err
	}
	if c.Subject != subject {
		return domain.Resubmission{}, false, ErrNotOwner
	}
	if c.Status != domain.StatusReturned {
		return s.alreadyResubmitted(ctx, c, subject, in.ReviewSeq)
	}
	reviews, err := s.store.Reviews(ctx, claimID)
	if err != nil {
		return domain.Resubmission{}, false, fmt.Errorf("read the reviews of the claim: %w", err)
	}
	if len(reviews) == 0 {
		return domain.Resubmission{}, false, domain.ErrNotReturned
	}
	uploaded, err := s.supports.Find(ctx, newSupportIDs(c, in))
	if err != nil {
		return domain.Resubmission{}, false, fmt.Errorf("read the supports: %w", err)
	}
	r, err := domain.Resubmit(c, reviews[len(reviews)-1], subject, in, uploaded, s.clock.Now())
	if err != nil {
		return domain.Resubmission{}, false, err
	}
	stored, err := s.resubmissions.Resubmit(ctx, r)
	switch {
	case errors.Is(err, ports.ErrSupportAlreadyAttached):
		// Another filing took one of the new supports in the meantime.
		return domain.Resubmission{}, false, &domain.ValidationError{
			Fields: []domain.FieldError{{Field: "supports", Code: domain.CodeNotFound}},
		}
	case errors.Is(err, domain.ErrNotReturned):
		// Another resubmission of the same insured won the race.
		if c, err = s.claims.Get(ctx, claimID); err != nil {
			return domain.Resubmission{}, false, err
		}
		return s.alreadyResubmitted(ctx, c, subject, in.ReviewSeq)
	case err != nil:
		return domain.Resubmission{}, false, err
	}
	return stored, true, nil
}

// alreadyResubmitted answers a resubmission of a claim that is not returned:
// the one of the subject that answered the same return when there is one,
// domain.ErrNotReturned otherwise. It carries the claim as it is now.
func (s *Service) alreadyResubmitted(ctx context.Context, c domain.ClaimRecord, subject string,
	reviewSeq int) (domain.Resubmission, bool, error) {
	done, err := s.resubmissions.Resubmissions(ctx, c.ID)
	if err != nil {
		return domain.Resubmission{}, false, fmt.Errorf("read the resubmissions of the claim: %w", err)
	}
	for _, r := range done {
		if r.ReviewSeq == reviewSeq && r.ResubmittedBy == subject {
			r.Claim = c
			return r, false, nil
		}
	}
	return domain.Resubmission{}, false, domain.ErrNotReturned
}

// newSupportIDs lists the ids of the request that are not attached to the
// claim and have the shape of a support id: nothing else is worth reading.
func newSupportIDs(c domain.ClaimRecord, in domain.ResubmissionRequest) []string {
	attached := map[string]bool{}
	for _, a := range c.Supports {
		attached[a.File.ID] = true
	}
	seen := map[string]bool{}
	var ids []string
	for _, g := range in.Supports {
		for _, id := range g.SupportIDs {
			id = strings.TrimSpace(id)
			if attached[id] || seen[id] || !domain.ValidSupportID(id) {
				continue
			}
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}
