package orchestrator_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/orchestrator"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/rating"
)

type fakeRecorder struct {
	mu   sync.Mutex
	got  []ports.DecisionRecord
	fail error
}

func (f *fakeRecorder) Record(_ context.Context, r ports.DecisionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, r)
	return f.fail
}

func envWithRecorder(rec ports.DecisionRecorder) *offerEnv {
	e := newOfferEnv()
	e.svc = orchestrator.NewOfferService(orchestrator.OfferDeps{
		Store: e.store, Offers: e.router, Consents: e.consent, Profiles: e.profiles, Rater: rating.New(), Clock: e.clock,
		Decisions: rec,
	})
	return e
}

func TestGenerateRecordsTheAcceptedDecisionWithItsRulesAndSources(t *testing.T) {
	rec := &fakeRecorder{}
	e := envWithRecorder(rec)
	profile := freshProfile()
	asked := offerStart.Add(-time.Second)
	profile.Sources = []domain.SourceOutcome{
		{Source: "open_finance", Status: domain.SourceStatusOK, Latency: 85 * time.Millisecond, ConsultedAt: asked, Value: &domain.SourceValue{Score: 0.82, Category: "preferred"}},
		{Source: "kyc", Status: domain.SourceStatusTimeout, Latency: 700 * time.Millisecond, ConsultedAt: asked},
	}
	e.profiles.set(profile, nil)

	o, err := e.svc.Generate(context.Background(), offerReq())

	require.NoError(t, err)
	require.Len(t, rec.got, 1)
	d := rec.got[0]
	assert.Equal(t, o.ID, d.OfferID)
	assert.Equal(t, "accepted", d.Decision)
	assert.Empty(t, d.DecisionReason)
	assert.Equal(t, o.Premium, d.Premium)
	assert.Equal(t, o.RaterVersion, d.RaterVersion)
	assert.Equal(t, offerStart, d.RequestedAt)
	score := 0.82
	assert.Equal(t, []ports.DecisionSource{
		{Source: "open_finance", Status: "ok", LatencyMS: 85, ConsultedAt: asked, Score: &score, Category: "preferred"},
		{Source: "kyc", Status: "timeout", LatencyMS: 700, ConsultedAt: asked},
	}, d.Sources, "each source with the value it returned (nil when it did not answer)")
	assert.Equal(t, "cns-1", d.ConsentID, "the consent the sources were consulted under")
	require.Len(t, d.Rules, len(o.Rules))
	assert.Equal(t, ports.DecisionRule{Order: 1, Code: rating.RuleBaseMonthlyRate, Value: o.Rules[0].Value}, d.Rules[0])
	assert.Equal(t, len(o.Rules), d.Rules[len(d.Rules)-1].Order, "orders are consecutive")
}

func TestGenerateRecordsAProvisionalOfferAsDeferredWithItsReason(t *testing.T) {
	rec := &fakeRecorder{}
	e := envWithRecorder(rec)
	e.profiles.set(baseProfile(), nil)

	_, err := e.svc.Generate(context.Background(), offerReq())

	require.NoError(t, err)
	require.Len(t, rec.got, 1)
	assert.Equal(t, "deferred", rec.got[0].Decision)
	assert.NotEmpty(t, rec.got[0].DecisionReason)
}

func TestGenerateDoesNotFailWhenAuditCannotRecord(t *testing.T) {
	rec := &fakeRecorder{fail: errors.New("audit down")}
	e := envWithRecorder(rec)

	o, err := e.svc.Generate(context.Background(), offerReq())

	require.NoError(t, err, "the customer still gets the offer")
	assert.NotEmpty(t, o.ID)
	assert.Len(t, rec.got, 1)
}

func TestGenerateWithoutARecorderRecordsNothing(t *testing.T) {
	_, err := newOfferEnv().svc.Generate(context.Background(), offerReq())
	require.NoError(t, err)
}
