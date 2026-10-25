package domain_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/audit/internal/domain"
)

// HU-W4.2-02: the request of the compliance portal and the masked document.

func complianceFields(t *testing.T, err error) []domain.FieldError {
	t.Helper()
	var verr *domain.ValidationError
	require.True(t, errors.As(err, &verr), "want a *ValidationError, got %v", err)
	return verr.Fields
}

func TestComplianceRevokeRequestValidate(t *testing.T) {
	valid := domain.ComplianceRevokeRequest{Reason: "customer_request", Reference: "TCK-2026-0412", RoleConfirmed: true}

	cases := []struct {
		name   string
		mutate func(*domain.ComplianceRevokeRequest)
		want   []domain.FieldError
	}{
		{name: "valid with a customer request", mutate: func(*domain.ComplianceRevokeRequest) {}},
		{name: "valid with a regulatory order", mutate: func(r *domain.ComplianceRevokeRequest) { r.Reason = "regulatory_order" }},
		{name: "reference of 3 characters", mutate: func(r *domain.ComplianceRevokeRequest) { r.Reference = "A-1" }},
		{name: "reference of 64 characters", mutate: func(r *domain.ComplianceRevokeRequest) { r.Reference = strings.Repeat("a", 64) }},
		{name: "reference with every allowed symbol", mutate: func(r *domain.ComplianceRevokeRequest) { r.Reference = "OF 12/2026 #3_a.b-c" }},
		{name: "reference of 64 once the spaces are collapsed", mutate: func(r *domain.ComplianceRevokeRequest) {
			r.Reference = strings.Repeat("a", 30) + "     " + strings.Repeat("b", 33)
		}},
		{
			name: "reference of 2 characters", mutate: func(r *domain.ComplianceRevokeRequest) { r.Reference = "ab" },
			want: []domain.FieldError{{Field: "reference", Code: domain.CodeOutOfRange}},
		},
		{
			name: "reference of 65 characters", mutate: func(r *domain.ComplianceRevokeRequest) { r.Reference = strings.Repeat("a", 65) },
			want: []domain.FieldError{{Field: "reference", Code: domain.CodeOutOfRange}},
		},
		{
			name:   "reference that is short once the spaces are collapsed",
			mutate: func(r *domain.ComplianceRevokeRequest) { r.Reference = "  a        " },
			want:   []domain.FieldError{{Field: "reference", Code: domain.CodeOutOfRange}},
		},
		{
			name: "reference missing", mutate: func(r *domain.ComplianceRevokeRequest) { r.Reference = "" },
			want: []domain.FieldError{{Field: "reference", Code: domain.CodeRequired}},
		},
		{
			name: "reference of only spaces", mutate: func(r *domain.ComplianceRevokeRequest) { r.Reference = " \t  " },
			want: []domain.FieldError{{Field: "reference", Code: domain.CodeRequired}},
		},
		{
			name: "reference with a control character", mutate: func(r *domain.ComplianceRevokeRequest) { r.Reference = "TCK-\x00-1" },
			want: []domain.FieldError{{Field: "reference", Code: domain.CodeInvalidFormat}},
		},
		{
			name: "reference with a symbol that is not allowed", mutate: func(r *domain.ComplianceRevokeRequest) { r.Reference = "TCK<script>" },
			want: []domain.FieldError{{Field: "reference", Code: domain.CodeInvalidFormat}},
		},
		{
			name: "reference with an accent", mutate: func(r *domain.ComplianceRevokeRequest) { r.Reference = "Orden-Ñ1" },
			want: []domain.FieldError{{Field: "reference", Code: domain.CodeInvalidFormat}},
		},
		{
			name: "reason missing", mutate: func(r *domain.ComplianceRevokeRequest) { r.Reason = "" },
			want: []domain.FieldError{{Field: "reason", Code: domain.CodeRequired}},
		},
		{
			name: "reason unknown", mutate: func(r *domain.ComplianceRevokeRequest) { r.Reason = "because" },
			want: []domain.FieldError{{Field: "reason", Code: domain.CodeInvalidFormat}},
		},
		{
			name: "reason with another case", mutate: func(r *domain.ComplianceRevokeRequest) { r.Reason = "Customer_Request" },
			want: []domain.FieldError{{Field: "reason", Code: domain.CodeInvalidFormat}},
		},
		{
			name: "role not confirmed", mutate: func(r *domain.ComplianceRevokeRequest) { r.RoleConfirmed = false },
			want: []domain.FieldError{{Field: "role_confirmed", Code: domain.CodeMustBeTrue}},
		},
		{
			name:   "everything missing names the three fields at once",
			mutate: func(r *domain.ComplianceRevokeRequest) { *r = domain.ComplianceRevokeRequest{} },
			want: []domain.FieldError{
				{Field: "reason", Code: domain.CodeRequired},
				{Field: "reference", Code: domain.CodeRequired},
				{Field: "role_confirmed", Code: domain.CodeMustBeTrue},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := valid
			tc.mutate(&req)
			err := req.Validate()
			if len(tc.want) == 0 {
				assert.NoError(t, err)
				return
			}
			assert.Equal(t, tc.want, complianceFields(t, err))
		})
	}
}

