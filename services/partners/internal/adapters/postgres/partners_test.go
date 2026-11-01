package postgres_test

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
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

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/events"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/ports"
)

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

// requireDocker fails with REQUIRE_DOCKER=1 and skips otherwise when Docker
// is not healthy (same rule as audit).
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

func newDB(t *testing.T) *sql.DB {
	t.Helper()
	requireDocker(t)
	ctx := context.Background()
	containerOnce.Do(func() {
		container, containerErr = tcpostgres.Run(ctx, "postgres:16-alpine",
			tcpostgres.WithDatabase("partners"), tcpostgres.WithUsername("partners"), tcpostgres.WithPassword("partners"),
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
	db, err := postgres.Open(ctx, strings.Replace(adminURL, "/partners?", "/"+name+"?", 1))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, postgres.Migrate(ctx, db))
	require.NoError(t, postgres.Migrate(ctx, db), "migrating twice changes nothing")
	return db
}

var (
	t0  = time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC)
	ops = domain.Actor{Type: "operations", ID: "sub-1", Label: "andres@solventa.co"}
)

func partner(id, code string) domain.Partner {
	return domain.Partner{ID: id, Code: code, Name: "Banco " + code, Type: domain.TypeBank, Products: []string{"hogar"},
		TechnicalContactEmail: "api@b.co", Status: domain.StatusPendingApproval,
		Quota: domain.Quota{DailyLimit: 100, RateLimit: 10, BurstLimit: 20}, RegisteredAt: t0, RegisteredBy: ops}
}

func TestPartnerLifecycle(t *testing.T) {
	db := newDB(t)
	ctx := context.Background()
	s := postgres.NewPartnerStore(db)

	require.NoError(t, s.Create(ctx, partner("ptn-1", "ten_a"), domain.HistoryEntry{ID: "pev-1", PartnerID: "ptn-1",
		Type: domain.HistoryRegistered, OccurredAt: t0, Actor: ops}))
	require.ErrorIs(t, s.Create(ctx, partner("ptn-2", "ten_a"), domain.HistoryEntry{ID: "pev-2", PartnerID: "ptn-2",
		Type: domain.HistoryRegistered, OccurredAt: t0, Actor: ops}), domain.ErrCodeTaken)
	_, err := s.Get(ctx, "ptn-x")
	require.ErrorIs(t, err, domain.ErrPartnerNotFound)

	// Approve: partner active, credential and event in one transaction.
	cred := domain.Credential{ID: "crd-1", PartnerID: "ptn-1", ClientID: "sol_1", GatewayKeyID: "key-1", SecretHint: "abcd",
		Status: domain.CredentialActive, IssuedAt: t0, IssuedBy: ops, Reason: domain.ReasonApproval}
	ev, err := events.New("partner-credentials-issued-crd-1", domain.EventCredentialsIssued, "partners", 1, t0,
		map[string]string{"partner_id": "ptn-1"})
	require.NoError(t, err)
	require.NoError(t, s.Mutate(ctx, "ptn-1", func(rec ports.PartnerRecord) (ports.Change, error) {
		p := rec.Partner
		p.Status, p.UsagePlanID, p.ApprovedAt = domain.StatusActive, "plan-1", &t0
		return ports.Change{Partner: &p, NewCredentials: []domain.Credential{cred}, Events: []events.Envelope{ev},
			History: []domain.HistoryEntry{{ID: "pev-3", PartnerID: "ptn-1", Type: domain.HistoryCredentialsIssued,
				OccurredAt: t0, Actor: ops, Details: map[string]any{"client_id": "sol_1"}}}}, nil
	}))

	// A failing mutation writes nothing.
	boom := errors.New("boom")
	require.ErrorIs(t, s.Mutate(ctx, "ptn-1", func(ports.PartnerRecord) (ports.Change, error) { return ports.Change{}, boom }), boom)

	// Rotate: the old one retiring, a new active one.
	exp := t0.Add(time.Hour)
	require.NoError(t, s.Mutate(ctx, "ptn-1", func(rec ports.PartnerRecord) (ports.Change, error) {
		old := rec.Credentials[0]
		old.Status, old.ExpiresAt = domain.CredentialRetiring, &exp
		next := cred
		next.ID, next.ClientID, next.GatewayKeyID, next.Reason, next.IssuedAt = "crd-2", "sol_2", "key-2", domain.ReasonRotation, t0.Add(time.Minute)
		return ports.Change{UpdatedCredentials: []domain.Credential{old}, NewCredentials: []domain.Credential{next}}, nil
	}))

	rec, err := s.Get(ctx, "ptn-1")
	require.NoError(t, err)
	assert.Equal(t, domain.StatusActive, rec.Partner.Status)
	assert.Equal(t, "plan-1", rec.Partner.UsagePlanID)
	require.Len(t, rec.Credentials, 2)
	assert.Equal(t, "crd-2", rec.Credentials[0].ID)
	assert.Equal(t, domain.CredentialRetiring, rec.Credentials[1].Status)
	assert.Equal(t, exp, *rec.Credentials[1].ExpiresAt)
	require.Len(t, rec.History, 2)
	assert.Equal(t, "sol_1", rec.History[0].Details["client_id"])

	due, err := s.DueRetirements(ctx, t0, 10)
	require.NoError(t, err)
	assert.Empty(t, due)
	due, err = s.DueRetirements(ctx, exp, 10)
	require.NoError(t, err)
	assert.Equal(t, []string{"ptn-1"}, due)

	list, err := s.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Len(t, list[0].Credentials, 2)

	// The history is append-only.
	_, err = db.ExecContext(ctx, `UPDATE partner_history SET actor_label = 'x'`)
	require.Error(t, err)
	_, err = db.ExecContext(ctx, `DELETE FROM partner_history`)
	require.Error(t, err)

	// The event is in the outbox and the relay can claim it.
	msgs, err := postgres.NewOutboxStore(db).Claim(ctx, t0.Add(time.Second), 30*time.Second, 10)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, domain.EventCredentialsIssued, msgs[0].EventType)
	require.NoError(t, postgres.NewOutboxStore(db).MarkPublished(ctx, msgs[0], t0))
}
