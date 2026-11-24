package assistance

import (
	"context"
	"errors"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assessors"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// Limits of the inbox (assumption S11).
const (
	DefaultInboxLimit = 50
	MaxInboxLimit     = 100
	// writeAttempts is how many times a decision is tried when another
	// analyst decided on the same request meanwhile.
	writeAttempts = 3
)

// Inbox is the inbox of operations: the requests with its indicators.
type Inbox struct {
	Summary domain.InboxSummary
	Items   []ports.AssistanceRecord
}

// CaseView is a request with the suggestion of provider of this moment.
type CaseView struct {
	ports.AssistanceRecord
	Suggestion domain.AssistanceSuggestion
}

// InboxService is the inbox of operations of HU-W2.3-03: it lists the
// requests for assistance, reads one with its suggestion, and records each
// decision of the analyst (attend, need, location by voice, dispatch and
// close) with who made it and when. The suggestion is the search of
// HU-W2.3-01 (assessors.Suggest, decision D5); this package computes no
// distance.
type InboxService struct {
	store      ports.AssistanceInboxStore
	providers  ports.ProviderDirectory
	clock      ports.Clock
	policy     domain.AssistanceInboxPolicy
	assignment domain.AssignmentPolicy
	wake       func()
}

// NewInboxService builds the service. wake is called after a dispatch, so the
// worker sends its notice at once; nil does nothing.
func NewInboxService(store ports.AssistanceInboxStore, providers ports.ProviderDirectory, clock ports.Clock,
	wake func()) *InboxService {
	if wake == nil {
		wake = func() {}
	}
	return &InboxService{store: store, providers: providers, clock: clock, policy: domain.AssistanceInboxPolicyV1(),
		assignment: domain.AssignmentPolicyV1(), wake: wake}
}

// Policy returns the assistance policy and the assignment policy whose radii
// the suggestion uses.
func (s *InboxService) Policy() (domain.AssistanceInboxPolicy, domain.AssignmentPolicy) {
	return domain.AssistanceInboxPolicyV1(), domain.AssignmentPolicyV1()
}

// Inbox returns the requests of the filter, the latest first, and the
// indicators of the inbox, which do not depend on the filter (criterion 1).
func (s *InboxService) Inbox(ctx context.Context, f ports.InboxFilter) (Inbox, error) {
	if f.Limit <= 0 {
		f.Limit = DefaultInboxLimit
	}
	f.Limit = min(f.Limit, MaxInboxLimit)
	items, err := s.store.List(ctx, f)
	if err != nil {
		return Inbox{}, err
	}
	newCount, inCall, dispatches, err := s.store.Counts(ctx, domain.ColombiaDayStart(s.clock.Now()))
	if err != nil {
		return Inbox{}, err
	}
	return Inbox{Summary: domain.NewInboxSummary(newCount, inCall, dispatches, s.policy), Items: items}, nil
}

// Case returns the request with the suggestion of this moment (criterion 2,
// decision D8). Reading it does not change it.
func (s *InboxService) Case(ctx context.Context, id string) (CaseView, error) {
	r, err := s.store.Case(ctx, id)
	if err != nil {
		return CaseView{}, err
	}
	return s.view(ctx, r)
}

// view completes the record with its suggestion.
func (s *InboxService) view(ctx context.Context, r ports.AssistanceRecord) (CaseView, error) {
	specialty, loc, state := r.Case.Searchable()
	if state != "" {
		return CaseView{AssistanceRecord: r, Suggestion: domain.NoSuggestion(state, s.assignment.Version)}, nil
	}
	result, err := assessors.Suggest(ctx, s.providers, loc.Point, specialty, s.assignment, s.clock.Now())
	if err != nil {
		return CaseView{}, err
	}
	ids := make([]string, 0, len(result.Candidates))
	for _, c := range result.Candidates {
		ids = append(ids, c.ProviderID)
	}
	today, err := s.store.DispatchesSince(ctx, ids, domain.ColombiaDayStart(s.clock.Now()))
	if err != nil {
		return CaseView{}, err
	}
	return CaseView{AssistanceRecord: r,
		Suggestion: domain.NewSuggestion(specialty, loc, result, s.policy, s.assignment.Version, today)}, nil
}

// decide reads the request, applies the decision and writes it. When another
// analyst decided on it meanwhile, it reads it again and decides once more:
// the rules of the domain then answer the new status (decision D6).
func (s *InboxService) decide(ctx context.Context, id string,
	apply func(r ports.AssistanceRecord, now time.Time) error) (CaseView, error) {
	for attempt := 1; ; attempt++ {
		r, err := s.store.Case(ctx, id)
		if err != nil {
			return CaseView{}, err
		}
		// The database keeps microseconds; so does the answer.
		err = apply(r, s.clock.Now().UTC().Truncate(time.Microsecond))
		if errors.Is(err, ports.ErrAssistanceChanged) && attempt < writeAttempts {
			continue
		}
		if err != nil {
			return CaseView{}, err
		}
		return s.Case(ctx, id)
	}
}

// Attend is the analyst attending the request: "Atender" on a new one,
// "Continuar" on one in call (assumption S2).
func (s *InboxService) Attend(ctx context.Context, id string, by domain.Analyst) (CaseView, error) {
	return s.decide(ctx, id, func(r ports.AssistanceRecord, now time.Time) error {
		action, changed, err := domain.Attend(r.Case, by, now)
		if err != nil || !changed {
			return err
		}
		return s.store.Attend(ctx, id, r.Version(), action)
	})
}

// RecordNeed records what the insured needs (decision D1, assumption S4).
func (s *InboxService) RecordNeed(ctx context.Context, id, need string, by domain.Analyst) (CaseView, error) {
	return s.decide(ctx, id, func(r ports.AssistanceRecord, now time.Time) error {
		n, action, err := domain.RecordNeed(r.Case, s.policy, need, by, now)
		if err != nil {
			return err
		}
		return s.store.RecordNeed(ctx, id, r.Version(), n, action)
	})
}

// RecordVoiceLocation records the address given by voice and the pin of the
// map (decision D3, assumption S5).
func (s *InboxService) RecordVoiceLocation(ctx context.Context, id string, in domain.VoiceLocationInput,
	by domain.Analyst) (CaseView, error) {
	return s.decide(ctx, id, func(r ports.AssistanceRecord, now time.Time) error {
		v, action, err := domain.RecordVoiceLocation(r.Case, in, by, now)
		if err != nil {
			return err
		}
		return s.store.RecordVoiceLocation(ctx, id, r.Version(), v, action)
	})
}

// Dispatch sends the provider to the request (criterion 3, assumption S9).
// The search is made again now: the provider must be eligible at this
// moment. The notice is left due in the same write, and the worker is woken.
func (s *InboxService) Dispatch(ctx context.Context, id, providerID string, by domain.Analyst) (CaseView, error) {
	view, err := s.decide(ctx, id, func(r ports.AssistanceRecord, now time.Time) error {
		var result domain.SearchResult
		specialty, loc, state := r.Case.Searchable()
		if state == "" {
			var err error
			result, err = assessors.Suggest(ctx, s.providers, loc.Point, specialty, s.assignment, now)
			if err != nil {
				return err
			}
		}
		current, err := s.currentProvider(ctx, providerID)
		if err != nil {
			return err
		}
		d, action, err := domain.Dispatch(r.Case, s.policy, s.assignment.Version, result, current, by, now)
		if err != nil {
			return err
		}
		subject, body := DispatchNotice(r.Case.Request, d)
		notice := ports.AssistanceNotice{RequestID: id, To: d.Provider.ContactEmail, Subject: subject, Body: body,
			Status: ports.NoticePending, NextAttemptAt: now}
		return s.store.Dispatch(ctx, id, r.Version(), d, action, notice)
	})
	if err == nil {
		s.wake()
	}
	return view, err
}

// currentProvider is the provider as the registry has it now; nil when it
// does not exist.
func (s *InboxService) currentProvider(ctx context.Context, id string) (*domain.Provider, error) {
	p, err := s.providers.Get(ctx, id)
	if errors.Is(err, ports.ErrProviderNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// Close closes the request without a dispatch, with its reason (criterion 4,
// assumption S10).
func (s *InboxService) Close(ctx context.Context, id string, in domain.CloseInput, by domain.Analyst) (CaseView, error) {
	return s.decide(ctx, id, func(r ports.AssistanceRecord, now time.Time) error {
		c, action, err := domain.Close(r.Case, s.policy, in, by, now)
		if err != nil {
			return err
		}
		return s.store.Close(ctx, id, r.Version(), c, action)
	})
}
