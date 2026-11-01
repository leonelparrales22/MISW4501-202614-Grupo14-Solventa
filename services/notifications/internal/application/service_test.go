package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/application"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/notifications/internal/ports"
)

var now = time.Date(2026, 9, 11, 21, 40, 2, 0, time.UTC)

type fixedClock struct{}

func (fixedClock) Now() time.Time { return now }

// fakeStore keeps what the service writes, in memory.
type fakeStore struct {
	recorded   []domain.Notification
	created    bool
	deliveries []string
	err        error
	listLimit  int
	subject    string
	readID     string
	allRead    bool
	devices    []domain.Device
}

func (f *fakeStore) RecordPayout(_ context.Context, n domain.Notification, _ time.Time) (bool, []string, error) {
	f.recorded = append(f.recorded, n)
	return f.created, f.deliveries, f.err
}

func (f *fakeStore) List(_ context.Context, subject string, limit int) ([]domain.Notification, int, error) {
	f.subject, f.listLimit = subject, limit
	return f.recorded, len(f.recorded), f.err
}

func (f *fakeStore) Get(_ context.Context, subject, id string) (domain.Notification, error) {
	f.subject = subject
	for _, n := range f.recorded {
		if n.ID == id && n.Subject == subject {
			return n, nil
		}
	}
	return domain.Notification{}, ports.ErrNotificationNotFound
}

func (f *fakeStore) MarkRead(_ context.Context, subject, id string, _ time.Time) error {
	f.subject, f.readID = subject, id
	return f.err
}

func (f *fakeStore) MarkAllRead(_ context.Context, subject string, _ time.Time) error {
	f.subject, f.allRead = subject, true
	return f.err
}

func (f *fakeStore) SaveDevice(_ context.Context, d domain.Device) error {
	f.devices = append(f.devices, d)
	return f.err
}

func asInsured(subject string) context.Context {
	return security.WithClaims(context.Background(), security.Claims{Subject: subject, Roles: []security.Role{security.RoleInsured}})
}

func payout() domain.ParametricPayout {
	return domain.ParametricPayout{PayoutID: "ppo-1", Subject: "sub-a", AmountMinor: 18_000_000, Currency: "COP",
		Trigger: domain.Trigger{Parameter: "flight_delay", EventReference: "AV-8421"}, PaidAt: now.Add(-2 * time.Second)}
}

func newService(t *testing.T, store *fakeStore, wake func()) *application.Service {
	t.Helper()
	svc, err := application.NewService(store, fixedClock{}, wake)
	require.NoError(t, err)
	return svc
}

func TestNewServiceNeedsAStoreAndAClock(t *testing.T) {
	_, err := application.NewService(nil, fixedClock{}, nil)
	require.Error(t, err)
	_, err = application.NewService(&fakeStore{}, nil, nil)
	require.Error(t, err)
}

func TestRecordPayoutStoresTheNotificationAndWakesTheWorker(t *testing.T) {
	store := &fakeStore{created: true, deliveries: []string{"ntf-ppo-1:inst-1"}}
	woken := 0
	svc := newService(t, store, func() { woken++ })

	require.NoError(t, svc.RecordPayout(context.Background(), payout()))

	require.Len(t, store.recorded, 1)
	assert.Equal(t, "ntf-ppo-1", store.recorded[0].ID)
	assert.Equal(t, now, store.recorded[0].CreatedAt)
	assert.Equal(t, 1, woken)
}

func TestWithoutDevicesTheNotificationWaitsInTheCenter(t *testing.T) {
	store := &fakeStore{created: true}
	woken := 0
	svc := newService(t, store, func() { woken++ })

	require.NoError(t, svc.RecordPayout(context.Background(), payout()))

	assert.Len(t, store.recorded, 1, "criterion 5: recorded although no push goes out")
	assert.Zero(t, woken)
}

