// Package assessors is the assignment of the nearest provider to a claim
// (HU-W2.3-01): where the incident happened, the providers of the specialty
// the claim needs around it, the radius widened until one is available, the
// analyst confirming one and the e-mail that gives it the location. It owns
// the registry of providers and the distance (decision #11); the assistance
// of HU-W2.3-03 searches with Suggest. The status of the claim does not
// change: the assignment has its own (decision D2).
package assessors

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// maxConflicts bounds how many times a write that lost its place to another
// one is made again.
const maxConflicts = 3

// Service locates, searches and assigns the providers of the claims.
type Service struct {
	store     ports.AssignmentStore
	providers ports.ProviderStore
	clock     ports.Clock
	policy    domain.AssignmentPolicy
	// wake, when set, is called after an assignment so the worker sends the
	// notice at once instead of on its next poll.
	wake func()
}

// NewService builds the service with assignment-policy-v1. wake may be nil.
func NewService(store ports.AssignmentStore, providers ports.ProviderStore, clock ports.Clock, wake func()) *Service {
	if wake == nil {
		wake = func() {}
	}
	return &Service{store: store, providers: providers, clock: clock, policy: domain.AssignmentPolicyV1(), wake: wake}
}

// Policy returns the assignment policy in force, a copy.
func (s *Service) Policy() domain.AssignmentPolicy { return domain.AssignmentPolicyV1() }

// View is where the assignment of a claim stands, with everything the screen
// "Asignar prestador" shows.
type View struct {
	Claim     domain.ClaimRecord
	State     domain.AssignmentState
	Line      domain.PolicyLine
	Specialty domain.Specialty
	// PolicyVersion is the version of the policy in force.
	PolicyVersion string
	// Location is the one the claim is searched from; nil when it has none.
	Location   *domain.IncidentLocation
	LastSearch *domain.ProviderSearch
	Assignment *domain.Assignment
	Notice     *ports.ProviderNotice
}

func (s *Service) viewOf(ctx context.Context, k ports.AssignmentCase) View {
	line, specialty, known := s.policy.SpecialtyFor(k.Claim)
	if !known {
		slog.WarnContext(ctx, "the assignment policy does not know the claim type: it needs no on-site visit",
			"claim_id", k.Claim.ID, "incident_type", k.Claim.IncidentType, "policy_version", s.policy.Version)
	}
	location := domain.LocationOf(k.Claim, k.Location)
	return View{
		Claim: k.Claim, Line: line, Specialty: specialty, PolicyVersion: s.policy.Version, Location: location,
		LastSearch: k.LastSearch, Assignment: k.Assignment, Notice: k.Notice,
		State: domain.AssignmentStateOf(k.Claim, s.policy, location, k.LastSearch, k.Assignment),
	}
}

// View returns the assignment of a claim, or ports.ErrClaimNotFound.
func (s *Service) View(ctx context.Context, claimID string) (View, error) {
	k, err := s.store.Case(ctx, claimID)
	if err != nil {
		return View{}, err
	}
	return s.viewOf(ctx, k), nil
}

// Summary is the assignment of a claim in a list of claims.
type Summary struct {
	ClaimID      string
	State        domain.AssignmentState
	Specialty    domain.Specialty
	ProviderName string
	// DistanceKm is the distance of the assigned provider; nil when none.
	DistanceKm *float64
	AssignedAt time.Time
	// Reason is why the claim is pending assignment; empty otherwise.
	Reason string
}

// Summaries returns the assignment of each claim, in the order of the ids;
// an id that does not exist is not_found.
func (s *Service) Summaries(ctx context.Context, ids []string) ([]Summary, error) {
	cases, err := s.store.Cases(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(ids))
	for _, id := range ids {
		k, ok := cases[id]
		if !ok {
			out = append(out, Summary{ClaimID: id, State: domain.AssignmentNotFound})
			continue
		}
		v := s.viewOf(ctx, k)
		sum := Summary{ClaimID: id, State: v.State, Specialty: v.Specialty}
		switch {
		case v.Assignment != nil:
			distance := v.Assignment.DistanceKm
			sum.ProviderName, sum.DistanceKm, sum.AssignedAt = v.Assignment.ProviderName, &distance, v.Assignment.AssignedAt
		case v.State == domain.AssignmentPending:
			sum.Reason = v.LastSearch.Result.Reason
		}
		out = append(out, sum)
	}
	return out, nil
}

