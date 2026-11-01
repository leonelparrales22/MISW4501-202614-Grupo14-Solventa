package credentials_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/credentials"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/ports"
)

var (
	t0      = time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC)
	analyst = domain.Actor{Type: domain.ActorOperations, ID: "sub-1", Label: "andres@solventa.co"}
)

type fixture struct {
	store *memStore
	gw    *fakeGateway
	clock *fixedClock
	svc   *credentials.Service
}

// activePartner seeds an approved partner with one active credential.
func newFixture(t *testing.T) fixture {
	t.Helper()
	f := fixture{store: newMemStore(), gw: newFakeGateway(), clock: &fixedClock{now: t0}}
	issuer := credentials.NewIssuer(f.gw, nil, "test")
	svc, err := credentials.NewService(f.store, issuer, f.clock, 24)
	require.NoError(t, err)
	f.svc = svc
	p := domain.Partner{ID: "ptn-1", Code: "ten_banco", Name: "Banco", Type: domain.TypeBank, Status: domain.StatusActive,
		Quota: domain.Quota{DailyLimit: 5000, RateLimit: 100, BurstLimit: 200}}
	plan, err := issuer.CreatePlan(context.Background(), p)
	require.NoError(t, err)
	p.UsagePlanID = plan
	c, _, err := issuer.Issue(context.Background(), p, plan, analyst, domain.ReasonApproval, t0.Add(-time.Hour))
	require.NoError(t, err)
	f.store.partners[p.ID] = ports.PartnerRecord{Partner: p, Credentials: []domain.Credential{c}}
	return f
}

func TestNewServiceRejectsInvalidDefaultGrace(t *testing.T) {
	_, err := credentials.NewService(newMemStore(), credentials.NewIssuer(nil, nil, "x"), &fixedClock{}, 0)
	require.Error(t, err)
}

func TestRotateKeepsPreviousValidDuringTransition(t *testing.T) {
	f := newFixture(t)
	secret, err := f.svc.Rotate(context.Background(), "ptn-1", 0, analyst)
	require.NoError(t, err)
	assert.Len(t, secret.ClientSecret, 43)
	assert.Regexp(t, `^sol_[0-9a-f]{26}$`, secret.ClientID)

	rec := f.store.partners["ptn-1"]
	require.Len(t, rec.Credentials, 2)
	assert.Equal(t, domain.CredentialActive, rec.Credentials[0].Status)
	assert.Equal(t, domain.Hint(secret.ClientSecret), rec.Credentials[0].SecretHint)
	assert.Equal(t, domain.CredentialRetiring, rec.Credentials[1].Status)
	assert.Equal(t, t0.Add(24*time.Hour), *rec.Credentials[1].ExpiresAt)
	// Both keys are in the same usage plan: transactions in flight go on.
	assert.Len(t, f.gw.attached, 2)
	require.Len(t, f.store.outbox, 1)
	assert.Equal(t, domain.EventCredentialsRotated, f.store.outbox[0].EventType)
	assert.NotContains(t, string(f.store.outbox[0].Payload), secret.ClientSecret)
	assert.Equal(t, domain.HistoryCredentialsRotated, rec.History[0].Type)
	assert.Equal(t, analyst, rec.History[0].Actor)
}

