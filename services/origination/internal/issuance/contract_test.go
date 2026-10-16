package issuance_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/links"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/issuance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

var activation = time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)

type steppedClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *steppedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *steppedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type fakeGenerator struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (g *fakeGenerator) Generate(_ context.Context, d domain.ContractData) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls++
	if g.err != nil {
		return nil, g.err
	}
	return []byte("%PDF-fake " + d.PolicyID), nil
}

type fakeStore struct {
	mu   sync.Mutex
	docs map[string][]byte
	puts int
	err  error
}

func newFakeStore() *fakeStore { return &fakeStore{docs: map[string][]byte{}} }

func (s *fakeStore) Put(_ context.Context, key string, content []byte, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.puts++
	if s.err != nil {
		return s.err
	}
	s.docs[key] = content
	return nil
}

func (s *fakeStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.docs[key]
	if !ok {
		return nil, ports.ErrDocumentNotFound
	}
	return c, nil
}

type fakeNotifier struct {
	mu       sync.Mutex
	sent     []domain.IssuedNotification
	failures int
}

func (n *fakeNotifier) Notify(_ context.Context, in domain.IssuedNotification) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.failures > 0 {
		n.failures--
		return errors.New("provider down")
	}
	n.sent = append(n.sent, in)
	return nil
}

func (n *fakeNotifier) count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.sent)
}

type contractEnv struct {
	svc      *issuance.ContractService
	reader   *issuance.ContractReader
	clock    *steppedClock
	repo     *memory.ContractStore
	audit    *memory.IssuanceAuditLog
	gen      *fakeGenerator
	store    *fakeStore
	notifier *fakeNotifier
	signer   *links.Signer
}

func newContractEnv(t *testing.T, cfg issuance.ContractConfig) *contractEnv {
	t.Helper()
	signer, err := links.NewSigner("0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	e := &contractEnv{
		clock: &steppedClock{t: activation}, repo: memory.NewContractStore(), audit: memory.NewIssuanceAuditLog(),
		gen: &fakeGenerator{}, store: newFakeStore(), notifier: &fakeNotifier{}, signer: signer,
	}
	if cfg.PublicBaseURL == "" {
		cfg.PublicBaseURL = "https://api.solventa.example/"
	}
	e.svc = issuance.NewContractService(issuance.ContractDeps{
		Repo: e.repo, Generator: e.gen, Store: e.store, Signer: signer, Notifier: e.notifier, Audit: e.audit, Clock: e.clock,
	}, cfg)
	e.reader = issuance.NewContractReader(e.repo, e.store, e.audit, signer, e.clock)
	return e
}

func event() domain.CoverageActivated {
	return domain.CoverageActivated{
		EventID: "evt-1", PolicyID: "pol-1", AcceptanceID: "acc-1", CustomerID: "cust-1",
		Holder:  domain.Applicant{FullName: "Ana Pérez", DocumentID: "1032456789", Email: "ana@example.com"},
		Premium: 132_800, Currency: "COP", InsuredAmount: 250_000_000,
		Coverages:   []domain.Coverage{{Code: domain.CoverageDeath, InsuredAmount: 250_000_000}},
		ActivatedAt: activation, CoverageStart: activation, CoverageEnd: activation.AddDate(10, 0, 0),
	}
}

func (e *contractEnv) get(t *testing.T) domain.ContractIssuance {
	t.Helper()
	c, err := e.repo.Get(context.Background(), "pol-1")
	require.NoError(t, err)
	return c
}

func (e *contractEnv) auditEvents(t *testing.T) []string {
	t.Helper()
	entries, err := e.audit.List(context.Background(), "pol-1")
	require.NoError(t, err)
	var names []string
	for _, en := range entries {
		names = append(names, en.Event)
	}
	return names
}

