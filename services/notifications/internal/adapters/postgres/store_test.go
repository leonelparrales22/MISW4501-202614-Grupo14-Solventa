package postgres_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/ports"
)

// One PostgreSQL container for the package, started by the first test that
// needs it; every test gets its own database in it.
var (
	containerOnce sync.Once
	container     *tcpostgres.PostgresContainer
	adminURL      string
	containerErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if container != nil {
		_ = testcontainers.TerminateContainer(container)
	}
	os.Exit(code)
}

// requireDocker fails the test when REQUIRE_DOCKER=1 and Docker is not
// healthy, and skips it otherwise.
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

// newStore returns a store over a migrated, empty database of its own.
func newStore(t *testing.T) (*postgres.Store, *sql.DB) {
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
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	name := "t_" + hex.EncodeToString(b)
	_, err = admin.ExecContext(ctx, "CREATE DATABASE "+name)
	require.NoError(t, err)
	db, err := postgres.Open(ctx, strings.Replace(adminURL, "/notifications?", "/"+name+"?", 1))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, postgres.Migrate(ctx, db))
	require.NoError(t, postgres.Migrate(ctx, db), "migrating twice changes nothing")
	return postgres.NewStore(db), db
}

var paidAt = time.Date(2026, 9, 11, 21, 40, 0, 0, time.UTC)

func payout(id string) domain.ParametricPayout {
	return domain.ParametricPayout{
		PayoutID: id, ClaimID: "SIN-2026-01702", PolicyID: "VJ-2026-000931", Subject: "sub-a", AmountMinor: 18_000_000,
		Currency: "COP",
		Trigger: domain.Trigger{Parameter: "flight_delay", EventReference: "AV-8421", Threshold: 180, Observed: 215,
			Unit: "minutes", ObservedAt: paidAt.Add(-time.Minute)},
		Method: domain.PaymentMethod{Brand: "visa", Last4: "1111"}, IdempotencyKey: "pay-" + id, PaidAt: paidAt,
	}
}

func device(id string, pushEnabled bool) domain.Device {
	token := ""
	if pushEnabled {
		token = "token-" + id
	}
	return domain.Device{InstallationID: id, Subject: "sub-a", Platform: "android", Token: token, PushEnabled: pushEnabled,
		Locale: domain.LocaleSpanish, UpdatedAt: paidAt}
}

func TestRecordPayoutCreatesOneDeliveryPerReachableDeviceAndIsIdempotent(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	require.NoError(t, store.SaveDevice(ctx, device("inst-1", true)))
	require.NoError(t, store.SaveDevice(ctx, device("inst-2", false)))
	n := domain.NewPayoutNotification(payout("ppo-1"), paidAt.Add(2*time.Second))

	created, ids, err := store.RecordPayout(ctx, n, paidAt.Add(2*time.Second))
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, []string{"ntf-ppo-1:inst-1"}, ids, "a device without the permission gets no push")

	created, again, err := store.RecordPayout(ctx, n, paidAt.Add(time.Minute))
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, ids, again)

	got, err := store.Get(ctx, "sub-a", n.ID)
	require.NoError(t, err)
	assert.Equal(t, n.Payout, got.Payout, "the payout survives the round trip")
	assert.Nil(t, got.ReadAt)
}

func TestTheCenterListsEveryPayoutTheNewestFirstAndCountsTheUnread(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	for i, id := range []string{"ppo-1", "ppo-2", "ppo-3"} {
		p := payout(id)
		p.PaidAt = paidAt.Add(time.Duration(i) * time.Hour)
		_, _, err := store.RecordPayout(ctx, domain.NewPayoutNotification(p, p.PaidAt), p.PaidAt)
		require.NoError(t, err)
	}
	other := payout("ppo-x")
	other.Subject = "sub-b"
	_, _, err := store.RecordPayout(ctx, domain.NewPayoutNotification(other, paidAt), paidAt)
	require.NoError(t, err)

	items, unread, err := store.List(ctx, "sub-a", 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"ntf-ppo-3", "ntf-ppo-2", "ntf-ppo-1"}, idsOf(items), "none is lost or overwritten (criterion 6)")
	assert.Equal(t, 3, unread)

	limited, _, err := store.List(ctx, "sub-a", 2)
	require.NoError(t, err)
	assert.Len(t, limited, 2)

	require.NoError(t, store.MarkRead(ctx, "sub-a", "ntf-ppo-1", paidAt.Add(5*time.Hour)))
	require.NoError(t, store.MarkRead(ctx, "sub-a", "ntf-ppo-1", paidAt.Add(6*time.Hour)))
	read, err := store.Get(ctx, "sub-a", "ntf-ppo-1")
	require.NoError(t, err)
	require.NotNil(t, read.ReadAt)
	assert.Equal(t, paidAt.Add(5*time.Hour), *read.ReadAt, "reading again keeps the first time")

	require.NoError(t, store.MarkAllRead(ctx, "sub-a", paidAt.Add(7*time.Hour)))
	_, unread, err = store.List(ctx, "sub-a", 10)
	require.NoError(t, err)
	assert.Zero(t, unread)
}

