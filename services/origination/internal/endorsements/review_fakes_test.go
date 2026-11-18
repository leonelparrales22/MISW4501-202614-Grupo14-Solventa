package endorsements_test

import (
	"context"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

// fakeStore is the store of the endorsements over the fake requests, with the
// claims of the PostgreSQL store: a decision wins only from the statuses it
// needs, and an endorsement takes the version that follows its base.
type fakeStore struct {
	mu           sync.Mutex
	requests     *fakeRequests
	endorsements []domain.Endorsement
	attempts     []domain.EndorsementAttempt
	actions      []domain.RequestAction
	counter      int
	err          error
	// beforeApply runs inside Apply, before the claim: a test makes another
	// analyst win there.
	beforeApply func()
}

func newFakeStore(requests *fakeRequests) *fakeStore { return &fakeStore{requests: requests} }

func (f *fakeStore) Inbox(_ context.Context, filter domain.EndorsementInboxFilter) (domain.EndorsementInboxPage, error) {
	if f.err != nil {
		return domain.EndorsementInboxPage{}, f.err
	}
	out := domain.EndorsementInboxPage{Items: []domain.EndorsementInboxItem{}, Page: filter.Page, PageSize: filter.PageSize}
	f.requests.mu.Lock()
	defer f.requests.mu.Unlock()
	for _, r := range f.requests.rows {
		if filter.Matches(r) {
			out.Items = append(out.Items, domain.EndorsementInboxItem{Request: r})
		}
	}
	out.Total = len(out.Items)
	return out, nil
}

func (f *fakeStore) latest(policyID string) *domain.Endorsement {
	var last *domain.Endorsement
	for i := range f.endorsements {
		if e := &f.endorsements[i]; e.PolicyID == policyID && (last == nil || e.Version > last.Version) {
			last = e
		}
	}
	return last
}

func (f *fakeStore) LatestTerms(_ context.Context, policyID string) (domain.LatestTerms, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return domain.LatestTerms{}, f.err
	}
	e := f.latest(policyID)
	if e == nil {
		return domain.LatestTerms{}, nil
	}
	return domain.LatestTerms{Found: true, Own: true, Version: e.Version, Terms: e.Terms, EndorsementID: e.ID, Type: e.Type, At: e.At}, nil
}

func (f *fakeStore) Record(_ context.Context, requestID string) (domain.RequestRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out domain.RequestRecord
	for i := range f.endorsements {
		if f.endorsements[i].RequestID == requestID {
			e := f.endorsements[i]
			out.Endorsement = &e
		}
	}
	for i := range f.attempts {
		if f.attempts[i].RequestID == requestID {
			a := f.attempts[i]
			out.LastAttempt = &a
		}
	}
	for _, a := range f.actions {
		if a.RequestID == requestID {
			out.Actions = append(out.Actions, a)
		}
	}
	return out, nil
}

func (f *fakeStore) Endorsements(_ context.Context, policyID string) (ports.PolicyEndorsements, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := ports.PolicyEndorsements{}
	for _, e := range f.endorsements {
		if e.PolicyID == policyID {
			out.Count++
		}
	}
	if last := f.latest(policyID); last != nil {
		e := *last
		out.Last = &e
	}
	return out, nil
}