func TestActivationProducesDocumentNotificationAndAuditWithoutManualSteps(t *testing.T) {
	e := newContractEnv(t, issuance.ContractConfig{})
	ctx := context.Background()
	require.NoError(t, e.svc.Receive(ctx, event()))

	e.clock.advance(3 * time.Second)
	require.Equal(t, 1, e.svc.ProcessDue(ctx))

	c := e.get(t)
	assert.Equal(t, domain.ContractCompleted, c.Status)
	require.NotNil(t, c.Document)
	assert.Equal(t, 1, c.Document.Version)
	assert.Equal(t, "policies/pol-1/contract-v1.pdf", c.Document.StorageKey)
	assert.Equal(t, "application/pdf", c.Document.ContentType)
	assert.Len(t, c.Document.Checksum, 64)
	assert.True(t, c.WithinSLO(), "criterion 1: available within 60 s of the activation")
	assert.Equal(t, activation.Add(3*time.Second), c.AvailableAt)
	assert.Equal(t, domain.NotificationSent, c.NotificationStatus)
	assert.Equal(t, domain.ChannelEmail, c.NotificationChannel)

	content, err := e.store.Get(ctx, c.Document.StorageKey)
	require.NoError(t, err)
	assert.Equal(t, []byte("%PDF-fake pol-1"), content, "criterion 2: stored in the repository, tied to the policy")

	require.Equal(t, 1, e.notifier.count())
	n := e.notifier.sent[0]
	assert.Equal(t, "ana@example.com", n.To)
	assert.Equal(t, "policy-issued:pol-1:v1", n.IdempotencyKey)
	assert.Regexp(t, `^https://api\.solventa\.example/v1/documents/[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+$`, n.DocumentURL,
		"criterion 3: a secure link, with the trailing slash of the base URL trimmed")
	assert.Equal(t, activation.Add(3*time.Second).Add(issuance.DefaultDocumentLinkTTL), n.LinkExpiresAt)

	assert.Equal(t, []string{domain.AuditDocumentGenerated, domain.AuditNotificationSent}, e.auditEvents(t))
	entries, _ := e.audit.List(ctx, "pol-1")
	assert.Equal(t, domain.IssuanceAuditEntry{
		PolicyID: "pol-1", Event: domain.AuditNotificationSent, At: activation.Add(3 * time.Second),
		DocumentID: c.Document.ID, DocumentVersion: 1, Checksum: c.Document.Checksum,
		DeliveryResult: domain.NotificationSent, Channel: domain.ChannelEmail, Attempt: 1,
	}, entries[1], "criterion 4: date and time, document version and delivery result")

	assert.Zero(t, e.svc.ProcessDue(ctx), "nothing is left to do")
}

func TestLinkInTheNotificationOpensTheDocumentWithoutASession(t *testing.T) {
	e := newContractEnv(t, issuance.ContractConfig{})
	ctx := context.Background()
	require.NoError(t, e.svc.Receive(ctx, event()))
	e.svc.ProcessDue(ctx)

	token := e.notifier.sent[0].DocumentURL[len("https://api.solventa.example/v1/documents/"):]
	doc, content, err := e.reader.DocumentByLink(ctx, token)

	require.NoError(t, err)
	assert.Equal(t, 1, doc.Version)
	assert.Equal(t, []byte("%PDF-fake pol-1"), content)

	e.clock.advance(issuance.DefaultDocumentLinkTTL)
	_, _, err = e.reader.DocumentByLink(ctx, token)
	assert.ErrorIs(t, err, ports.ErrInvalidLink, "the link expires")
}

func TestARedeliveredEventChangesNothing(t *testing.T) {
	e := newContractEnv(t, issuance.ContractConfig{})
	ctx := context.Background()
	require.NoError(t, e.svc.Receive(ctx, event()))
	e.svc.ProcessDue(ctx)

	require.NoError(t, e.svc.Receive(ctx, event()))
	e.clock.advance(time.Minute)

	assert.Zero(t, e.svc.ProcessDue(ctx))
	assert.Equal(t, 1, e.gen.calls)
	assert.Equal(t, 1, e.notifier.count())
	assert.Len(t, e.auditEvents(t), 2)
}

func TestAFailedNotificationIsRetriedWithoutRegeneratingTheDocument(t *testing.T) {
	e := newContractEnv(t, issuance.ContractConfig{RetryBase: time.Second, RetryMax: 4 * time.Second})
	e.notifier.failures = 2
	ctx := context.Background()
	require.NoError(t, e.svc.Receive(ctx, event()))

	e.svc.ProcessDue(ctx) // document ready, notification fails
	c := e.get(t)
	assert.Equal(t, domain.ContractDocumentReady, c.Status)
	assert.Equal(t, 1, c.Attempts)
	assert.Equal(t, activation.Add(time.Second), c.NextAttemptAt, "wait RetryBase")
	assert.NotNil(t, c.Document, "the document is already available while the notification retries")

	assert.Zero(t, e.svc.ProcessDue(ctx), "not due yet")
	e.clock.advance(time.Second)
	e.svc.ProcessDue(ctx) // second failure
	c = e.get(t)
	assert.Equal(t, 2, c.Attempts)
	assert.Equal(t, e.clock.Now().Add(2*time.Second), c.NextAttemptAt, "the wait doubles")

	e.clock.advance(2 * time.Second)
	e.svc.ProcessDue(ctx) // delivered

	c = e.get(t)
	assert.Equal(t, domain.ContractCompleted, c.Status)
	assert.Equal(t, 1, e.gen.calls, "the document was generated once")
	assert.Equal(t, 1, e.store.puts)
	assert.Equal(t, 1, e.notifier.count())
	assert.Equal(t, []string{
		domain.AuditDocumentGenerated, domain.AuditNotificationFailed, domain.AuditNotificationFailed, domain.AuditNotificationSent,
	}, e.auditEvents(t))
	entries, _ := e.audit.List(ctx, "pol-1")
	assert.Equal(t, domain.NotificationFailed, entries[1].DeliveryResult)
	assert.Equal(t, 1, entries[1].Attempt)
	assert.Equal(t, "notify: could not notify the insured: provider down", entries[1].Detail)
}

