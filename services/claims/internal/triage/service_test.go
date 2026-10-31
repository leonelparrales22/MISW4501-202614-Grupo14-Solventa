package triage_test

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/triage"
)

// The calculation, the record and the reads of the priority of the claims
// (HU-W2.2-01).

type fixedClock struct{ at time.Time }

func (c *fixedClock) Now() time.Time { return c.at }

var filedAt = time.Date(2026, 9, 4, 13, 30, 0, 0, time.UTC)

// fakeStore is a TriageStore whose answers the tests control.
type fakeStore struct {
	mu      sync.Mutex
	claims  map[string]domain.ClaimRecord
	records map[string][]domain.TriageRecord

	pendingErr error
	recordErr  error
	// failFor makes Record fail for one claim only.
	failFor string
	// conflictFor makes another task win the next Record of the claim.
	conflictFor string
}

func newFakeStore(claims ...domain.ClaimRecord) *fakeStore {
	s := &fakeStore{claims: map[string]domain.ClaimRecord{}, records: map[string][]domain.TriageRecord{}}
	for _, c := range claims {
		s.claims[c.ID] = c
	}
	return s
}

func (s *fakeStore) last(id string) *domain.TriageRecord {
	r := s.records[id]
	if len(r) == 0 {
		return nil
	}
	last := r[len(r)-1]
	return &last
}

