// Package integration exercises the notifications service end to end over a
// real PostgreSQL (testcontainers): the event of an automatic payment, the
// notification center and the pushes with their retries (HU-M3.1-01).
package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	pushclient "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/push"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/adapters/messaging"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/adapters/pushprovider"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/application"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/push"
)

var (
	containerOnce sync.Once
	container     *tcpostgres.PostgresContainer
	adminURL      string
	containerErr  error
	databases     atomic.Int64
)

func TestMain(m *testing.M) {
	code := m.Run()
	if container != nil {
		_ = testcontainers.TerminateContainer(container)
	}
	os.Exit(code)
}

func requireDocker(t *testing.T) {
	t.Helper()
	provider, err := testcontainers.NewDockerProvider()
	if err == nil {
		defer func() { _ = provider.Close() }()
		err = provider.Health(context.Background())
	}
	if err == nil {
		return
	}
	if os.Getenv("REQUIRE_DOCKER") == "1" {
		t.Fatalf("Docker is required (REQUIRE_DOCKER=1) but is not healthy: %v", err)
	}
	t.Skipf("Docker is not available: %v", err)
}

// provider is the push provider: it fails the first failFirst sends with a
// 503 and records every message it accepts, with when it got it.
type provider struct {
	srv       *httptest.Server
	failFirst int64
	calls     atomic.Int64
	mu        sync.Mutex
	accepted  map[string]acceptedPush
}

type acceptedPush struct {
	push pushclient.Message
	at   time.Time
}