func TestAFailureGeneratingOrStoringTheDocumentIsRetried(t *testing.T) {
	e := newContractEnv(t, issuance.ContractConfig{RetryBase: time.Second})
	e.gen.err = errors.New("renderer down")
	ctx := context.Background()
	require.NoError(t, e.svc.Receive(ctx, event()))

	e.svc.ProcessDue(ctx)
	c := e.get(t)
	assert.Equal(t, domain.ContractPending, c.Status)
	assert.Nil(t, c.Document)
	assert.Zero(t, e.notifier.count(), "no notification without a document")
	assert.Empty(t, e.auditEvents(t), "no document event either")

	e.gen.err = nil
	e.store.err = errors.New("disk full")
	e.clock.advance(time.Second)
	e.svc.ProcessDue(ctx)
	assert.Nil(t, e.get(t).Document, "a document that was not stored is not associated")

	e.store.err = nil
	e.clock.advance(2 * time.Second)
	e.svc.ProcessDue(ctx)
	assert.Equal(t, domain.ContractCompleted, e.get(t).Status)
}

func TestAnExhaustedNotificationIsMarkedFailedButTheDocumentStaysAvailable(t *testing.T) {
	e := newContractEnv(t, issuance.ContractConfig{RetryBase: time.Second, RetryMax: time.Second, MaxAttempts: 2})
	e.notifier.failures = 99
	ctx := context.Background()
	require.NoError(t, e.svc.Receive(ctx, event()))

	e.svc.ProcessDue(ctx)
	e.clock.advance(time.Second)
	e.svc.ProcessDue(ctx)

	c := e.get(t)
	assert.Equal(t, domain.ContractFailed, c.Status)
	assert.Equal(t, domain.NotificationFailed, c.NotificationStatus)
	assert.True(t, c.NextAttemptAt.IsZero())
	assert.NotNil(t, c.Document)
	e.clock.advance(time.Hour)
	assert.Zero(t, e.svc.ProcessDue(ctx), "a failed issuance is not retried forever")
}

func TestADocumentAvailableAfterTheSLOIsFlagged(t *testing.T) {
	e := newContractEnv(t, issuance.ContractConfig{})
	ctx := context.Background()
	require.NoError(t, e.svc.Receive(ctx, event()))

	e.clock.advance(domain.ContractSLO + time.Second)
	e.svc.ProcessDue(ctx)

	assert.False(t, e.get(t).WithinSLO())
}

func TestReceiveRejectsAnInvalidEvent(t *testing.T) {
	e := newContractEnv(t, issuance.ContractConfig{})
	for name, mutate := range map[string]func(*domain.CoverageActivated){
		"no event id":        func(ev *domain.CoverageActivated) { ev.EventID = "" },
		"unsafe policy id":   func(ev *domain.CoverageActivated) { ev.PolicyID = "../etc" },
		"no customer":        func(ev *domain.CoverageActivated) { ev.CustomerID = "" },
		"no holder email":    func(ev *domain.CoverageActivated) { ev.Holder.Email = "" },
		"no premium":         func(ev *domain.CoverageActivated) { ev.Premium = 0 },
		"no coverages":       func(ev *domain.CoverageActivated) { ev.Coverages = nil },
		"no period":          func(ev *domain.CoverageActivated) { ev.CoverageEnd = ev.CoverageStart },
		"no activation time": func(ev *domain.CoverageActivated) { ev.ActivatedAt = time.Time{} },
	} {
		ev := event()
		mutate(&ev)
		assert.Error(t, e.svc.Receive(context.Background(), ev), name)
	}
	assert.Zero(t, e.svc.ProcessDue(context.Background()))
}

type brokenRepo struct {
	ports.ContractRepository
	createErr, listErr, updateErr error
}

func (b brokenRepo) Create(ctx context.Context, c domain.ContractIssuance) (domain.ContractIssuance, bool, error) {
	if b.createErr != nil {
		return domain.ContractIssuance{}, false, b.createErr
	}
	return b.ContractRepository.Create(ctx, c)
}

func (b brokenRepo) ListDue(ctx context.Context, now time.Time, limit int) ([]domain.ContractIssuance, error) {
	if b.listErr != nil {
		return nil, b.listErr
	}
	return b.ContractRepository.ListDue(ctx, now, limit)
}

func (b brokenRepo) Update(ctx context.Context, c domain.ContractIssuance) error {
	if b.updateErr != nil {
		return b.updateErr
	}
	return b.ContractRepository.Update(ctx, c)
}