// RecordLocation records the address and the pin of the incident of a claim
// with no coordinates from the app (criterion 1).
func (s *Service) RecordLocation(ctx context.Context, claimID string, in domain.LocationRequest,
	by domain.Analyst) (View, error) {
	for attempt := 1; ; attempt++ {
		k, err := s.store.Case(ctx, claimID)
		if err != nil {
			return View{}, err
		}
		l, err := domain.RecordLocation(k.Claim, s.policy, k.Location, k.Assignment, in, by, s.clock.Now())
		if err != nil {
			return View{}, err
		}
		err = s.store.RecordLocation(ctx, claimID, l)
		if errors.Is(err, ports.ErrLocationConflict) && attempt < maxConflicts {
			continue // another analyst recorded one first: this one goes after it
		}
		if err != nil {
			return View{}, err
		}
		return s.View(ctx, claimID)
	}
}

// Search searches the provider of a claim from a radius of the policy (nil:
// the initial one) and records it (criteria 2 to 4 and 6). When another
// search of the claim was recorded at the same time, the view shows that one.
func (s *Service) Search(ctx context.Context, claimID string, fromRadiusKm *float64, by domain.Analyst) (View, error) {
	k, err := s.store.Case(ctx, claimID)
	if err != nil {
		return View{}, err
	}
	if err := s.search(ctx, k, domain.SearchRequest{FromRadiusKm: fromRadiusKm, Trigger: domain.SearchByAnalyst,
		By: &by}); err != nil {
		return View{}, err
	}
	return s.View(ctx, claimID)
}

// Retry searches again, from the initial radius, the provider of a claim
// pending assignment (criterion 6, decision D4), and returns where its
// assignment stands. It never assigns: when it finds a provider the claim is
// proposed and the analyst confirms. A claim that is not pending any more is
// left as it is.
func (s *Service) Retry(ctx context.Context, claimID string) (domain.AssignmentState, error) {
	k, err := s.store.Case(ctx, claimID)
	if err != nil {
		return "", err
	}
	if state := s.viewOf(ctx, k).State; state != domain.AssignmentPending {
		return state, nil
	}
	if err := s.search(ctx, k, domain.SearchRequest{Trigger: domain.SearchByRetry}); err != nil {
		return "", err
	}
	v, err := s.View(ctx, claimID)
	if err != nil {
		return "", err
	}
	return v.State, nil
}

// search records a search of the claim. Losing the place to another search
// is not an error: that search is the last one of the claim.
func (s *Service) search(ctx context.Context, k ports.AssignmentCase, in domain.SearchRequest) error {
	location := domain.LocationOf(k.Claim, k.Location)
	var providers []domain.Provider
	if _, specialty, _ := s.policy.SpecialtyFor(k.Claim); specialty != "" && location != nil {
		var err error
		if providers, err = s.providers.WithSpecialty(ctx, specialty); err != nil {
			return err
		}
	}
	search, err := domain.NewSearch(k.Claim, s.policy, location, k.LastSearch, k.Assignment, providers, in,
		s.clock.Now())
	if err != nil {
		return err
	}
	err = s.store.RecordSearch(ctx, search)
	if errors.Is(err, ports.ErrSearchConflict) {
		return nil
	}
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "provider search recorded", "claim_id", search.ClaimID, "seq", search.Seq,
		"trigger", search.Trigger, "outcome", search.Result.Outcome, "radius_km", search.Result.RadiusKm)
	return nil
}

// Assign confirms a provider of the last search of a claim (criterion 5):
// the provider becomes busy with the claim and the e-mail with the location
// is left due. The worker sends it.
func (s *Service) Assign(ctx context.Context, claimID, providerID string, searchSeq int, by domain.Analyst) (View, error) {
	k, err := s.store.Case(ctx, claimID)
	if err != nil {
		return View{}, err
	}
	var current *domain.Provider
	switch p, err := s.providers.Get(ctx, providerID); {
	case err == nil:
		current = &p
	case !errors.Is(err, ports.ErrProviderNotFound):
		return View{}, err
	}
	a, err := domain.Assign(k.Claim, s.policy, domain.LocationOf(k.Claim, k.Location), k.LastSearch, k.Assignment,
		providerID, searchSeq, current, by, s.clock.Now())
	if err != nil {
		return View{}, err
	}
	subject, body := ProviderNotice(k.Claim, a)
	notice := ports.ProviderNotice{ClaimID: a.ClaimID, To: a.ContactEmail, Subject: subject, Body: body,
		Status: ports.NoticePending, NextAttemptAt: a.AssignedAt}
	if err := s.store.Assign(ctx, a, notice); err != nil {
		return View{}, err
	}
	slog.InfoContext(ctx, "provider assigned", "claim_id", a.ClaimID, "provider_id", a.ProviderID,
		"search_seq", a.SearchSeq)
	s.wake()
	return s.View(ctx, claimID)
}
