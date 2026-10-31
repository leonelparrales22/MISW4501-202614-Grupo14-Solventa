package httpadapter_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/claims/internal/domain"
)

// The estimated amount in the web filing and in the detail of a claim
// (HU-W2.2-01, decision D12).

func webBodyWithAmount(amount string) string {
	return strings.Replace(webFilingBody, `"supports":`, `"estimated_amount_minor":`+amount+`,"supports":`, 1)
}

func TestTheAmountReachesTheFilingAsItCame(t *testing.T) {
	cases := map[string]string{
		"480000000":   "480000000",
		`"480000000"`: `"480000000"`,
		"4.8e8":       "4.8e8",
		"null":        "",
	}
	for raw, want := range cases {
		web := &fakeWeb{claim: webClaim(), created: true}

		rec := fileWeb(webRouter(web, &fakeUploads{}, &fakeReads{}), "insured", "form-1", webBodyWithAmount(raw))

		require.Equal(t, http.StatusCreated, rec.Code, raw)
		assert.Equal(t, want, web.gotRequest.EstimatedAmountMinor, raw)
	}

	web := &fakeWeb{claim: webClaim(), created: true}
	fileWeb(webRouter(web, &fakeUploads{}, &fakeReads{}), "insured", "form-1", webFilingBody)
	assert.Empty(t, web.gotRequest.EstimatedAmountMinor, "absent")
}

func TestAnAmountThatIsNotAnIntegerIsAFieldError(t *testing.T) {
	web := &fakeWeb{err: &domain.ValidationError{Fields: []domain.FieldError{
		{Field: "estimated_amount_minor", Code: domain.CodeInvalidFormat}}}}

	rec := fileWeb(webRouter(web, &fakeUploads{}, &fakeReads{}), "insured", "form-1", webBodyWithAmount(`"mucho"`))

	require.Equal(t, http.StatusUnprocessableEntity, rec.Code, "not 400: the body is valid JSON")
	assert.Equal(t, "validation_failed", decode(t, rec)["error"])
}

func TestTheDetailShowsTheAmountOnlyWhenItWasDeclared(t *testing.T) {
	declared := webClaim()
	amount := int64(480000000)
	declared.EstimatedAmountMinor = &amount
	web := &fakeWeb{claim: declared, created: true}

	rec := fileWeb(webRouter(web, &fakeUploads{}, &fakeReads{}), "insured", "form-1", webBodyWithAmount("480000000"))
	require.Equal(t, http.StatusCreated, rec.Code)
	assert.InDelta(t, 480000000, decode(t, rec)["estimated_amount_minor"], 0)

	rec = do(webRouter(&fakeWeb{}, &fakeUploads{}, &fakeReads{claim: declared}), http.MethodGet,
		"/v1/claims/"+claimID, "insured", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.InDelta(t, 480000000, decode(t, rec)["estimated_amount_minor"], 0)

	rec = do(webRouter(&fakeWeb{}, &fakeUploads{}, &fakeReads{claim: webClaim()}), http.MethodGet,
		"/v1/claims/"+claimID, "insured", nil, nil)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, decode(t, rec), "estimated_amount_minor")
}
