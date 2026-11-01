package httpadapter_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/renewal"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/wallet"
)

type fakeWallet struct {
	wallet wallet.Wallet
	err    error
}

func (f *fakeWallet) List(context.Context) (wallet.Wallet, error) { return f.wallet, f.err }

func callWallet(t *testing.T, svc *fakeWallet, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/policies", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithObservability("origination"),
		httpadapter.WithPolicyWallet(svc, tokenVerifier{})).ServeHTTP(rec, req)
	return rec
}

type walletBody struct {
	Items   []map[string]any `json:"items"`
	Partial bool             `json:"partial"`
}

func decodeWallet(t *testing.T, rec *httptest.ResponseRecorder) walletBody {
	t.Helper()
	var body walletBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	return body
}

func corePolicy() domain.WalletPolicy {
	return domain.WalletPolicy{
		ID: "AU-2025-118204", Source: domain.SourcePolicyCore, Line: domain.LineAuto, Product: "Autos",
		InsuredObject: "KJT-482", Status: domain.PolicyActive,
		Holder:   domain.PolicyHolder{FullName: "Carolina Pérez Ruiz", DocumentType: "CC", DocumentID: "1032456789"},
		Currency: "COP", InsuredAmount: 72_500_000,
		Coverages:     []domain.Coverage{{Code: "collision", InsuredAmount: 72_500_000}},
		CoverageStart: time.Date(2025, 10, 2, 0, 0, 0, 0, time.UTC), CoverageEnd: time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC),
	}
}

func TestListPoliciesShowsTheWindowAndTheRenewalOfEachOne(t *testing.T) {
	p := renewalPolicy()
	view := renewal.PolicyView{Policy: p, CanRenew: true, WindowOpensAt: policyEndsAt.AddDate(0, 0, -30), WindowClosesAt: policyEndsAt}
	svc := &fakeWallet{wallet: wallet.Wallet{Items: []wallet.Item{
		{Policy: domain.WalletPolicyOf(p, policyEndsAt.AddDate(0, -1, 0)), Renewal: &view},
	}}}

	rec := callWallet(t, svc, "insured:sub-1")

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	body := decodeWallet(t, rec)
	require.Len(t, body.Items, 1)
	item := body.Items[0]
	assert.Equal(t, "pol-1", item["policy_id"])
	assert.Equal(t, "origination", item["source"])
	assert.Equal(t, "life", item["line"])
	assert.Equal(t, "active", item["status"])
	assert.Equal(t, 142_300.0, item["premium"])
	assert.Equal(t, true, item["can_renew"])
	assert.NotNil(t, item["renewal_window"])
	assert.Nil(t, item["renewal"])
	assert.False(t, body.Partial)
	assert.NotContains(t, rec.Body.String(), "email", "the list carries no contact data")
}

// HU-M1.2-01, criterion 2: a policy of the core carries its kind, its object,
// the insured value of each coverage and the main insured, and no renewal.
func TestListPoliciesDescribesAPolicyOfTheCore(t *testing.T) {
	svc := &fakeWallet{wallet: wallet.Wallet{Items: []wallet.Item{{Policy: corePolicy()}}}}

	rec := callWallet(t, svc, "insured:sub-1")

	require.Equal(t, http.StatusOK, rec.Code)
	item := decodeWallet(t, rec).Items[0]
	assert.Equal(t, "policy_core", item["source"])
	assert.Equal(t, "auto", item["line"])
	assert.Equal(t, "Autos", item["product"])
	assert.Equal(t, "KJT-482", item["insured_object"])
	assert.Nil(t, item["premium"], "the policy core keeps no premiums")
	assert.Nil(t, item["renewal_window"])
	assert.Equal(t, false, item["can_renew"])
	assert.Equal(t, "Carolina Pérez Ruiz", item["holder_name"])
	assert.Equal(t, map[string]any{"full_name": "Carolina Pérez Ruiz", "document_type": "CC", "document_id": "1032456789"}, item["holder"])
	assert.Equal(t, []any{map[string]any{"code": "collision", "insured_amount": 72_500_000.0}}, item["coverages"])
	assert.Equal(t, "2026-10-02T00:00:00Z", item["coverage_end"])
}

func TestListPoliciesSaysWhenTheCoreDidNotAnswer(t *testing.T) {
	rec := callWallet(t, &fakeWallet{wallet: wallet.Wallet{Items: []wallet.Item{}, Partial: true}}, "insured:sub-1")

	require.Equal(t, http.StatusOK, rec.Code)
	body := decodeWallet(t, rec)
	assert.True(t, body.Partial)
	assert.Empty(t, body.Items)
	assert.Contains(t, rec.Body.String(), `"items":[]`, "an empty wallet is a list, never null")
}

func TestListPoliciesIsOnlyForTheInsured(t *testing.T) {
	assert.Equal(t, http.StatusUnauthorized, callWallet(t, &fakeWallet{}, "").Code)
	assert.Equal(t, http.StatusForbidden, callWallet(t, &fakeWallet{}, "advisor:sub-2").Code)
	assert.Equal(t, http.StatusForbidden, callWallet(t, &fakeWallet{}, "operations:ops").Code)
}

func TestListPoliciesAnswers503WhenOriginationsStoreFails(t *testing.T) {
	rec := callWallet(t, &fakeWallet{err: errors.New("database down")}, "insured:sub-1")

	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Contains(t, rec.Body.String(), "renewal_unavailable")
}
