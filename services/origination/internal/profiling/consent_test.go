package profiling_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/ports"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/profiling"
)

type fakeVerifier struct {
	status domain.ConsentStatus
	err    error
	calls  []string
}

func (f *fakeVerifier) Verify(_ context.Context, customerID string) (domain.ConsentStatus, error) {
	f.calls = append(f.calls, customerID)
	return f.status, f.err
}

type recordingResolver struct {
	calls   int
	consent bool
	res     domain.ProfileResolution
	err     error
}

func (r *recordingResolver) Resolve(_ context.Context, _ string, consentActive bool) (domain.ProfileResolution, error) {
	r.calls++
	r.consent = consentActive
	return r.res, r.err
}

func TestConsentCheckedResolverResolvesWhenTheConsentIsActive(t *testing.T) {
	want := domain.ProfileResolution{Mode: domain.ModeNormal}
	next := &recordingResolver{res: want}
	verifier := &fakeVerifier{status: domain.ConsentStatus{Active: true, ConsentID: "cns-1"}}

	got, err := profiling.NewConsentCheckedResolver(next, verifier).Resolve(context.Background(), "c1", false)

	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, 1, next.calls)
	assert.True(t, next.consent, "the verified consent replaces the flag the caller sent")
	assert.Equal(t, []string{"c1"}, verifier.calls)
}

func TestConsentCheckedResolverIgnoresAnAffirmedConsentThatAuditDoesNotHave(t *testing.T) {
	next := &recordingResolver{}
	verifier := &fakeVerifier{status: domain.ConsentStatus{Active: false}}

	_, err := profiling.NewConsentCheckedResolver(next, verifier).Resolve(context.Background(), "c1", true)

	assert.ErrorIs(t, err, profiling.ErrConsentRequired)
	assert.Zero(t, next.calls, "nothing is resolved without a consent")
}

func TestConsentCheckedResolverFailsClosedWhenTheConsentCannotBeVerified(t *testing.T) {
	next := &recordingResolver{}
	verifier := &fakeVerifier{err: ports.ErrConsentUnverifiable}

	_, err := profiling.NewConsentCheckedResolver(next, verifier).Resolve(context.Background(), "c1", true)

	assert.ErrorIs(t, err, ports.ErrConsentUnverifiable)
	assert.NotErrorIs(t, err, profiling.ErrConsentRequired)
	assert.Zero(t, next.calls, "no source is queried when audit does not answer")
}

func TestConsentCheckedResolverPropagatesTheResolverError(t *testing.T) {
	boom := errors.New("boom")
	next := &recordingResolver{err: boom}
	verifier := &fakeVerifier{status: domain.ConsentStatus{Active: true}}

	_, err := profiling.NewConsentCheckedResolver(next, verifier).Resolve(context.Background(), "c1", true)

	assert.ErrorIs(t, err, boom)
}

func TestConsentCheckedResolverWithoutVerifierKeepsTheAffirmedFlag(t *testing.T) {
	next := &recordingResolver{res: domain.ProfileResolution{Mode: domain.ModeNormal}}
	resolver := profiling.NewConsentCheckedResolver(next, nil)

	_, err := resolver.Resolve(context.Background(), "c1", false)
	require.NoError(t, err)
	assert.False(t, next.consent)

	_, err = resolver.Resolve(context.Background(), "c1", true)
	require.NoError(t, err)
	assert.True(t, next.consent)
	assert.Equal(t, 2, next.calls)
}
