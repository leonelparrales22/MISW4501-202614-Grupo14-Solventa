package pdf_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/adapters/pdf"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

// The receipt of a revocation made from the compliance portal (HU-W4.2-02,
// decision D11): the channel and the confirmation method are printed with
// their label, never with their code.

func portalRevocation() (domain.Revocation, domain.Consent) {
	rev, c := revocation()
	rev.Channel = domain.RevocationChannelCompliancePortal
	rev.Device = "Mozilla/5.0 (Macintosh)"
	rev.ConfirmationMethod = domain.ConfirmationRoleAttestation
	rev.Actor = domain.Actor{Type: domain.ActorOperations, ID: "sub-ops"}
	rev.Reason, rev.Reference = string(domain.ReasonCustomerRequest), "TCK-2026-0412"
	return rev, c
}

func TestTheReceiptOfThePortalUsesTheLabelsOfThePortal(t *testing.T) {
	rev, c := portalRevocation()
	cases := []struct {
		lang        string
		wants       []string
		withoutCode []string
	}{
		{
			lang:        "es",
			wants:       []string{"Portal de cumplimiento de Solventa", "del rol de cumplimiento"},
			withoutCode: []string{"compliance_portal", "role_attestation"},
		},
		{
			lang:        "en",
			wants:       []string{"Solventa compliance portal", "Compliance role attestation"},
			withoutCode: []string{"compliance_portal", "role_attestation"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.lang, func(t *testing.T) {
			out, err := pdf.NewUncompressed().Render(rev, c, tc.lang)

			require.NoError(t, err)
			text := string(out)
			for _, want := range tc.wants {
				assert.Contains(t, text, want)
			}
			for _, raw := range tc.withoutCode {
				assert.NotContains(t, text, raw, "the raw code is never printed")
			}
			assert.Contains(t, text, "Mozilla/5.0", "the user agent is the device")
			assert.NotContains(t, text, "1020304050", "the document stays masked")
			assert.NotContains(t, text, "TCK-2026-0412", "the reference is not part of the receipt of the holder")
		})
	}
}

func TestTheReceiptOfThePortalIsDeterministic(t *testing.T) {
	rev, c := portalRevocation()
	for _, r := range []pdf.Renderer{pdf.New(), pdf.NewUncompressed()} {
		first, err := r.Render(rev, c, "es")
		require.NoError(t, err)
		second, err := r.Render(rev, c, "es")
		require.NoError(t, err)

		assert.Equal(t, first, second)
	}
}

func TestTheReceiptOfTheAppKeepsItsLabels(t *testing.T) {
	rev, c := revocation()

	out, err := pdf.NewUncompressed().Render(rev, c, "en")

	require.NoError(t, err)
	assert.Contains(t, string(out), "Solventa mobile app")
	assert.Contains(t, string(out), "Biometrics")
	assert.NotContains(t, string(out), "compliance portal")
}