func TestARedeliveredPayoutChangesNothing(t *testing.T) {
	store := &fakeStore{created: false, deliveries: []string{"ntf-ppo-1:inst-1"}}
	woken := 0
	svc := newService(t, store, func() { woken++ })

	require.NoError(t, svc.RecordPayout(context.Background(), payout()))

	assert.Zero(t, woken, "no second push")
}

func TestAnInvalidPayoutOrAStoreFailureIsAnError(t *testing.T) {
	svc := newService(t, &fakeStore{}, nil)
	bad := payout()
	bad.Currency = ""
	require.ErrorIs(t, svc.RecordPayout(context.Background(), bad), domain.ErrInvalidPayout)

	boom := errors.New("database down")
	svc = newService(t, &fakeStore{err: boom}, nil)
	require.ErrorIs(t, svc.RecordPayout(context.Background(), payout()), boom)
}

func TestTheCenterIsAlwaysTheCallers(t *testing.T) {
	store := &fakeStore{}
	svc := newService(t, store, nil)
	n := domain.NewPayoutNotification(payout(), now)
	store.recorded = []domain.Notification{n}

	items, unread, err := svc.List(asInsured("sub-a"), 0)
	require.NoError(t, err)
	assert.Len(t, items, 1)
	assert.Equal(t, 1, unread)
	assert.Equal(t, "sub-a", store.subject)
	assert.Equal(t, application.DefaultListLimit, store.listLimit)

	_, _, err = svc.List(asInsured("sub-a"), 500)
	require.NoError(t, err)
	assert.Equal(t, application.DefaultListLimit, store.listLimit)
	_, _, err = svc.List(asInsured("sub-a"), 20)
	require.NoError(t, err)
	assert.Equal(t, 20, store.listLimit)

	got, err := svc.Get(asInsured("sub-a"), "ntf-ppo-1")
	require.NoError(t, err)
	assert.Equal(t, n.ID, got.ID)
	_, err = svc.Get(asInsured("sub-b"), "ntf-ppo-1")
	require.ErrorIs(t, err, ports.ErrNotificationNotFound)

	require.NoError(t, svc.MarkRead(asInsured("sub-a"), "ntf-ppo-1"))
	assert.Equal(t, "ntf-ppo-1", store.readID)
	require.NoError(t, svc.MarkAllRead(asInsured("sub-a")))
	assert.True(t, store.allRead)
}

func TestEveryReadNeedsTheCaller(t *testing.T) {
	svc := newService(t, &fakeStore{}, nil)
	ctx := context.Background()

	_, _, err := svc.List(ctx, 0)
	require.ErrorIs(t, err, security.ErrUnauthenticated)
	_, err = svc.Get(ctx, "x")
	require.ErrorIs(t, err, security.ErrUnauthenticated)
	require.ErrorIs(t, svc.MarkRead(ctx, "x"), security.ErrUnauthenticated)
	require.ErrorIs(t, svc.MarkAllRead(ctx), security.ErrUnauthenticated)
	_, err = svc.RegisterDevice(ctx, domain.Device{})
	require.ErrorIs(t, err, security.ErrUnauthenticated)
}

func TestRegisterDeviceKeepsTheTokenOnlyWithThePermission(t *testing.T) {
	store := &fakeStore{}
	svc := newService(t, store, nil)

	d, err := svc.RegisterDevice(asInsured("sub-a"), domain.Device{InstallationID: "inst-1", Platform: "android",
		Token: "tok", PushEnabled: false, Locale: "es"})
	require.NoError(t, err)
	assert.Equal(t, "sub-a", d.Subject)
	assert.Empty(t, d.Token, "without the permission the token is not kept")
	assert.Equal(t, now, d.UpdatedAt)

	_, err = svc.RegisterDevice(asInsured("sub-a"), domain.Device{InstallationID: "inst-1", Platform: "web", Locale: "es"})
	require.ErrorIs(t, err, domain.ErrInvalidDevice)
	assert.Len(t, store.devices, 1)
}
