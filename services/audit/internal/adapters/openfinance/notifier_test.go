package openfinance_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ofclient "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/adapters/openfinance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/idempotency"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/resilience"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/openfinance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/ports"
)

var effective = time.Date(2026, 10, 24, 20, 0, 1, 0, time.UTC)

func record() ports.NoticeRecord {
	return ports.NoticeRecord{
		ConsentID: "cns-1", EntityCode: "aliado-banco-1", Key: "revocation-cns-1-aliado-banco-1", Seq: 2,
		Status: idempotency.StatusInProgress, CustomerID: "cust-1", EffectiveAt: effective,
	}
}

func client(url string) *ofclient.Client {
	return ofclient.NewClient(ofclient.Config{BaseURL: url, Timeout: time.Second, Breaker: resilience.DefaultConfig()})
}

func TestNotifySendsTheMinimumDataWithTheKeyOfTheNotice(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/consent-revocations", r.URL.Path)
		assert.Equal(t, "revocation-cns-1-aliado-banco-1", r.Header.Get("Idempotency-Key"))
		var body map[string]any
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, map[string]any{"consent_id": "cns-1", "entity_code": "aliado-banco-1", "customer_id": "cust-1",
			"effective_at": "2026-10-24T20:00:01Z"}, body, "never the document nor the name")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"revocation_reference":"of-rev-9","status":"confirmed","confirmed_at":"2026-10-24T20:00:02Z"}`))
	}))
	defer srv.Close()

	out, err := openfinance.NewNotifier(client(srv.URL)).Notify(context.Background(), record())

	require.NoError(t, err)
	assert.Equal(t, idempotency.Outcome{Reference: "of-rev-9"}, out)
}

func TestNotifyKeepsTheClassificationOfTheClient(t *testing.T) {
	for code, want := range map[int]idempotency.CallOutcome{
		http.StatusServiceUnavailable: idempotency.OutcomeAmbiguous,
		http.StatusBadRequest:         idempotency.OutcomeRejected,
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) }))
		_, err := openfinance.NewNotifier(client(srv.URL)).Notify(context.Background(), record())
		assert.Equal(t, want, idempotency.Classify(err), code)
		srv.Close()
	}
}
