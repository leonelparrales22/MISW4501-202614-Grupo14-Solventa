package domain_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/partners/internal/domain"
)

func valid() domain.RegisterRequest {
	return domain.RegisterRequest{
		Name: "Banco Aliado", Code: "ten_bancoaliado", Type: domain.TypeBank, Products: []string{"hogar"},
		TechnicalContactEmail: "api@bancoaliado.co", Quota: domain.Quota{DailyLimit: 5000, RateLimit: 100, BurstLimit: 200},
	}
}

func fields(err error) []string {
	verr, ok := err.(*domain.ValidationError)
	if !ok {
		return nil
	}
	out := []string{}
	for _, f := range verr.Fields {
		out = append(out, f.Field+":"+f.Code)
	}
	return out
}

func TestRegisterRequestValidate(t *testing.T) {
	require.NoError(t, valid().Validate())

	err := domain.RegisterRequest{}.Validate()
	assert.ElementsMatch(t, []string{"name:required", "code:required", "type:required", "products:required",
		"technical_contact_email:required", "quota.daily_limit:out_of_range", "quota.rate_limit:out_of_range",
		"quota.burst_limit:out_of_range"}, fields(err))
	assert.Contains(t, err.Error(), "name: required")

	bad := valid()
	bad.Name, bad.Code, bad.Type = "AB", "Ten-Banco", "insurer"
	bad.Products = []string{"x"}
	bad.TechnicalContactEmail = "no es correo"
	bad.Quota = domain.Quota{DailyLimit: domain.MaxDailyLimit + 1, RateLimit: 100, BurstLimit: 50}
	assert.ElementsMatch(t, []string{"name:out_of_range", "code:invalid_format", "type:invalid_format",
		"products:out_of_range", "technical_contact_email:invalid_format", "quota.daily_limit:out_of_range",
		"quota.burst_limit:out_of_range"}, fields(bad.Validate()))

	many := valid()
	many.Products = make([]string, domain.MaxProducts+1)
	assert.Equal(t, []string{"products:out_of_range"}, fields(many.Validate()))

	for _, e := range []string{"a@b", "Ana <a@b.co>", "a@b.co"} {
		r := valid()
		r.TechnicalContactEmail = e
		if e == "a@b.co" {
			assert.NoError(t, r.Validate())
		} else {
			assert.Error(t, r.Validate(), e)
		}
	}
}

func TestNormalize(t *testing.T) {
	r := domain.RegisterRequest{Name: " A ", Code: " c ", TechnicalContactEmail: " e ", Products: []string{" p "}}.Normalize()
	assert.Equal(t, domain.RegisterRequest{Name: "A", Code: "c", TechnicalContactEmail: "e", Products: []string{"p"}}, r)
}

func TestUsage(t *testing.T) {
	u := domain.NewUsage("2026-11-01", 3120, 5000)
	assert.Equal(t, domain.Usage{Date: "2026-11-01", Used: 3120, Remaining: 1880, Percent: 62.4}, u)
	over := domain.NewUsage("d", 6000, 5000)
	assert.Zero(t, over.Remaining)
	assert.Equal(t, domain.UsageExceeded, domain.StatusOf(&over))
	assert.Equal(t, domain.UsageUnknown, domain.StatusOf(nil))
	assert.Zero(t, domain.NewUsage("d", 1, 0).Percent)
}

func TestHintAndGrace(t *testing.T) {
	assert.Equal(t, "a1c9", domain.Hint("xxxxa1c9"))
	assert.Equal(t, "ab", domain.Hint("ab"))
	assert.True(t, domain.ValidGracePeriod(1))
	assert.True(t, domain.ValidGracePeriod(168))
	assert.False(t, domain.ValidGracePeriod(169))
	assert.True(t, domain.TypeOther.Valid())
}

func TestCredentialEvents(t *testing.T) {
	at := time.Date(2026, 11, 1, 10, 0, 0, 0, time.FixedZone("COT", -5*3600))
	p := domain.Partner{ID: "ptn-1", Code: "ten_a", Name: "A", Quota: domain.Quota{DailyLimit: 10, RateLimit: 1, BurstLimit: 2}}
	c := domain.Credential{ID: "crd-2", ClientID: "sol_2", SecretHint: "zzzz"}
	exp := at.Add(time.Hour)
	prev := &domain.Credential{ID: "crd-1", ClientID: "sol_1", ExpiresAt: &exp}

	for _, tc := range []struct {
		typ, id, reason string
		previous        *domain.Credential
	}{
		{domain.EventCredentialsIssued, "partner-credentials-issued-crd-2", "approval", nil},
		{domain.EventCredentialsRotated, "partner-credentials-rotated-crd-2", "rotation", prev},
		{domain.EventCredentialsRevoked, "partner-credentials-revoked-crd-2", "grace_period_ended", nil},
	} {
		e, err := domain.CredentialEvent(tc.typ, p, c, domain.SystemActor, at, tc.previous, 24)
		require.NoError(t, err)
		assert.Equal(t, tc.id, e.EventID)
		assert.Equal(t, "partners", e.Source)
		assert.Equal(t, at.UTC(), e.OccurredAt)
		var payload domain.CredentialEventPayload
		require.NoError(t, json.Unmarshal(e.Payload, &payload))
		assert.Equal(t, tc.reason, payload.Reason)
		assert.Equal(t, "Sistema", payload.Actor.Label)
		assert.NotContains(t, string(e.Payload), "zzzz", "never the hint")
		if tc.previous != nil {
			assert.Equal(t, "crd-1", payload.PreviousCredentialID)
			assert.Equal(t, 24, payload.GracePeriodHours)
			assert.Equal(t, exp.UTC(), *payload.PreviousExpiresAt)
		}
	}
}
