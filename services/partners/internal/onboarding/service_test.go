package onboarding_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/credentials"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/onboarding"
)

var (
	t0      = time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC)
	analyst = domain.Actor{Type: domain.ActorOperations, ID: "sub-1", Label: "andres@solventa.co"}
)

func request(code, name string) domain.RegisterRequest {
	return domain.RegisterRequest{
		Name: name, Code: code, Type: domain.TypeBank, Products: []string{" hogar "},
		TechnicalContactEmail: "api@banco.co", Quota: domain.Quota{DailyLimit: 1000, RateLimit: 10, BurstLimit: 20},
	}
}

func newService(gw *fakeGateway) (*onboarding.Service, *memStore) {
	store := newMemStore()
	var g interface{} = gw
	issuer := credentials.NewIssuer(nil, nil, "test")
	if gw != nil {
		issuer = credentials.NewIssuer(g.(*fakeGateway), nil, "test")
	}
	return onboarding.NewService(store, issuer, &fixedClock{now: t0}), store
}

func TestRegisterLeavesPartnerPending(t *testing.T) {
	svc, _ := newService(newFakeGateway())
	v, err := svc.Register(context.Background(), request("ten_banco", "Banco Aliado"), analyst)
	require.NoError(t, err)
	assert.Equal(t, domain.StatusPendingApproval, v.Partner.Status)
	assert.Equal(t, []string{"hogar"}, v.Partner.Products)
	assert.Equal(t, domain.CredentialsNone, v.CredentialsStatus())
	assert.Equal(t, domain.UsageNone, v.UsageStatus())
	assert.Equal(t, domain.HistoryRegistered, v.History[0].Type)

	_, err = svc.Register(context.Background(), request("ten_banco", "Otro"), analyst)
	require.ErrorIs(t, err, domain.ErrCodeTaken)
	_, err = svc.Register(context.Background(), domain.RegisterRequest{}, analyst)
	var verr *domain.ValidationError
	require.ErrorAs(t, err, &verr)
}

func TestApproveIssuesUniqueCredentialsWithQuota(t *testing.T) {
	gw := newFakeGateway()
	svc, store := newService(gw)
	a, _ := svc.Register(context.Background(), request("ten_a", "Banco A"), analyst)
	b, _ := svc.Register(context.Background(), request("ten_b", "Banco B"), analyst)

	ra, err := svc.Approve(context.Background(), a.Partner.ID, analyst)
	require.NoError(t, err)
	rb, err := svc.Approve(context.Background(), b.Partner.ID, analyst)
	require.NoError(t, err)

	// CA-1: unique credentials, each in its own usage plan with its quota.
	assert.NotEqual(t, ra.Secret.ClientID, rb.Secret.ClientID)
	assert.NotEqual(t, ra.Secret.ClientSecret, rb.Secret.ClientSecret)
	assert.NotEqual(t, ra.View.Partner.UsagePlanID, rb.View.Partner.UsagePlanID)
	assert.Equal(t, domain.Quota{DailyLimit: 1000, RateLimit: 10, BurstLimit: 20}, gw.plans[ra.View.Partner.UsagePlanID])
	assert.Equal(t, domain.StatusActive, ra.View.Partner.Status)
	assert.Equal(t, domain.CredentialsActive, ra.View.CredentialsStatus())
	// The secret is never stored, only its hint.
	assert.Equal(t, domain.Hint(ra.Secret.ClientSecret), ra.View.Credentials[0].SecretHint)
	// CA-3: the issuance goes to audit through the outbox, with who and when.
	require.Len(t, store.outbox, 2)
	assert.Equal(t, domain.EventCredentialsIssued, store.outbox[0].EventType)
	assert.Contains(t, string(store.outbox[0].Payload), `"label":"andres@solventa.co"`)
	assert.NotContains(t, string(store.outbox[0].Payload), ra.Secret.ClientSecret)

	// CA-4: approving again never shows the secret again.
	_, err = svc.Approve(context.Background(), a.Partner.ID, analyst)
	require.ErrorIs(t, err, domain.ErrNotPending)
}