func TestAnotherInsuredsNotificationLooksLikeOneThatDoesNotExist(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	_, _, err := store.RecordPayout(ctx, domain.NewPayoutNotification(payout("ppo-1"), paidAt), paidAt)
	require.NoError(t, err)

	_, err = store.Get(ctx, "sub-b", "ntf-ppo-1")
	require.ErrorIs(t, err, ports.ErrNotificationNotFound)
	require.ErrorIs(t, store.MarkRead(ctx, "sub-b", "ntf-ppo-1", paidAt), ports.ErrNotificationNotFound)
}

func TestADeviceMovesToTheAccountThatRegistersItLast(t *testing.T) {
	store, db := newStore(t)
	ctx := context.Background()
	require.NoError(t, store.SaveDevice(ctx, device("inst-1", true)))
	moved := device("inst-1", false)
	moved.Subject = "sub-b"
	require.NoError(t, store.SaveDevice(ctx, moved))

	var subject string
	var enabled bool
	require.NoError(t, db.QueryRowContext(ctx, `SELECT subject, push_enabled FROM device WHERE installation_id = 'inst-1'`).
		Scan(&subject, &enabled))
	assert.Equal(t, "sub-b", subject)
	assert.False(t, enabled)
}

func TestDeliveriesFollowTheClaimProtocol(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	require.NoError(t, store.SaveDevice(ctx, device("inst-1", true)))
	now := paidAt.Add(time.Second)
	_, ids, err := store.RecordPayout(ctx, domain.NewPayoutNotification(payout("ppo-1"), now), now)
	require.NoError(t, err)
	id := ids[0]

	due, err := store.Due(ctx, now, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{id}, due)

	rec, claimed, err := store.Claim(ctx, id, now, 10*time.Second)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Equal(t, idempotency.StatusInProgress, rec.Status)
	assert.Equal(t, "ntf-ppo-1:inst-1", rec.Key)
	assert.Equal(t, "token-inst-1", rec.Token)
	require.NotNil(t, rec.Notification.Payout, "the claim loads what the push tells")
	assert.Equal(t, "AV-8421", rec.Notification.Payout.Trigger.EventReference)
	_, again, err := store.Claim(ctx, id, now, 10*time.Second)
	require.NoError(t, err)
	assert.False(t, again, "a held delivery is not claimed twice")

	rec, err = store.Reschedule(ctx, rec, now, now.Add(2*time.Second), "provider down")
	require.NoError(t, err)
	assert.Equal(t, idempotency.StatusPending, rec.Status)
	assert.Equal(t, 1, rec.FailedAttempts)
	assert.Equal(t, "provider down", rec.LastError)
	due, err = store.Due(ctx, now.Add(time.Second), 10)
	require.NoError(t, err)
	assert.Empty(t, due, "not due before its next attempt")

	rec, claimed, err = store.Claim(ctx, id, now.Add(3*time.Second), 10*time.Second)
	require.NoError(t, err)
	require.True(t, claimed)
	confirmed, err := store.Confirm(ctx, rec, now.Add(3*time.Second), idempotency.Outcome{Reference: "push-1"})
	require.NoError(t, err)
	assert.Equal(t, idempotency.StatusConfirmed, confirmed.Status)
	assert.Equal(t, "push-1", confirmed.Reference)
	require.NotNil(t, confirmed.SentAt)

	_, err = store.Fail(ctx, rec, now.Add(4*time.Second), idempotency.ReasonRejected, "late")
	require.ErrorIs(t, err, idempotency.ErrClaimLost, "a finished delivery is no longer held")
}

func TestAFailedDeliveryKeepsItsNotification(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	require.NoError(t, store.SaveDevice(ctx, device("inst-1", true)))
	_, ids, err := store.RecordPayout(ctx, domain.NewPayoutNotification(payout("ppo-1"), paidAt), paidAt)
	require.NoError(t, err)
	rec, _, err := store.Claim(ctx, ids[0], paidAt, time.Second)
	require.NoError(t, err)

	failed, err := store.Fail(ctx, rec, paidAt, idempotency.ReasonWindowExpired, "gave up")
	require.NoError(t, err)
	assert.Equal(t, idempotency.StatusFailed, failed.Status)

	items, _, err := store.List(ctx, "sub-a", 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"ntf-ppo-1"}, idsOf(items), "criterion 7")
}

func TestAnExpiredLeaseCanBeClaimedAgain(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()
	require.NoError(t, store.SaveDevice(ctx, device("inst-1", true)))
	_, ids, err := store.RecordPayout(ctx, domain.NewPayoutNotification(payout("ppo-1"), paidAt), paidAt)
	require.NoError(t, err)
	first, _, err := store.Claim(ctx, ids[0], paidAt, time.Second)
	require.NoError(t, err)

	_, claimed, err := store.Claim(ctx, ids[0], paidAt.Add(2*time.Second), time.Second)
	require.NoError(t, err)
	assert.True(t, claimed)
	_, err = store.Confirm(ctx, first, paidAt.Add(2*time.Second), idempotency.Outcome{})
	require.ErrorIs(t, err, idempotency.ErrClaimLost, "the first claim lost its lease")
}

func idsOf(items []domain.Notification) []string {
	out := make([]string, 0, len(items))
	for _, n := range items {
		out = append(out, n.ID)
	}
	return out
}
