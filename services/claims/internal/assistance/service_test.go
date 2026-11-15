package assistance_test

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/assistance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The request for on-site assistance (HU-M2.3-01 criteria 2, 4 and 5).

const (
	carolina = "sub-carolina"
	andres   = "sub-andres"
	key      = "assist-7c1f"
)

var start = time.Date(2026, 11, 14, 21, 31, 8, 123456789, time.UTC)

type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

// directory is the policy core: the policies of each holder, or a failure.
type directory struct {
	mu       sync.Mutex
	policies map[string][]domain.Policy
	err      error
	calls    int
}

func (d *directory) HolderPolicies(_ context.Context, subject string) ([]domain.Policy, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if d.err != nil {
		return nil, d.err
	}
	return d.policies[subject], nil
}

// store counts the writes and can fail.
type store struct {
	*memory.AssistanceStore
	creates int
	err     error
}

func (s *store) Create(ctx context.Context, r domain.AssistanceRequest) (domain.AssistanceRequest, bool, error) {
	s.creates++
	if s.err != nil {
		return domain.AssistanceRequest{}, false, s.err
	}
	return s.AssistanceStore.Create(ctx, r)
}

func (s *store) Get(ctx context.Context, id string) (domain.AssistanceRequest, error) {
	if s.err != nil {
		return domain.AssistanceRequest{}, s.err
	}
	return s.AssistanceStore.Get(ctx, id)
}

func autoPolicy(id string, status domain.PolicyStatus, from, to time.Time) domain.Policy {
	return domain.Policy{ID: id, Line: domain.LineAuto, Product: "Autos", InsuredObject: "KJT-482",
		Status: status, ValidFrom: from, ValidTo: to}
}

type fixture struct {
	svc      *assistance.Service
	clock    *clock
	policies *directory
	requests *store
}

func newFixture() fixture {
	c := &clock{now: start}
	d := &directory{policies: map[string][]domain.Policy{
		carolina: {
			autoPolicy("AU-2025-118204", domain.PolicyActive, start.AddDate(-1, 0, 0), start.AddDate(0, 6, 0)),
			autoPolicy("AU-2024-000001", "expired", start.AddDate(-2, 0, 0), start.AddDate(0, 0, -30)),
			{ID: "HO-2025-000002", Line: domain.LineHome, Product: "Hogar", InsuredObject: "Calle 80 # 15-20",
				Status: domain.PolicyActive, ValidFrom: start.AddDate(-1, 0, 0), ValidTo: start.AddDate(0, 6, 0)},
		},
		andres: {autoPolicy("AU-2025-555555", domain.PolicyActive, start.AddDate(-1, 0, 0), start.AddDate(0, 6, 0))},
	}}
	s := &store{AssistanceStore: memory.NewAssistanceStore()}
	return fixture{svc: assistance.NewService(s, d, c), clock: c, policies: d, requests: s}
}

func ptr[T any](v T) *T { return &v }

func withLocation() domain.AssistanceRequestInput {
	return domain.AssistanceRequestInput{
		PolicyID: "AU-2025-118204",
		Location: &domain.AssistanceLocationInput{Status: "sent", Latitude: ptr(4.7712), Longitude: ptr(-74.0452),
			AccuracyM: ptr(8.0), CapturedAt: ptr("2026-11-14T21:31:02Z")},
		Place: ptr("Autopista Norte km 12, Bogotá"),
	}
}

func withoutLocation(reason string) domain.AssistanceRequestInput {
	return domain.AssistanceRequestInput{PolicyID: "AU-2025-118204",
		Location: &domain.AssistanceLocationInput{Status: "unavailable", UnavailableReason: ptr(reason)}}
}

