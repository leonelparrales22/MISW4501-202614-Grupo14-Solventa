package audit_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/audit"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
)

func decision() ports.DecisionRecord {
	return ports.DecisionRecord{
		OfferID: "off-1", CustomerID: "c1", PartnerID: "p1", ApplicationID: "a1", Channel: "embedded",
		RequestedAt: time.Date(2026, 10, 20, 15, 0, 0, 0, time.UTC), RaterVersion: "v1",
		Sources:      []ports.DecisionSource{{Source: "kyc", Status: "timeout", LatencyMS: 700}},
		ProfileScore: 0.4, ProfileCategory: "standard", ProfilePartial: true,
		Rules:   []ports.DecisionRule{{Order: 1, Code: "base_monthly_rate", Value: 0.001}},
		Premium: 1000, Currency: "COP", Decision: "deferred", DecisionReason: "kyc_unavailable",
	}
}

func newRecorder(url string, timeout time.Duration) *adapter.Recorder {
	return adapter.NewRecorder(adapter.Config{BaseURL: url, Timeout: timeout, Breaker: breaker()})
}

func TestRecordPostsTheDecisionToAudit(t *testing.T) {
	srv, hits := serve(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/v1/audit/decisions", r.URL.Path)
		var body map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		assert.Equal(t, "off-1", body["offer_id"])
		assert.Equal(t, "deferred", body["decision"])
		assert.Equal(t, "kyc_unavailable", body["decision_reason"])
		assert.Equal(t, "2026-10-20T15:00:00Z", body["requested_at"])
		assert.Equal(t, map[string]any{"score": 0.4, "category": "standard", "partial": true}, body["profile"])
		assert.NotContains(t, body, "document_id", "no personal data travels to audit")
		w.WriteHeader(http.StatusCreated)
	})

	require.NoError(t, newRecorder(srv.URL+"/", time.Second).Record(context.Background(), decision()))
	assert.EqualValues(t, 1, hits.Load())
}

func TestRecordAcceptsAnIdempotentReplay(t *testing.T) {
	srv, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	require.NoError(t, newRecorder(srv.URL, time.Second).Record(context.Background(), decision()))
}

func TestRecordReportsEveryFailure(t *testing.T) {
	bad, _ := serve(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) })
	assert.Error(t, newRecorder(bad.URL, time.Second).Record(context.Background(), decision()))

	slow, _ := serve(t, func(_ http.ResponseWriter, _ *http.Request) { time.Sleep(300 * time.Millisecond) })
	assert.Error(t, newRecorder(slow.URL, 50*time.Millisecond).Record(context.Background(), decision()))

	assert.Error(t, newRecorder("http://127.0.0.1:1", 50*time.Millisecond).Record(context.Background(), decision()))
}