// claim moves the request when its status is one of from (and it has no
// decision reason), as the conditional UPDATE does.
func (f *fakeStore) claim(id string, to domain.ChangeRequestStatus, reason string, at time.Time, from ...domain.ChangeRequestStatus) (bool, error) {
	f.requests.mu.Lock()
	defer f.requests.mu.Unlock()
	r, ok := f.requests.rows[id]
	if !ok {
		return false, ports.ErrChangeRequestNotFound
	}
	if r.DecisionReason != "" {
		return false, nil
	}
	for _, s := range from {
		if r.Status == s {
			r.Status, r.StatusChangedAt = to, at
			if reason != "" {
				r.DecisionReason = reason
			}
			f.requests.rows[id] = r
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) StartReview(_ context.Context, id string, by domain.Actor, at time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	won, err := f.claim(id, domain.ChangeRequestInReview, "", at, domain.ChangeRequestFiled)
	if err != nil || won {
		if won {
			f.actions = append(f.actions, domain.RequestAction{RequestID: id, Type: domain.ActionReview, By: by, At: at})
		}
		return won, err
	}
	if f.requests.rows[id].Status == domain.ChangeRequestInReview {
		return false, nil
	}
	return false, ports.ErrRequestAlreadyDecided
}

func (f *fakeStore) Apply(_ context.Context, a ports.AppliedEndorsement) (domain.Endorsement, error) {
	if f.beforeApply != nil {
		f.beforeApply()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return domain.Endorsement{}, f.err
	}
	if last := f.latest(a.Request.PolicyID); last != nil && last.Version != a.BaseVersion {
		return domain.Endorsement{}, ports.ErrPolicyChanged
	}
	won, err := f.claim(a.Request.ID, domain.ChangeRequestApplied, "", a.At, domain.ChangeRequestFiled, domain.ChangeRequestInReview)
	if err != nil {
		return domain.Endorsement{}, err
	}
	if !won {
		return domain.Endorsement{}, ports.ErrRequestAlreadyDecided
	}
	f.counter++
	attempt := domain.EndorsementAttempt{ID: strconv.Itoa(len(f.attempts) + 1), RequestID: a.Request.ID, PolicyID: a.Request.PolicyID,
		Outcome: domain.OutcomeApplied, Validations: a.Validations, By: a.By, At: a.At}
	f.attempts = append(f.attempts, attempt)
	e := domain.Endorsement{ID: domain.FormatEndorsementID(domain.EndorsementYear(a.At), f.counter), RequestID: a.Request.ID,
		PolicyID: a.Request.PolicyID, Type: a.Request.Type, Version: a.BaseVersion + 1, Terms: a.Terms,
		PreviousPremium: a.PreviousPremium, AttemptID: attempt.ID, By: a.By, At: a.At}
	f.endorsements = append(f.endorsements, e)
	return e, nil
}

func (f *fakeStore) RecordNotApplied(_ context.Context, a domain.EndorsementAttempt) (domain.EndorsementAttempt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	won, err := f.claim(a.RequestID, domain.ChangeRequestNotApplied, "", a.At, domain.ChangeRequestFiled, domain.ChangeRequestInReview)
	if err != nil {
		return domain.EndorsementAttempt{}, err
	}
	if !won {
		return domain.EndorsementAttempt{}, ports.ErrRequestAlreadyDecided
	}
	a.ID, a.Outcome = strconv.Itoa(len(f.attempts)+1), domain.OutcomeNotApplied
	f.attempts = append(f.attempts, a)
	return a, nil
}

func (f *fakeStore) Reject(_ context.Context, id, observations string, by domain.Actor, at time.Time) error {
	return f.decide(id, observations, by, at, domain.ActionRejection, ports.ErrRequestAlreadyDecided,
		domain.ChangeRequestRejected, domain.ChangeRequestFiled, domain.ChangeRequestInReview)
}

func (f *fakeStore) Return(_ context.Context, id, observations string, by domain.Actor, at time.Time) error {
	return f.decide(id, observations, by, at, domain.ActionReturn, ports.ErrNotReturnable,
		domain.ChangeRequestNotApplied, domain.ChangeRequestNotApplied)
}

func (f *fakeStore) decide(id, observations string, by domain.Actor, at time.Time, t domain.RequestActionType, lost error,
	to domain.ChangeRequestStatus, from ...domain.ChangeRequestStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	won, err := f.claim(id, to, observations, at, from...)
	if err != nil {
		return err
	}
	if !won {
		return lost
	}
	f.actions = append(f.actions, domain.RequestAction{RequestID: id, Type: t, Observations: observations, By: by, At: at})
	return nil
}

func (f *fakeStore) History(_ context.Context, policyID string) ([]domain.EndorsementHistoryEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	out := []domain.EndorsementHistoryEntry{}
	for _, e := range f.endorsements {
		if e.PolicyID == policyID {
			out = append(out, domain.EndorsementHistoryEntry{Outcome: domain.OutcomeApplied, At: e.At, EndorsementID: e.ID,
				Request: f.requests.rows[e.RequestID], PreviousPremium: e.PreviousPremium, NewPremium: e.Terms.Premium, By: e.By})
		}
	}
	for _, a := range f.attempts {
		if a.PolicyID == policyID && a.Outcome == domain.OutcomeNotApplied {
			out = append(out, domain.EndorsementHistoryEntry{Outcome: domain.OutcomeNotApplied, At: a.At,
				Request: f.requests.rows[a.RequestID], Failed: a.Validations.Of(domain.ValidationFailed), By: a.By})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// fakeDocuments answers the registry by the end of the number, like the stub:
// "000" is not found; unavailable answers every document as unverifiable.
type fakeDocuments struct {
	unavailable bool
	asked       []string
}

func (f *fakeDocuments) CheckDocument(_ context.Context, b domain.Beneficiary) domain.DocumentStatus {
	f.asked = append(f.asked, b.DocumentNumber)
	switch {
	case f.unavailable:
		return domain.DocumentUnverifiable
	case len(b.DocumentNumber) >= 3 && b.DocumentNumber[len(b.DocumentNumber)-3:] == "000":
		return domain.DocumentNotFound
	default:
		return domain.DocumentValid
	}
}

type fakeClaims struct {
	open map[string]int
	err  error
}

func (f fakeClaims) OpenClaims(_ context.Context, policyID string) (int, error) {
	return f.open[policyID], f.err
}

type fakeRenewals struct {
	rows map[string]domain.Renewal
	err  error
}

func (f fakeRenewals) Get(_ context.Context, policyID string, _ time.Time) (domain.Renewal, error) {
	if f.err != nil {
		return domain.Renewal{}, f.err
	}
	r, ok := f.rows[policyID]
	if !ok {
		return domain.Renewal{}, ports.ErrRenewalNotFound
	}
	return r, nil
}

type fakeOffers struct {
	rows map[string]domain.PersonalizedOffer
	err  error
}

func (f fakeOffers) Get(_ context.Context, id string) (domain.PersonalizedOffer, error) {
	if f.err != nil {
		return domain.PersonalizedOffer{}, f.err
	}
	o, ok := f.rows[id]
	if !ok {
		return domain.PersonalizedOffer{}, ports.ErrOfferNotFound
	}
	return o, nil
}