func (s *fakeStore) Pending(_ context.Context, statuses []domain.Status, _ string, limit int) ([]ports.TriageCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pendingErr != nil {
		return nil, s.pendingErr
	}
	var out []ports.TriageCandidate
	for _, c := range s.claims {
		for _, st := range statuses {
			if c.Status == st {
				out = append(out, ports.TriageCandidate{Claim: c, Last: s.last(c.ID)})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Claim.ID < out[j].Claim.ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *fakeStore) Candidate(_ context.Context, id string) (ports.TriageCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.claims[id]
	if !ok {
		return ports.TriageCandidate{}, ports.ErrClaimNotFound
	}
	return ports.TriageCandidate{Claim: c, Last: s.last(id)}, nil
}

func (s *fakeStore) Record(_ context.Context, r domain.TriageRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.recordErr != nil:
		return s.recordErr
	case r.ClaimID == s.failFor:
		return errors.New("the database is down")
	case r.ClaimID == s.conflictFor:
		s.conflictFor = ""
		winner := r
		s.records[r.ClaimID] = append(s.records[r.ClaimID], winner)
		return ports.ErrTriageConflict
	case r.Seq != len(s.records[r.ClaimID])+1:
		return ports.ErrTriageConflict
	}
	s.records[r.ClaimID] = append(s.records[r.ClaimID], r)
	return nil
}

func (s *fakeStore) History(_ context.Context, id string) ([]domain.TriageRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.TriageRecord
	for i := len(s.records[id]) - 1; i >= 0; i-- {
		out = append(out, s.records[id][i])
	}
	return out, nil
}

func (s *fakeStore) Current(_ context.Context, ids []string) (map[string]ports.TriageCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]ports.TriageCandidate{}
	for _, id := range ids {
		if c, ok := s.claims[id]; ok {
			out[id] = ports.TriageCandidate{Claim: domain.ClaimRecord{Claim: domain.Claim{ID: id, Status: c.Status}}, Last: s.last(id)}
		}
	}
	return out, nil
}

func (s *fakeStore) set(c domain.ClaimRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims[c.ID] = c
}

func amount(cop int64) *int64 {
	minor := cop * 100
	return &minor
}

func waterDamage(id string, cop *int64) domain.ClaimRecord {
	return domain.ClaimRecord{
		Claim: domain.Claim{ID: id, Subject: "sub-carolina", IncidentType: domain.IncidentWaterDamage,
			Status: domain.StatusActive, FiledAt: filedAt},
		Channel: domain.ChannelWeb, CatalogVersion: domain.CatalogVersionV1, EstimatedAmountMinor: cop,
	}
}

func newService(store *fakeStore) (*triage.Service, *fixedClock) {
	clock := &fixedClock{at: filedAt.Add(7*time.Second + 123*time.Nanosecond)}
	return triage.NewService(store, clock), clock
}

func candidate(t *testing.T, store *fakeStore, id string) ports.TriageCandidate {
	t.Helper()
	c, err := store.Candidate(context.Background(), id)
	require.NoError(t, err)
	return c
}

func TestTheFirstCalculationOfAClaimThatEntersEvaluation(t *testing.T) {
	for _, status := range []domain.Status{domain.StatusActive, domain.StatusInEvaluation} {
		c := waterDamage("cl-1", amount(4_800_000))
		c.Status = status
		store := newFakeStore(c)
		svc, clock := newService(store)

		r, recorded, err := svc.Assess(context.Background(), candidate(t, store, "cl-1"))

		require.NoError(t, err)
		require.True(t, recorded, status)
		assert.Equal(t, 1, r.Seq)
		assert.Equal(t, []domain.TriageCause{domain.CauseEnteredEvaluation}, r.Causes)
		assert.Nil(t, r.PreviousLevel)
		assert.Equal(t, domain.Triage2, r.Assessment.Level)
		assert.Equal(t, domain.SeverityMedium, r.Assessment.Severity)
		assert.Equal(t, domain.AmountLow, r.Assessment.AmountBand)
		assert.Equal(t, domain.TriageActorSystem, r.Actor)
		assert.Equal(t, clock.at.Truncate(time.Microsecond), r.AssessedAt, "stored with the precision of PostgreSQL")
		assert.Len(t, store.records["cl-1"], 1)
	}
}

func TestAClaimOutOfEvaluationIsNotAssessed(t *testing.T) {
	for _, status := range []domain.Status{"paid", "rejected", "payment_exception", "returned"} {
		c := waterDamage("cl-1", amount(4_800_000))
		c.Status = status
		store := newFakeStore(c)
		svc, _ := newService(store)

		_, recorded, err := svc.Assess(context.Background(), candidate(t, store, "cl-1"))

		require.NoError(t, err)
		assert.False(t, recorded, status)
		assert.Empty(t, store.records)
	}
}

func TestTheLevelIsRecalculatedWithTheCauseOfTheChange(t *testing.T) {
	store := newFakeStore(waterDamage("cl-1", amount(4_800_000)))
	svc, _ := newService(store)
	ctx := context.Background()
	_, _, err := svc.Assess(ctx, candidate(t, store, "cl-1"))
	require.NoError(t, err)

	_, recorded, err := svc.Assess(ctx, candidate(t, store, "cl-1"))
	require.NoError(t, err)
	assert.False(t, recorded, "nothing changed")

	store.set(waterDamage("cl-1", amount(40_000_000)))
	r, recorded, err := svc.Assess(ctx, candidate(t, store, "cl-1"))
	require.NoError(t, err)
	require.True(t, recorded)
	assert.Equal(t, 2, r.Seq)
	assert.Equal(t, domain.Triage1, r.Assessment.Level)
	assert.Equal(t, domain.Triage2, *r.PreviousLevel)
	assert.Equal(t, []domain.TriageCause{domain.CauseEstimatedAmountChanged}, r.Causes)

	glass := waterDamage("cl-1", amount(900_000))
	glass.IncidentType = domain.IncidentGlass
	store.set(glass)
	r, recorded, err = svc.Assess(ctx, candidate(t, store, "cl-1"))
	require.NoError(t, err)
	require.True(t, recorded)
	assert.Equal(t, domain.Triage3, r.Assessment.Level)
	assert.Equal(t, []domain.TriageCause{domain.CauseEstimatedAmountChanged, domain.CauseIncidentTypeChanged,
		domain.CauseCoverageChanged}, r.Causes)
}

func TestAChangeThatKeepsTheLevelIsRecordedToo(t *testing.T) {
	store := newFakeStore(waterDamage("cl-1", amount(4_800_000)))
	svc, _ := newService(store)
	_, _, err := svc.Assess(context.Background(), candidate(t, store, "cl-1"))
	require.NoError(t, err)

	store.set(waterDamage("cl-1", amount(4_000_000)))
	r, recorded, err := svc.Assess(context.Background(), candidate(t, store, "cl-1"))

	require.NoError(t, err)
	require.True(t, recorded)
	assert.Equal(t, domain.Triage2, r.Assessment.Level)
	assert.Equal(t, domain.Triage2, *r.PreviousLevel)
}

func TestANewVersionOfThePolicyRecalculatesTheClaims(t *testing.T) {
	store := newFakeStore(waterDamage("cl-1", amount(4_800_000)))
	svc, _ := newService(store)
	_, _, err := svc.Assess(context.Background(), candidate(t, store, "cl-1"))
	require.NoError(t, err)

	v2 := domain.TriagePolicyV1()
	v2.Version, v2.LowBelowMinor = "triage-policy-v2", 1_000_000_00
	r, recorded, err := svc.WithPolicy(v2).Assess(context.Background(), candidate(t, store, "cl-1"))

	require.NoError(t, err)
	require.True(t, recorded)
	assert.Equal(t, []domain.TriageCause{domain.CausePolicyUpdated}, r.Causes)
	assert.Equal(t, domain.AmountMedium, r.Assessment.AmountBand)
	assert.Equal(t, "triage-policy-v2", r.Assessment.Input.PolicyVersion)
	assert.Equal(t, "triage-policy-v2", svc.Policy().Version)
}

func TestAnotherTaskThatRecordsFirstIsNotAnError(t *testing.T) {
	store := newFakeStore(waterDamage("cl-1", amount(4_800_000)))
	store.conflictFor = "cl-1"
	svc, _ := newService(store)

	_, recorded, err := svc.Assess(context.Background(), candidate(t, store, "cl-1"))

	require.NoError(t, err)
	assert.False(t, recorded)
	assert.Len(t, store.records["cl-1"], 1, "the calculation of the other task")
}

func TestAFailureToRecordIsAnError(t *testing.T) {
	store := newFakeStore(waterDamage("cl-1", amount(4_800_000)))
	store.recordErr = errors.New("the database is down")
	svc, _ := newService(store)

	_, recorded, err := svc.Assess(context.Background(), candidate(t, store, "cl-1"))

	require.Error(t, err)
	assert.False(t, recorded)
}

func TestAClaimTypeOutOfTheTableIsAssessedAsMedium(t *testing.T) {
	c := waterDamage("cl-1", nil)
	c.IncidentType = "fire"
	store := newFakeStore(c)
	svc, _ := newService(store)

	r, recorded, err := svc.Assess(context.Background(), candidate(t, store, "cl-1"))

	require.NoError(t, err)
	require.True(t, recorded)
	assert.Equal(t, domain.SeverityMedium, r.Assessment.Severity)
	assert.Equal(t, domain.Triage2, r.Assessment.Level)
}

func TestTheSweepAssessesEveryWaitingClaimAndSurvivesAFailure(t *testing.T) {
	theft := waterDamage("cl-2", nil)
	theft.IncidentType = domain.IncidentTheft
	paid := waterDamage("cl-4", nil)
	paid.Status = "paid"
	store := newFakeStore(waterDamage("cl-1", amount(4_800_000)), theft, waterDamage("cl-3", nil), paid)
	store.failFor = "cl-1"
	svc, _ := newService(store)

	recorded, err := svc.Sweep(context.Background(), 10)

	require.NoError(t, err)
	assert.Equal(t, 2, recorded)
	assert.Empty(t, store.records["cl-1"], "it failed and waits for the next sweep")
	assert.Equal(t, domain.Triage1, store.records["cl-2"][0].Assessment.Level)
	assert.Equal(t, domain.AmountUnknown, store.records["cl-3"][0].Assessment.AmountBand)
	assert.Empty(t, store.records["cl-4"])

	store.failFor = ""
	recorded, err = svc.Sweep(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, recorded, "only the one that failed")

	recorded, err = svc.Sweep(context.Background(), 1)
	require.NoError(t, err)
	assert.Zero(t, recorded)
}

func TestTheSweepReportsAStoreThatCannotBeRead(t *testing.T) {
	store := newFakeStore()
	store.pendingErr = errors.New("the database is down")
	svc, _ := newService(store)

	_, err := svc.Sweep(context.Background(), 10)

	require.Error(t, err)
}

func as(subject string, roles ...security.Role) context.Context {
	return security.WithClaims(context.Background(), security.Claims{Subject: subject, Roles: roles})
}

func TestOperationsReadsTheTriageOfAnyClaim(t *testing.T) {
	store := newFakeStore(waterDamage("cl-1", amount(4_800_000)))
	svc, _ := newService(store)
	_, _, err := svc.Assess(context.Background(), candidate(t, store, "cl-1"))
	require.NoError(t, err)
	store.set(waterDamage("cl-1", amount(40_000_000)))
	_, _, err = svc.Assess(context.Background(), candidate(t, store, "cl-1"))
	require.NoError(t, err)

	v, err := svc.View(as("sub-analyst", security.RoleOperations), "cl-1")

	require.NoError(t, err)
	assert.Equal(t, domain.TriageAssessed, v.State)
	require.Len(t, v.History, 2)
	assert.Equal(t, 2, v.History[0].Seq, "newest first")
	require.NotNil(t, v.Current)
	assert.Equal(t, domain.Triage1, v.Current.Assessment.Level)
}

func TestTheInsuredReadsOnlyTheTriageOfTheirClaims(t *testing.T) {
	store := newFakeStore(waterDamage("cl-1", amount(4_800_000)))
	svc, _ := newService(store)

	v, err := svc.View(as("sub-carolina", security.RoleInsured), "cl-1")
	require.NoError(t, err)
	assert.Equal(t, domain.TriagePending, v.State)
	assert.Nil(t, v.Current)
	assert.Empty(t, v.History)

	_, err = svc.View(as("sub-other", security.RoleInsured), "cl-1")
	assert.ErrorIs(t, err, security.ErrForbidden)

	_, err = svc.View(as("sub-advisor", security.RoleAdvisor), "cl-1")
	assert.ErrorIs(t, err, security.ErrForbidden)

	_, err = svc.View(as("sub-carolina", security.RoleInsured), "cl-404")
	assert.ErrorIs(t, err, ports.ErrClaimNotFound)
}

func TestAClaimThatNeverEnteredEvaluationHasNoTriage(t *testing.T) {
	c := waterDamage("cl-1", nil)
	c.Status = "rejected"
	svc, _ := newService(newFakeStore(c))

	v, err := svc.View(as("sub-analyst", security.RoleOperations), "cl-1")

	require.NoError(t, err)
	assert.Equal(t, domain.TriageNotApplicable, v.State)
}

func TestTheSummariesComeInTheOrderAsked(t *testing.T) {
	store := newFakeStore(waterDamage("cl-1", amount(4_800_000)), waterDamage("cl-2", nil))
	svc, _ := newService(store)
	_, _, err := svc.Assess(context.Background(), candidate(t, store, "cl-1"))
	require.NoError(t, err)

	got, err := svc.Summaries(context.Background(), []string{"cl-2", "cl-404", "cl-1"})

	require.NoError(t, err)
	require.Len(t, got, 3)
	assert.Equal(t, triage.Summary{ClaimID: "cl-2", State: domain.TriagePending}, got[0])
	assert.Equal(t, triage.Summary{ClaimID: "cl-404", State: domain.TriageNotFound}, got[1])
	assert.Equal(t, domain.TriageAssessed, got[2].State)
	require.NotNil(t, got[2].Current)
	assert.Equal(t, domain.Triage2, got[2].Current.Assessment.Level)
}