func TestComplianceRevokeRequestNormalizedReference(t *testing.T) {
	r := domain.ComplianceRevokeRequest{Reference: "  TCK-2026   0412 \t"}
	assert.Equal(t, "TCK-2026 0412", r.NormalizedReference())
}

func TestComplianceRevokeRequestDevice(t *testing.T) {
	cases := []struct {
		name, userAgent, want string
	}{
		{name: "empty", userAgent: "", want: ""},
		{name: "only spaces", userAgent: "   ", want: ""},
		{name: "a browser", userAgent: "Mozilla/5.0 (Macintosh)", want: "Mozilla/5.0 (Macintosh)"},
		{name: "spaces collapsed", userAgent: "  Mozilla/5.0   (X11)  ", want: "Mozilla/5.0 (X11)"},
		{name: "exactly 120 characters", userAgent: strings.Repeat("a", 120), want: strings.Repeat("a", 120)},
		{name: "121 characters are cut to 120", userAgent: strings.Repeat("a", 121), want: strings.Repeat("a", 120)},
		{name: "10 KB are cut to 120", userAgent: strings.Repeat("b", 10*1024), want: strings.Repeat("b", 120)},
		{name: "multi-byte characters count as one", userAgent: strings.Repeat("ñ", 121), want: strings.Repeat("ñ", 120)},
		{name: "the cut does not leave a trailing space", userAgent: strings.Repeat("a", 119) + " bbb", want: strings.Repeat("a", 119)},
		{name: "control characters removed", userAgent: "Moz\x00illa\x07/5.0", want: "Mozilla/5.0"},
		{name: "invalid UTF-8 removed", userAgent: "Mozilla\xff/5.0", want: "Mozilla/5.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.ComplianceRevokeRequest{UserAgent: tc.userAgent}.Device()
			assert.Equal(t, tc.want, got)
			assert.LessOrEqual(t, len([]rune(got)), domain.MaxDeviceLength)
		})
	}
}

func TestValidatePortalCommand(t *testing.T) {
	at := time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)
	cases := []struct {
		name, reason, reference string
		requestedAt             time.Time
		want                    []domain.FieldError
	}{
		{name: "valid", reason: "customer_request", reference: "TCK-1", requestedAt: at},
		{name: "valid with a regulatory order", reason: "regulatory_order", reference: "ORD 7", requestedAt: at},
		{
			name: "reason unknown", reason: "x", reference: "TCK-1", requestedAt: at,
			want: []domain.FieldError{{Field: "reason", Code: domain.CodeInvalidFormat}},
		},
		{
			name: "reference empty", reason: "customer_request", reference: " ", requestedAt: at,
			want: []domain.FieldError{{Field: "reference", Code: domain.CodeRequired}},
		},
		{
			name: "reference too short", reason: "customer_request", reference: "ab", requestedAt: at,
			want: []domain.FieldError{{Field: "reference", Code: domain.CodeOutOfRange}},
		},
		{
			name: "requested_at missing", reason: "customer_request", reference: "TCK-1",
			want: []domain.FieldError{{Field: "requested_at", Code: domain.CodeRequired}},
		},
		{
			name: "all three at once",
			want: []domain.FieldError{
				{Field: "reason", Code: domain.CodeRequired},
				{Field: "reference", Code: domain.CodeRequired},
				{Field: "requested_at", Code: domain.CodeRequired},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := domain.ValidatePortalCommand(tc.reason, tc.reference, tc.requestedAt)
			if len(tc.want) == 0 {
				assert.NoError(t, err)
				return
			}
			assert.Equal(t, tc.want, complianceFields(t, err))
		})
	}
}

func TestRevocationReasonValid(t *testing.T) {
	assert.True(t, domain.ReasonCustomerRequest.Valid())
	assert.True(t, domain.ReasonRegulatoryOrder.Valid())
	assert.False(t, domain.RevocationReason("").Valid())
	assert.False(t, domain.RevocationReason("other").Valid())
}

func TestMaskDocument(t *testing.T) {
	cases := []struct{ doc, want string }{
		{"", ""},
		{"1234", "****"},
		{"123", "***"},
		{"12345", "*2345"},
		{"1020304050", "******4050"},
		{"  1020304050 ", "******4050"},
		{"AB12345678", "******5678"},
	}
	for _, tc := range cases {
		t.Run(tc.doc, func(t *testing.T) {
			got := domain.MaskDocument(tc.doc)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, len([]rune(strings.TrimSpace(tc.doc))), len([]rune(got)), "the mask keeps the length")
		})
	}
}