func newProvider(t *testing.T, failFirst int64) *provider {
	t.Helper()
	p := &provider{failFirst: failFirst, accepted: map[string]acceptedPush{}}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p.calls.Add(1) <= p.failFirst {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		var m pushclient.Message
		_ = json.NewDecoder(r.Body).Decode(&m)
		m.IdempotencyKey = r.Header.Get("Idempotency-Key")
		p.mu.Lock()
		if _, dup := p.accepted[m.IdempotencyKey]; !dup {
			p.accepted[m.IdempotencyKey] = acceptedPush{push: m, at: time.Now()}
		}
		p.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"push-` + m.IdempotencyKey + `"}`))
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *provider) pushes() map[string]acceptedPush {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]acceptedPush, len(p.accepted))
	for k, v := range p.accepted {
		out[k] = v
	}
	return out
}

// env is the service over its own database, with the worker running.
type env struct {
	svc      *application.Service
	handler  events.Handler
	provider *provider
}

func newEnv(t *testing.T, failFirst int64) *env {
	t.Helper()
	requireDocker(t)
	ctx := context.Background()
	containerOnce.Do(func() {
		container, containerErr = tcpostgres.Run(ctx, "postgres:16-alpine",
			tcpostgres.WithDatabase("notifications"), tcpostgres.WithUsername("notifications"),
			tcpostgres.WithPassword("notifications"),
			testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(2*time.Minute)))
		if containerErr == nil {
			adminURL, containerErr = container.ConnectionString(ctx, "sslmode=disable")
		}
	})
	require.NoError(t, containerErr, "start PostgreSQL")
	admin, err := postgres.Open(ctx, adminURL)
	require.NoError(t, err)
	defer func() { _ = admin.Close() }()
	name := "it_" + strings.ReplaceAll(t.Name(), "/", "_")
	name = strings.ToLower(name[:min(len(name), 40)]) + "_" + string(rune('a'+databases.Add(1)%26))
	_, err = admin.ExecContext(ctx, "CREATE DATABASE "+name)
	require.NoError(t, err)
	db, err := postgres.Open(ctx, strings.Replace(adminURL, "/notifications?", "/"+name+"?", 1))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, postgres.Migrate(ctx, db))

	store := postgres.NewStore(db)
	p := newProvider(t, failFirst)
	client := pushclient.NewClient(pushclient.Config{BaseURL: p.srv.URL, Timeout: time.Second,
		Breaker: resilience.Config{ConsecutiveFailures: 1000, OpenTimeout: time.Second, HalfOpenSuccesses: 1}})
	worker, err := push.NewWorker(store, pushprovider.NewSender(client), push.Config{
		PollInterval: 50 * time.Millisecond, RetryBase: 50 * time.Millisecond, RetryMax: 200 * time.Millisecond,
	})
	require.NoError(t, err)
	runCtx, cancel := context.WithCancel(ctx)
	t.Cleanup(cancel)
	go worker.Run(runCtx)

	svc, err := application.NewService(store, clock{}, worker.Wake)
	require.NoError(t, err)
	return &env{svc: svc, handler: messaging.NewParametricPayoutHandler(svc), provider: p}
}

type clock struct{}

func (clock) Now() time.Time { return time.Now() }

func asInsured(subject string) context.Context {
	return security.WithClaims(context.Background(), security.Claims{Subject: subject, Roles: []security.Role{security.RoleInsured}})
}

// publish delivers ParametricPayoutCompleted as the queue would, for the
// payment of the prototype: $ 180.000 COP for the flight AV-8421.
func (e *env) publish(t *testing.T, payoutID, flight string, paidAt time.Time) {
	t.Helper()
	ev, err := events.New("parametric-payout-completed-"+payoutID, messaging.EventTypeParametricPayoutCompleted, "claims", 1,
		paidAt, map[string]any{
			"payout_id": payoutID, "claim_id": "SIN-2026-01702", "policy_id": "VJ-2026-000931", "subject": "sub-a",
			"amount_minor": 18_000_000, "currency": "COP",
			"trigger": map[string]any{"parameter": "flight_delay", "event_reference": flight, "threshold": 180, "observed": 215,
				"unit": "minutes", "observed_at": paidAt.Add(-time.Minute)},
			"payment_method": map[string]any{"brand": "visa", "last4": "1111"}, "idempotency_key": "pay-" + payoutID,
			"paid_at": paidAt,
		})
	require.NoError(t, err)
	require.NoError(t, e.handler.Handle(context.Background(), ev))
}

func (e *env) registerDevice(t *testing.T, installation string, pushEnabled bool) {
	t.Helper()
	token := ""
	if pushEnabled {
		token = "token-" + installation
	}
	_, err := e.svc.RegisterDevice(asInsured("sub-a"), domain.Device{InstallationID: installation, Platform: "android",
		Token: token, PushEnabled: pushEnabled, Locale: domain.LocaleSpanish})
	require.NoError(t, err)
}

// Criteria 1 and 2: the push leaves within 60 s of the payment with the
// amount, the currency and the event, and the notification stays in the
// center.
func TestThePushLeavesWithin60SecondsAndStaysInTheCenter(t *testing.T) {
	e := newEnv(t, 0)
	e.registerDevice(t, "inst-1", true)
	paidAt := time.Now().UTC()

	e.publish(t, "ppo-1", "AV-8421", paidAt)

	require.Eventually(t, func() bool { return len(e.provider.pushes()) == 1 }, 60*time.Second, 20*time.Millisecond)
	got := e.provider.pushes()["ntf-ppo-1:inst-1"]
	assert.Less(t, got.at.Sub(paidAt), 60*time.Second)
	assert.Equal(t, "Pago automático recibido: $ 180.000 COP", got.push.Title)
	assert.Contains(t, got.push.Body, "AV-8421")
	assert.Equal(t, "ntf-ppo-1", got.push.Data["notification_id"], "criterion 4: what to open")

	items, unread, err := e.svc.List(asInsured("sub-a"), 0)
	require.NoError(t, err)
	require.Len(t, items, 1)
	assert.Equal(t, 1, unread)
	assert.Equal(t, int64(18_000_000), items[0].Payout.AmountMinor)
}

// Criterion 7: the provider fails, the push is retried until it arrives, and
// the notification is in the center from the start.
func TestAFailingProviderIsRetriedAndTheCenterHasTheNotificationMeanwhile(t *testing.T) {
	e := newEnv(t, 3)
	e.registerDevice(t, "inst-1", true)

	e.publish(t, "ppo-1", "AV-8421", time.Now().UTC())

	_, err := e.svc.Get(asInsured("sub-a"), "ntf-ppo-1")
	require.NoError(t, err, "in the center before the push arrives")
	require.Eventually(t, func() bool { return len(e.provider.pushes()) == 1 }, 30*time.Second, 20*time.Millisecond)
	assert.GreaterOrEqual(t, e.provider.calls.Load(), int64(4))
}

// Criterion 5: without the permission no push goes out and the notification
// waits in the center for the next time the insured opens the app.
func TestWithoutThePermissionTheNotificationWaitsInTheCenter(t *testing.T) {
	e := newEnv(t, 0)
	e.registerDevice(t, "inst-1", false)

	e.publish(t, "ppo-1", "AV-8421", time.Now().UTC())

	time.Sleep(300 * time.Millisecond)
	assert.Empty(t, e.provider.pushes())
	items, _, err := e.svc.List(asInsured("sub-a"), 0)
	require.NoError(t, err)
	assert.Len(t, items, 1)
}

// Criterion 6: several payments the same day are all kept and all pushed,
// each with its own key, in the same group; a redelivered event changes
// nothing.
func TestSeveralPaymentsTheSameDayAreAllKept(t *testing.T) {
	e := newEnv(t, 0)
	e.registerDevice(t, "inst-1", true)
	start := time.Now().UTC()

	for i, flight := range []string{"AV-8421", "LA-4012", "AV-9310"} {
		e.publish(t, "ppo-"+flight, flight, start.Add(time.Duration(i)*time.Minute))
	}
	e.publish(t, "ppo-AV-8421", "AV-8421", start)

	require.Eventually(t, func() bool { return len(e.provider.pushes()) == 3 }, 30*time.Second, 20*time.Millisecond)
	for _, p := range e.provider.pushes() {
		assert.Equal(t, domain.GroupParametricPayouts, p.push.Group)
	}
	items, unread, err := e.svc.List(asInsured("sub-a"), 0)
	require.NoError(t, err)
	assert.Len(t, items, 3)
	assert.Equal(t, 3, unread)
	assert.Equal(t, "ntf-ppo-AV-9310", items[0].ID, "the newest first")
	time.Sleep(200 * time.Millisecond)
	assert.Len(t, e.provider.pushes(), 3, "the redelivered event sent no second push")
}