func newBrokenEnv(t *testing.T, repo brokenRepo) *contractEnv {
	t.Helper()
	e := newContractEnv(t, issuance.ContractConfig{})
	repo.ContractRepository = e.repo
	e.svc = issuance.NewContractService(issuance.ContractDeps{
		Repo: repo, Generator: e.gen, Store: e.store, Signer: e.signer, Notifier: e.notifier, Audit: e.audit, Clock: e.clock,
	}, issuance.ContractConfig{})
	return e
}

func TestReceiveReportsARepositoryFailure(t *testing.T) {
	e := newBrokenEnv(t, brokenRepo{createErr: errors.New("db down")})
	assert.Error(t, e.svc.Receive(context.Background(), event()), "the event is not acknowledged, so it is redelivered")
}

func TestProcessDueSurvivesAListingFailure(t *testing.T) {
	e := newBrokenEnv(t, brokenRepo{listErr: errors.New("db down")})
	assert.Zero(t, e.svc.ProcessDue(context.Background()))
}

func TestAFailureToAssociateTheDocumentLeavesTheIssuancePending(t *testing.T) {
	e := newBrokenEnv(t, brokenRepo{updateErr: errors.New("db down")})
	ctx := context.Background()
	require.NoError(t, e.svc.Receive(ctx, event()))

	e.svc.ProcessDue(ctx)

	c := e.get(t)
	assert.Nil(t, c.Document)
	assert.Equal(t, domain.ContractPending, c.Status)
	assert.Zero(t, e.notifier.count())
}

func TestRunProcessesAnActivationAsSoonAsItIsReceived(t *testing.T) {
	e := newContractEnv(t, issuance.ContractConfig{PollInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.svc.Run(ctx); close(done) }()

	require.NoError(t, e.svc.Receive(ctx, event()))

	require.Eventually(t, func() bool { return e.notifier.count() == 1 }, 2*time.Second, 5*time.Millisecond,
		"the worker is woken by Receive, not by the (hour-long) poll")
	cancel()
	<-done
}

func TestReaderEnforcesOwnershipAndRoles(t *testing.T) {
	e := newContractEnv(t, issuance.ContractConfig{})
	bg := context.Background()
	require.NoError(t, e.svc.Receive(bg, event()))
	e.svc.ProcessDue(bg)

	as := func(subject string, roles ...security.Role) context.Context {
		return security.WithClaims(bg, security.Claims{Subject: subject, Roles: roles})
	}

	_, _, err := e.reader.Document(as("cust-1", security.RoleInsured), "pol-1")
	assert.NoError(t, err, "the owner reads their document")
	_, _, err = e.reader.Document(as("adv-1", security.RoleAdvisor), "pol-1")
	assert.NoError(t, err, "an advisor reads it")
	_, _, err = e.reader.Document(as("ops-1", security.RoleOperations), "pol-1")
	assert.NoError(t, err)
	_, _, err = e.reader.Document(as("cust-2", security.RoleInsured), "pol-1")
	assert.Error(t, err, "another insured does not")
	_, err = e.reader.Status(as("cust-2", security.RoleInsured), "pol-1")
	assert.Error(t, err)

	view, err := e.reader.Status(as("cust-1", security.RoleInsured), "pol-1")
	require.NoError(t, err)
	assert.Equal(t, domain.ContractCompleted, view.Issuance.Status)
	assert.Len(t, view.Audit, 2)

	_, err = e.reader.Status(as("cust-1", security.RoleInsured), "pol-404")
	assert.ErrorIs(t, err, ports.ErrContractNotFound)
}

func TestReaderReportsADocumentNotReadyYet(t *testing.T) {
	e := newContractEnv(t, issuance.ContractConfig{})
	bg := context.Background()
	require.NoError(t, e.svc.Receive(bg, event()))

	_, _, err := e.reader.Document(security.WithClaims(bg, security.Claims{Subject: "cust-1", Roles: []security.Role{security.RoleInsured}}), "pol-1")

	assert.ErrorIs(t, err, ports.ErrDocumentNotFound)
}

func TestLinkOfAnUnknownPolicyOrOtherVersionIsInvalid(t *testing.T) {
	e := newContractEnv(t, issuance.ContractConfig{})
	bg := context.Background()
	require.NoError(t, e.svc.Receive(bg, event()))
	e.svc.ProcessDue(bg)
	exp := e.clock.Now().Add(time.Hour)

	for name, token := range map[string]string{
		"unknown policy": e.signer.Sign("pol-404", 1, exp),
		"other version":  e.signer.Sign("pol-1", 2, exp),
		"garbage":        "not-a-token",
	} {
		_, _, err := e.reader.DocumentByLink(bg, token)
		assert.ErrorIs(t, err, ports.ErrInvalidLink, name)
	}
}