func TestRotateRules(t *testing.T) {
	t.Run("grace out of range", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.svc.Rotate(context.Background(), "ptn-1", 200, analyst)
		var verr *domain.ValidationError
		require.ErrorAs(t, err, &verr)
		assert.Equal(t, "grace_period_hours", verr.Fields[0].Field)
	})
	t.Run("second rotation during transition", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.svc.Rotate(context.Background(), "ptn-1", 2, analyst)
		require.NoError(t, err)
		_, err = f.svc.Rotate(context.Background(), "ptn-1", 2, analyst)
		require.ErrorIs(t, err, domain.ErrRotationPending)
	})
	t.Run("pending partner", func(t *testing.T) {
		f := newFixture(t)
		rec := f.store.partners["ptn-1"]
		rec.Partner.Status = domain.StatusPendingApproval
		f.store.partners["ptn-1"] = rec
		_, err := f.svc.Rotate(context.Background(), "ptn-1", 0, analyst)
		require.ErrorIs(t, err, domain.ErrNotActive)
	})
	t.Run("unknown partner", func(t *testing.T) {
		f := newFixture(t)
		_, err := f.svc.Rotate(context.Background(), "ptn-x", 0, analyst)
		require.ErrorIs(t, err, domain.ErrPartnerNotFound)
	})
	t.Run("gateway down changes nothing", func(t *testing.T) {
		f := newFixture(t)
		f.gw.failOn = "attach"
		_, err := f.svc.Rotate(context.Background(), "ptn-1", 0, analyst)
		require.ErrorIs(t, err, domain.ErrGatewayUnavailable)
		assert.Len(t, f.store.partners["ptn-1"].Credentials, 1)
		assert.Len(t, f.gw.keys, 1, "the new key is removed")
	})
	t.Run("database failure removes the new key", func(t *testing.T) {
		f := newFixture(t)
		f.store.failNext = errDB
		_, err := f.svc.Rotate(context.Background(), "ptn-1", 0, analyst)
		require.ErrorIs(t, err, errDB)
		assert.Len(t, f.gw.keys, 1)
	})
}

func TestRetireDueRevokesAfterTransition(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Rotate(context.Background(), "ptn-1", 1, analyst)
	require.NoError(t, err)
	old := f.store.partners["ptn-1"].Credentials[1]

	n, err := f.svc.RetireDue(context.Background(), 10)
	require.NoError(t, err)
	assert.Zero(t, n, "still in transition")

	f.clock.now = t0.Add(time.Hour)
	n, err = f.svc.RetireDue(context.Background(), 10)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	rec := f.store.partners["ptn-1"]
	assert.Equal(t, domain.CredentialRevoked, rec.Credentials[1].Status)
	assert.NotContains(t, f.gw.keys, old.GatewayKeyID)
	assert.Equal(t, domain.HistoryCredentialsRevoked, rec.History[0].Type)
	assert.Equal(t, domain.SystemActor, rec.History[0].Actor)
	assert.Equal(t, domain.EventCredentialsRevoked, f.store.outbox[len(f.store.outbox)-1].EventType)

	// A second pass finds nothing.
	n, err = f.svc.RetireDue(context.Background(), 10)
	require.NoError(t, err)
	assert.Zero(t, n)
}

func TestRetireDueRetriesWhenGatewayFails(t *testing.T) {
	f := newFixture(t)
	_, err := f.svc.Rotate(context.Background(), "ptn-1", 1, analyst)
	require.NoError(t, err)
	f.clock.now = t0.Add(2 * time.Hour)
	f.gw.failOn = "delete"
	n, err := f.svc.RetireDue(context.Background(), 10)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Equal(t, domain.CredentialRetiring, f.store.partners["ptn-1"].Credentials[1].Status)
}

func TestWorkerRunsUntilCancelled(t *testing.T) {
	f := newFixture(t)
	_, err := credentials.NewWorker(f.svc, 0)
	require.Error(t, err)
	w, err := credentials.NewWorker(f.svc, time.Millisecond)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	w.Run(ctx)
	assert.True(t, errors.Is(ctx.Err(), context.DeadlineExceeded))
}

func TestIssuerWithoutGateway(t *testing.T) {
	i := credentials.NewIssuer(nil, nil, "x")
	assert.False(t, i.Available())
	_, err := i.CreatePlan(context.Background(), domain.Partner{})
	require.ErrorIs(t, err, domain.ErrGatewayUnavailable)
	_, _, err = i.Issue(context.Background(), domain.Partner{}, "p", analyst, domain.ReasonApproval, t0)
	require.ErrorIs(t, err, domain.ErrGatewayUnavailable)
	require.ErrorIs(t, i.Retire(context.Background(), "p", "k"), domain.ErrGatewayUnavailable)
	assert.True(t, credentials.IsGatewayError(err))
	i.Discard(context.Background(), "p", "k") // no gateway: nothing to do
}

func TestRandomGenerator(t *testing.T) {
	g := credentials.RandomGenerator{}
	id, err := g.ID("ptn-")
	require.NoError(t, err)
	assert.Regexp(t, `^ptn-[0-9a-f]{32}$`, id)
	a, _ := g.Secret()
	b, _ := g.Secret()
	assert.NotEqual(t, a, b)
}