func TestApproveFailureLeavesGatewayAsItWas(t *testing.T) {
	for _, op := range []string{"plan", "key", "attach"} {
		t.Run(op, func(t *testing.T) {
			gw := newFakeGateway()
			gw.failOn = op
			svc, store := newService(gw)
			v, _ := svc.Register(context.Background(), request("ten_a", "Banco A"), analyst)
			_, err := svc.Approve(context.Background(), v.Partner.ID, analyst)
			require.ErrorIs(t, err, domain.ErrGatewayUnavailable)
			assert.Empty(t, gw.plans)
			assert.Empty(t, gw.keys)
			assert.Equal(t, domain.StatusPendingApproval, store.partners[v.Partner.ID].Partner.Status)
			assert.Empty(t, store.outbox)
		})
	}
	t.Run("database", func(t *testing.T) {
		gw := newFakeGateway()
		svc, store := newService(gw)
		v, _ := svc.Register(context.Background(), request("ten_a", "Banco A"), analyst)
		store.failNext = errDB
		_, err := svc.Approve(context.Background(), v.Partner.ID, analyst)
		require.ErrorIs(t, err, errDB)
		assert.Empty(t, gw.plans)
		assert.Empty(t, gw.keys)
	})
	t.Run("no gateway configured", func(t *testing.T) {
		svc, _ := newService(nil)
		v, _ := svc.Register(context.Background(), request("ten_a", "Banco A"), analyst)
		_, err := svc.Approve(context.Background(), v.Partner.ID, analyst)
		require.ErrorIs(t, err, domain.ErrGatewayUnavailable)
	})
}

func TestListWithUsageAndIndicators(t *testing.T) {
	gw := newFakeGateway()
	svc, _ := newService(gw)
	ids := map[string]string{}
	for _, p := range [][2]string{{"ten_b", "Banco"}, {"ten_a", "Aerolinea"}, {"ten_p", "Pendiente"}} {
		v, err := svc.Register(context.Background(), request(p[0], p[1]), analyst)
		require.NoError(t, err)
		ids[p[0]] = v.Partner.ID
	}
	for _, code := range []string{"ten_a", "ten_b"} {
		a, err := svc.Approve(context.Background(), ids[code], analyst)
		require.NoError(t, err)
		used := int64(100)
		if code == "ten_a" {
			used = 950
		}
		gw.usage[a.View.Credentials[0].GatewayKeyID] = used
	}

	views, ind, err := svc.List(context.Background())
	require.NoError(t, err)
	require.Len(t, views, 3)
	assert.Equal(t, "Aerolinea", views[0].Partner.Name)
	assert.Equal(t, "Pendiente", views[2].Partner.Name)
	assert.Equal(t, 95.0, views[0].Usage.Percent)
	assert.Equal(t, domain.UsageExceeded, domain.StatusOf(&domain.Usage{Percent: 100}))
	assert.Equal(t, domain.UsageNearLimit, views[0].UsageStatus())
	assert.Equal(t, domain.UsageNormal, views[1].UsageStatus())
	assert.Nil(t, views[2].Usage)
	assert.Equal(t, 2, ind.ActivePartners)
	assert.Equal(t, 52.5, *ind.AverageQuotaUsedPercent)
	assert.Equal(t, []string{"Aerolinea"}, ind.NearLimitNames)

	gw.usageErr = errBoom
	views, ind, err = svc.List(context.Background())
	require.NoError(t, err, "the gateway does not break the list")
	assert.Nil(t, views[0].Usage)
	assert.Equal(t, domain.UsageUnknown, views[0].UsageStatus())
	assert.Nil(t, ind.AverageQuotaUsedPercent)
}

func TestGetUnknownPartner(t *testing.T) {
	svc, _ := newService(newFakeGateway())
	_, err := svc.Get(context.Background(), "ptn-x")
	require.ErrorIs(t, err, domain.ErrPartnerNotFound)
	_, err = svc.Get(context.Background(), "")
	require.ErrorIs(t, err, domain.ErrPartnerNotFound)
}
