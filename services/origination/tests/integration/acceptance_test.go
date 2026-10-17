// This file wires the offer acceptance of HU-W1.3-01 end to end over HTTP
// (router, pkg/security middleware, use case, asynchronous processor and the
// in-memory adapters) and checks each acceptance criterion.
package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/security"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/memory"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/issuance"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

// subjectVerifier stands in for the Cognito JWKS verifier: the token is
// "<role>:<subject>".
type subjectVerifier struct{}

func (subjectVerifier) Verify(_ context.Context, raw string) (security.Claims, error) {
	role, subject, ok := strings.Cut(raw, ":")
	if !ok {
		return security.Claims{}, errors.New("invalid token")
	}
	return security.Claims{Subject: subject, Email: subject + "@example.com", Roles: []security.Role{security.Role(role)}}, nil
}

type wallClock struct{}

func (wallClock) Now() time.Time { return time.Now() }

type acceptanceEnv struct {
	srv     *httptest.Server
	reviews *memory.ReviewQueueStub
	issued  *memory.IssuanceStub
}

func newAcceptanceEnv(t *testing.T) *acceptanceEnv {
	t.Helper()
	clock := wallClock{}
	offers := memory.NewOfferStub(clock)
	acceptances := memory.NewAcceptanceStore()
	e := &acceptanceEnv{reviews: memory.NewReviewQueueStub(), issued: memory.NewIssuanceStub()}
	processor := issuance.NewProcessor(offers, acceptances, memory.NewUnderwriterStub(clock, 2, memory.DefaultReviewScoreThreshold), e.reviews, e.issued, clock,
		issuance.ProcessorConfig{PollInterval: 20 * time.Millisecond, RetryBase: 30 * time.Millisecond, RetryMax: 60 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go processor.Run(ctx)

	svc := issuance.NewAcceptanceService(offers, acceptances, clock, processor.Notify)
	resolver := profiling.NewResolver(nil, nil, clock) // the profiling routes are not used here
	e.srv = httptest.NewServer(httpadapter.NewRouter(resolver, httpadapter.WithAcceptances(svc, subjectVerifier{})))
	t.Cleanup(e.srv.Close)
	return e
}

func (e *acceptanceEnv) do(t *testing.T, method, path, token, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = res.Body.Close() }()
	var m map[string]any
	require.NoError(t, json.NewDecoder(res.Body).Decode(&m))
	return res.StatusCode, m
}

func (e *acceptanceEnv) accept(t *testing.T, offerID, token string) (int, map[string]any) {
	t.Helper()
	return e.do(t, http.MethodPost, "/v1/offers/"+offerID+"/acceptance", token, `{"channel":"web"}`)
}

// waitStatus polls GET /v1/acceptances/{id} the way the front does.
func (e *acceptanceEnv) waitStatus(t *testing.T, id, token, want string) map[string]any {
	t.Helper()
	var last map[string]any
	require.Eventually(t, func() bool {
		_, last = e.do(t, http.MethodGet, "/v1/acceptances/"+id, token, "")
		return last["status"] == want
	}, 3*time.Second, 10*time.Millisecond, "last answer: %v", last)
	return last
}

const customer = "insured:cust-1"

func TestCA1AcceptanceIsPersistedBeforeAnyLaterStep(t *testing.T) {
	e := newAcceptanceEnv(t)

	status, body := e.accept(t, "demo-active-ca1", customer)

	require.Equal(t, http.StatusAccepted, status)
	assert.Equal(t, "registered", body["status"])
	assert.Equal(t, "demo-active-ca1", body["offer_id"])
	assert.Equal(t, "cust-1", body["customer_id"])
	assert.Equal(t, "web", body["channel"])
	assert.NotEmpty(t, body["accepted_at"])
	assert.Nil(t, body["underwriting"], "nothing ran before the acceptance was registered")
}

func TestCA2ApprovedRiskStartsIssuanceWithoutManualSteps(t *testing.T) {
	e := newAcceptanceEnv(t)
	_, body := e.accept(t, "demo-active-ca2", customer)
	id := body["acceptance_id"].(string)

	got := e.waitStatus(t, id, customer, "issuance_pending")

	assert.Equal(t, "approved", got["underwriting"].(map[string]any)["outcome"])
	assert.True(t, e.issued.Started(id))
}

func TestCA3HumanReviewReachesTheInboxAndTheCustomerSeesIt(t *testing.T) {
	e := newAcceptanceEnv(t)
	_, body := e.accept(t, "demo-review-ca3", customer)
	id := body["acceptance_id"].(string)

	got := e.waitStatus(t, id, customer, "in_review")

	assert.Equal(t, map[string]any{"outcome": "human_review", "reason": "provisional_offer",
		"decided_at": got["underwriting"].(map[string]any)["decided_at"]}, got["underwriting"])
	pending := e.reviews.Pending()
	require.Len(t, pending, 1)
	assert.Equal(t, id, pending[0].AcceptanceID)
	assert.Equal(t, "demo-review-ca3", pending[0].OfferID)
	assert.Equal(t, "cust-1", pending[0].CustomerID)
	assert.Equal(t, "cust-1@example.com", pending[0].Applicant.Email)
	assert.Equal(t, domain.ReviewReasonProvisionalOffer, pending[0].Reason)
	assert.False(t, e.issued.Started(id))

	status, _ := e.do(t, http.MethodGet, "/v1/acceptances/"+id, "advisor:adv-1", "")
	assert.Equal(t, http.StatusOK, status, "the advisor can read it too")
}

func TestCA4ExpiredOfferIsRejectedWithItsReason(t *testing.T) {
	e := newAcceptanceEnv(t)

	status, body := e.accept(t, "demo-expired-ca4", customer)

	require.Equal(t, http.StatusUnprocessableEntity, status)
	assert.Equal(t, "offer_expired", body["error"])
	assert.Equal(t, "demo-expired-ca4", body["offer_id"])
	assert.NotEmpty(t, body["expires_at"])
	status, _ = e.do(t, http.MethodGet, "/v1/offers/demo-expired-ca4/acceptance", customer, "")
	assert.Equal(t, http.StatusNotFound, status, "nothing was registered")
}

func TestCA5AcceptanceSurvivesALaterFailureAndResumes(t *testing.T) {
	e := newAcceptanceEnv(t)
	status, body := e.accept(t, "demo-flaky-ca5", customer)
	require.Equal(t, http.StatusAccepted, status)
	id := body["acceptance_id"].(string)

	var resuming map[string]any
	require.Eventually(t, func() bool {
		_, resuming = e.do(t, http.MethodGet, "/v1/acceptances/"+id, customer, "")
		p := resuming["processing"].(map[string]any)
		return resuming["status"] == "registered" && p["attempts"].(float64) >= 1
	}, 3*time.Second, 5*time.Millisecond, "the failed attempt is visible while it resumes")
	assert.NotNil(t, resuming["processing"].(map[string]any)["last_failure_at"])

	// The client lost the acceptance id: it resumes from the offer, and
	// accepting again does not repeat the process.
	status, again := e.accept(t, "demo-flaky-ca5", customer)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, id, again["acceptance_id"])
	status, byOffer := e.do(t, http.MethodGet, "/v1/offers/demo-flaky-ca5/acceptance", customer, "")
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, id, byOffer["acceptance_id"])

	got := e.waitStatus(t, id, customer, "issuance_pending")
	assert.Equal(t, float64(2), got["processing"].(map[string]any)["attempts"])
	assert.True(t, e.issued.Started(id))
}

func TestAnotherCustomerCannotAcceptOrReadTheOffer(t *testing.T) {
	e := newAcceptanceEnv(t)
	_, body := e.accept(t, "demo-active-own", customer)

	status, _ := e.accept(t, "demo-active-own", "insured:cust-2")
	assert.Equal(t, http.StatusForbidden, status)
	status, _ = e.do(t, http.MethodGet, "/v1/acceptances/"+body["acceptance_id"].(string), "insured:cust-2", "")
	assert.Equal(t, http.StatusForbidden, status)
}
