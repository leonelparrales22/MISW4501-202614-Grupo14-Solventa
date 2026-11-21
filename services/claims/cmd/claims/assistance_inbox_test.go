package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/adapters/postgres"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// The composition of the inbox of assistance (HU-W2.3-03): over the requests
// of the phone, the registry of providers and the e-mail provider.

func TestTheDecisionsOfTheInboxLiveWhereTheRequestsLive(t *testing.T) {
	records := postgres.NewClaimRecordStore(nil)
	store, err := assistanceInboxStoreOf(postgres.AssistanceStoreOf(records), records)
	require.NoError(t, err)
	assert.IsType(t, &postgres.AssistanceInboxStore{}, store)

	store, err = assistanceInboxStoreOf(memory.NewAssistanceStore(), memory.NewCaseStore())
	require.NoError(t, err)
	assert.IsType(t, &memory.AssistanceInboxStore{}, store)

	_, err = assistanceInboxStoreOf(postgres.AssistanceStoreOf(records), memory.NewCaseStore())
	assert.Error(t, err, "in memory the inbox needs the very store of the requests")
	_, err = assistanceInboxStoreOf(memory.NewAssistanceStore(), fakeRecords{})
	assert.Error(t, err)
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer operations")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestTheInboxIsMountedOverTheRequestsOfThePhoneAndItsWorkerSendsTheNotice(t *testing.T) {
	provider := &emailProvider{}
	server := httptest.NewServer(provider)
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	requests := memory.NewAssistanceStore()
	const id = "3b8e2f6a-1c4d-4e7f-9a2b-6c5d8e0f1a2b"
	point, accuracy, at := domain.Coordinates{Latitude: 4.7712, Longitude: -74.0452}, 8.0, time.Now().UTC()
	_, _, err := requests.Create(ctx, domain.AssistanceRequest{ID: id, Subject: "sub-carolina", Key: "k", Fingerprint: "f",
		Status: domain.AssistanceNew, RequestedAt: at,
		Policy: domain.AssistancePolicy{ID: "AU-2025-118204", Line: domain.LineAuto, Product: "Autos"},
		Location: domain.AssistanceLocation{Status: domain.LocationSent, Coordinates: &point, AccuracyM: &accuracy,
			CapturedAt: &at}})
	require.NoError(t, err)

	opts, err := assistanceInboxRoutes(ctx, requests, memory.NewCaseStore(),
		memory.NewProviderStore(memory.ReferenceProviders()...), env(map[string]string{"EMAIL_URL": server.URL}),
		analystTokens{})
	require.NoError(t, err)
	require.Len(t, opts, 1)
	h := httpadapter.NewRouter(opts...)

	rec := get(t, h, "/v1/claims/assistance-inbox")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), id, "the inbox reads the requests the phone wrote")

	rec = post(t, h, "/v1/claims/assistance-inbox/"+id+"/attend", "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = post(t, h, "/v1/claims/assistance-inbox/"+id+"/need", `{"need": "towing"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	rec = post(t, h, "/v1/claims/assistance-inbox/"+id+"/dispatch", `{"provider_id": "prv-gruas-autopista-norte"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Eventually(t, func() bool {
		sent := provider.sent()
		return len(sent) == 1 && sent[0] == "asistencia-"+id
	}, 5*time.Second, 20*time.Millisecond, "the dispatch wakes the worker, which sends the notice")

	rec = get(t, h, "/v1/claims/assistance-policy")
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestTheInboxWithoutAnEmailProviderOrAStore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	providers := memory.NewProviderStore(memory.ReferenceProviders()...)
	opts, err := assistanceInboxRoutes(ctx, memory.NewAssistanceStore(), memory.NewCaseStore(), providers, env(nil),
		analystTokens{})
	require.NoError(t, err)
	assert.Len(t, opts, 1, "the notices wait; the inbox is mounted all the same, without the policy core")

	_, err = assistanceInboxRoutes(ctx, memory.NewAssistanceStore(), fakeRecords{}, providers, env(nil),
		analystTokens{})
	assert.Error(t, err)
}