var uuidV4 = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestARequestWithALocationIsRecorded(t *testing.T) {
	f := newFixture()
	r, created, err := f.svc.Request(context.Background(),
		assistance.Requester{Subject: carolina, Name: "Carolina Ruiz"}, key, withLocation())
	require.NoError(t, err)

	assert.True(t, created)
	assert.Regexp(t, uuidV4, r.ID)
	assert.Equal(t, "AST-2026-0001", r.Number)
	assert.Equal(t, domain.AssistanceNew, r.Status)
	assert.Equal(t, start.Truncate(time.Microsecond), r.RequestedAt, "the clock of the server, in UTC")
	assert.Equal(t, time.UTC, r.RequestedAt.Location())
	assert.Equal(t, domain.AssistancePolicy{ID: "AU-2025-118204", Line: domain.LineAuto, Product: "Autos",
		InsuredObject: "KJT-482"}, r.Policy)
	assert.Equal(t, domain.LocationSent, r.Location.Status)
	assert.Equal(t, domain.Coordinates{Latitude: 4.7712, Longitude: -74.0452}, *r.Location.Coordinates)
	assert.Equal(t, 8.0, *r.Location.AccuracyM)
	assert.Equal(t, "Autopista Norte km 12, Bogotá", *r.Place)
	assert.Equal(t, carolina, r.Subject)
	assert.Equal(t, "Carolina Ruiz", r.RequesterName)
	assert.Equal(t, key, r.Key)
	assert.NotEmpty(t, r.Fingerprint)
}

func TestARequestWithoutALocationIsRecordedWithItsReason(t *testing.T) {
	for i, reason := range []string{"permission_denied", "location_disabled", "no_fix"} {
		t.Run(reason, func(t *testing.T) {
			f := newFixture()
			f.clock.now = start.Add(time.Duration(i) * time.Minute)
			r, created, err := f.svc.Request(context.Background(), assistance.Requester{Subject: carolina},
				key, withoutLocation(reason))
			require.NoError(t, err)
			assert.True(t, created)
			assert.Equal(t, domain.LocationUnavailable, r.Location.Status)
			assert.Equal(t, domain.LocationUnavailableReason(reason), r.Location.UnavailableReason)
			assert.Nil(t, r.Location.Coordinates)
			assert.Equal(t, f.clock.now.Truncate(time.Microsecond), r.RequestedAt)
			assert.Nil(t, r.Place)
		})
	}
}

func TestATokenWithoutANameDoesNotFail(t *testing.T) {
	f := newFixture()
	r, _, err := f.svc.Request(context.Background(), assistance.Requester{Subject: carolina}, key, withLocation())
	require.NoError(t, err)
	assert.Empty(t, r.RequesterName)
}

func TestAnyLineTakesARequest(t *testing.T) {
	f := newFixture()
	in := withoutLocation("no_fix")
	in.PolicyID = "HO-2025-000002"
	r, _, err := f.svc.Request(context.Background(), assistance.Requester{Subject: carolina}, key, in)
	require.NoError(t, err)
	assert.Equal(t, domain.LineHome, r.Policy.Line)
}

func TestAnInvalidBodyCostsNoCallToThePolicyCore(t *testing.T) {
	f := newFixture()
	in := withLocation()
	in.Location.Latitude = ptr(91.0)
	_, _, err := f.svc.Request(context.Background(), assistance.Requester{Subject: carolina}, key, in)

	var invalid *domain.ValidationError
	require.ErrorAs(t, err, &invalid)
	assert.Equal(t, []domain.FieldError{{Field: "location.latitude", Code: domain.CodeOutOfRange}}, invalid.Fields)
	assert.Zero(t, f.policies.calls)
	assert.Zero(t, f.requests.creates)
}

func TestOnlyAnActivePolicyOfTheInsuredTakesARequest(t *testing.T) {
	cases := map[string]struct {
		policyID string
		reason   string
		validity bool
	}{
		"a policy of someone else":     {"AU-2025-555555", assistance.ReasonPolicyNotFound, false},
		"a policy that does not exist": {"AU-2025-000404", assistance.ReasonPolicyNotFound, false},
		"a policy not in force":        {"AU-2024-000001", assistance.ReasonPolicyNotActive, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture()
			in := withLocation()
			in.PolicyID = c.policyID
			_, _, err := f.svc.Request(context.Background(), assistance.Requester{Subject: carolina}, key, in)

			var notEligible *assistance.PolicyNotEligibleError
			require.ErrorAs(t, err, &notEligible)
			assert.Equal(t, c.reason, notEligible.Reason)
			assert.Contains(t, notEligible.Error(), c.reason)
			if c.validity {
				require.NotNil(t, notEligible.Policy)
				assert.Equal(t, c.policyID, notEligible.Policy.ID)
			} else {
				assert.Nil(t, notEligible.Policy, "a policy of someone else says nothing of it")
			}
			assert.Zero(t, f.requests.creates, "nothing is recorded")
		})
	}
}

