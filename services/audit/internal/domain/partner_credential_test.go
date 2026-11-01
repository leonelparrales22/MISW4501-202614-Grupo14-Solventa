package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

func credentialRecord(eventType, reason string) domain.PartnerCredentialRecord {
	return domain.PartnerCredentialRecord{
		EventID: "partner-credentials-x-crd-2", EventType: eventType, PartnerID: "ptn-1", PartnerCode: "ten_banco",
		PartnerName: "Banco Aliado", CredentialID: "crd-2", ClientID: "sol_2", Reason: reason,
		Quota:      domain.CredentialQuota{DailyLimit: 5000, RateLimit: 100, BurstLimit: 200},
		Actor:      domain.CredentialActor{Type: domain.CredentialActorSystem, ID: "partners", Label: "Sistema"},
		OccurredAt: time.Date(2026, 11, 1, 15, 0, 0, 0, time.UTC),
	}
}

func invalidFields(err error) []string {
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

func TestPartnerCredentialRecordValidate(t *testing.T) {
	require.NoError(t, credentialRecord(domain.PartnerCredentialsIssued, domain.CredentialReasonApproval).Validate())
	require.NoError(t, credentialRecord(domain.PartnerCredentialsRevoked, domain.CredentialReasonGracePeriodEnded).Validate())

	rotated := credentialRecord(domain.PartnerCredentialsRotated, domain.CredentialReasonRotation)
	assert.ElementsMatch(t, []string{"previous_credential_id:required", "previous_client_id:required",
		"previous_expires_at:required", "grace_period_hours:out_of_range"}, invalidFields(rotated.Validate()))
	rotated.PreviousCredentialID, rotated.PreviousClientID, rotated.GracePeriodHours = "crd-1", "sol_1", 24
	rotated.PreviousExpiresAt = rotated.OccurredAt.Add(24 * time.Hour)
	require.NoError(t, rotated.Validate())

	wrongReason := credentialRecord(domain.PartnerCredentialsIssued, domain.CredentialReasonRotation)
	assert.Equal(t, []string{"reason:invalid_format"}, invalidFields(wrongReason.Validate()))

	bad := credentialRecord("SomethingElse", "")
	bad.PartnerID, bad.PartnerName, bad.ClientID = "", strings.Repeat("x", 300), "con espacio"
	bad.Quota = domain.CredentialQuota{RateLimit: 10, BurstLimit: 5}
	bad.Actor = domain.CredentialActor{Type: "robot"}
	bad.OccurredAt = time.Time{}
	err := bad.Validate()
	assert.ElementsMatch(t, []string{"event_type:invalid_format", "partner_id:required", "partner_name:out_of_range",
		"client_id:invalid_format", "quota.daily_limit:out_of_range", "quota.burst_limit:out_of_range",
		"actor.type:invalid_format", "actor.id:required", "occurred_at:required"}, invalidFields(err))
}
