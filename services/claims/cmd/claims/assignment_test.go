package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/ports"
)

// The composition of the assignment of the provider (HU-W2.3-01): the
// interval of the automatic search, the stores and the worker.

func TestTheAutomaticSearchRunsEveryFiveMinutesByDefault(t *testing.T) {
	interval, err := loadAssignmentConfig(env(nil))
	require.NoError(t, err)
	assert.Equal(t, 300*time.Second, interval)
}

func TestTheIntervalOfTheAutomaticSearchAtItsLimits(t *testing.T) {
	for raw, want := range map[string]time.Duration{"10": 10 * time.Second, "3600": time.Hour, "60": time.Minute} {
		interval, err := loadAssignmentConfig(env(map[string]string{"ASSIGNMENT_RETRY_INTERVAL_SECONDS": raw}))
		require.NoError(t, err, raw)
		assert.Equal(t, want, interval, raw)
	}
	for _, raw := range []string{"9", "3601", "0", "-10", "abc", "1.5", "300s"} {
		_, err := loadAssignmentConfig(env(map[string]string{"ASSIGNMENT_RETRY_INTERVAL_SECONDS": raw}))
		assert.Error(t, err, raw)
	}
}

func TestTheAssignmentsLiveWhereTheClaimsLive(t *testing.T) {
	store, providers, err := assignmentStoresOf(postgres.NewClaimRecordStore(nil))
	require.NoError(t, err)
	assert.IsType(t, &postgres.AssignmentStore{}, store)
	assert.IsType(t, &postgres.ProviderStore{}, providers)

	store, providers, err = assignmentStoresOf(memory.NewCaseStore())
	require.NoError(t, err)
	assert.IsType(t, &memory.AssignmentStore{}, store)
	list, err := providers.List(context.Background(), ports.ProviderFilter{}, 200)
	require.NoError(t, err)
	assert.Len(t, list, len(memory.ReferenceProviders()), "the memory store has the reference providers")

	_, _, err = assignmentStoresOf(fakeRecords{})
	assert.Error(t, err)
}

// analystTokens is the verifier of the tests: one analyst of operations.
type analystTokens struct{}

func (analystTokens) Verify(_ context.Context, raw string) (security.Claims, error) {
	if raw != "operations" {
		return security.Claims{}, errors.New("bad token")
	}
	return security.Claims{Subject: "sub-ops", Email: "andres.ruiz@solventa.example", Name: "Andrés Ruiz",
		Roles: []security.Role{security.RoleOperations}}, nil
}

// emailProvider is the e-mail provider of the tests: it accepts every
// message and keeps its idempotency key.
type emailProvider struct {
	mu   sync.Mutex
	keys []string
}

func (p *emailProvider) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	p.mu.Lock()
	p.keys = append(p.keys, req.Header.Get("Idempotency-Key"))
	p.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
}

func (p *emailProvider) sent() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.keys...)
}

func post(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer operations")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTheAssignmentIsMountedAndItsWorkerSendsTheNotice(t *testing.T) {
	provider := &emailProvider{}
	server := httptest.NewServer(provider)
	defer server.Close()
	cases := memory.NewCaseStore()
	const id = "8f14e45f-ceea-467a-9f3a-5a2d0b1c7e90"
	_, _, err := cases.Create(context.Background(), domain.Claim{ID: id, Subject: "sub-carolina", FilingKey: "k",
		IncidentType: domain.IncidentWaterDamage, Status: domain.StatusInEvaluation, FiledAt: time.Now().UTC(),
		Details: map[string]string{}, Evidence: []domain.Evidence{{ObjectKey: "e/1.jpg", Kind: domain.KindPhoto,
			CapturedAt: time.Now().UTC(), Latitude: 4.6771, Longitude: -74.0483, AccuracyM: 8, LocationTrusted: true,
			SHA256: "ab", ByteSize: 10}}})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	opts, err := assignmentRoutes(ctx, cases, env(map[string]string{"EMAIL_URL": server.URL}), analystTokens{})
	require.NoError(t, err)
	require.Len(t, opts, 2)
	h := httpadapter.NewRouter(opts...)

	rec := post(t, h, "/v1/claims/"+id+"/provider-searches", "")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	rec = post(t, h, "/v1/claims/"+id+"/assignment", `{"provider_id": "prv-servicios-hogar-norte", "search_seq": 1}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Eventually(t, func() bool {
		sent := provider.sent()
		return len(sent) == 1 && sent[0] == "asignacion-"+id
	}, 5*time.Second, 20*time.Millisecond, "the assignment wakes the worker, which sends the notice")

	rec = post(t, h, "/v1/claims/providers", `{"name": "Nuevo Prestador", "specialties": ["painting"],
		"latitude": 4.7, "longitude": -74.05, "address": "Calle 100 # 15-20", "contact_email": "nuevo@example.com"}`)
	assert.Equal(t, http.StatusCreated, rec.Code, "the registry is mounted too")
}

func TestTheAssignmentWithoutAnEmailProviderOrWithABadConfiguration(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts, err := assignmentRoutes(ctx, memory.NewCaseStore(), env(nil), analystTokens{})
	require.NoError(t, err)
	assert.Len(t, opts, 2, "the notices wait; the assignment is mounted all the same")

	_, err = assignmentRoutes(ctx, memory.NewCaseStore(),
		env(map[string]string{"ASSIGNMENT_RETRY_INTERVAL_SECONDS": "5"}), analystTokens{})
	assert.Error(t, err)
	_, err = assignmentRoutes(ctx, fakeRecords{}, env(nil), analystTokens{})
	assert.Error(t, err)
}