func TestWithThePolicyCoreDownNothingIsRecorded(t *testing.T) {
	f := newFixture()
	f.policies.err = ports.ErrPolicyDirectoryUnavailable
	_, _, err := f.svc.Request(context.Background(), assistance.Requester{Subject: carolina}, key, withLocation())
	assert.ErrorIs(t, err, ports.ErrPolicyDirectoryUnavailable)
	assert.Zero(t, f.requests.creates)

	// When it is back, the same key records the request.
	f.policies.err = nil
	_, created, err := f.svc.Request(context.Background(), assistance.Requester{Subject: carolina}, key, withLocation())
	require.NoError(t, err)
	assert.True(t, created)
}

func TestARetryWithTheSameKeyFindsTheSameRequest(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	first, _, err := f.svc.Request(ctx, assistance.Requester{Subject: carolina}, key, withLocation())
	require.NoError(t, err)

	f.clock.now = start.Add(40 * time.Second)
	again, created, err := f.svc.Request(ctx, assistance.Requester{Subject: carolina}, key, withLocation())
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, first, again, "same id, same number and its original instant")

	_, _, err = f.svc.Request(ctx, assistance.Requester{Subject: carolina}, key, withoutLocation("no_fix"))
	assert.ErrorIs(t, err, ports.ErrAssistanceKeyReused, "the same key with another body")

	other, created, err := f.svc.Request(ctx, assistance.Requester{Subject: carolina}, "assist-8d2a", withLocation())
	require.NoError(t, err)
	assert.True(t, created, "another key is another request (assumption S10)")
	assert.NotEqual(t, first.ID, other.ID)
	assert.Equal(t, "AST-2026-0002", other.Number)

	in := withLocation()
	in.PolicyID = "AU-2025-555555"
	theirs, created, err := f.svc.Request(ctx, assistance.Requester{Subject: andres}, key, in)
	require.NoError(t, err)
	assert.True(t, created, "the same key of another insured is another request")
	assert.NotEqual(t, first.ID, theirs.ID)
}

func TestAFailureOfTheStoreIsReturned(t *testing.T) {
	f := newFixture()
	boom := errors.New("connection refused")
	f.requests.err = boom
	_, _, err := f.svc.Request(context.Background(), assistance.Requester{Subject: carolina}, key, withLocation())
	assert.ErrorIs(t, err, boom)
	_, err = f.svc.Get(context.Background(), carolina, "3b8e2f6a-1c4d-4e7f-9a2b-6c5d8e0f1a2b")
	assert.ErrorIs(t, err, boom)
}

func TestOnlyTheOwnerReadsARequest(t *testing.T) {
	f := newFixture()
	ctx := context.Background()
	r, _, err := f.svc.Request(ctx, assistance.Requester{Subject: carolina}, key, withLocation())
	require.NoError(t, err)

	got, err := f.svc.Get(ctx, carolina, r.ID)
	require.NoError(t, err)
	assert.Equal(t, r, got)

	_, err = f.svc.Get(ctx, andres, r.ID)
	assert.ErrorIs(t, err, ports.ErrAssistanceRequestNotFound, "another insured")
	_, err = f.svc.Get(ctx, carolina, "3b8e2f6a-1c4d-4e7f-9a2b-6c5d8e0f1a2b")
	assert.ErrorIs(t, err, ports.ErrAssistanceRequestNotFound, "one that does not exist")
}

func TestEveryRequestHasItsOwnID(t *testing.T) {
	f := newFixture()
	ids := map[string]bool{}
	for i := 0; i < 50; i++ {
		r, _, err := f.svc.Request(context.Background(), assistance.Requester{Subject: carolina},
			"assist-"+string(rune('a'+i%26))+string(rune('a'+i/26)), withLocation())
		require.NoError(t, err)
		assert.Regexp(t, uuidV4, r.ID)
		ids[r.ID] = true
	}
	assert.Len(t, ids, 50)
}

func TestWithoutAnIDNothingIsRecorded(t *testing.T) {
	f := newFixture()
	boom := errors.New("no entropy")
	f.svc.WithNewID(func() (string, error) { return "", boom })
	_, _, err := f.svc.Request(context.Background(), assistance.Requester{Subject: carolina}, key, withLocation())
	assert.ErrorIs(t, err, boom)
	assert.Zero(t, f.requests.creates)
}
