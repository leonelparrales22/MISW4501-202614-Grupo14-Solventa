package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var fixedNow = time.Date(2026, 10, 24, 15, 30, 0, 0, time.UTC)

func clock() time.Time { return fixedNow }

func TestBuildPoliciesIsDeterministicPerHolder(t *testing.T) {
	a, b := buildPolicies("sub-1", fixedNow), buildPolicies("sub-1", fixedNow)

	assert.Equal(t, a, b)
	assert.NotEqual(t, a[0].PolicyID, buildPolicies("sub-2", fixedNow)[0].PolicyID)
}

func TestBuildPoliciesStayTheSameAllDayLong(t *testing.T) {
	morning := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)
	night := time.Date(2026, 10, 24, 23, 59, 59, 0, time.UTC)

	assert.Equal(t, buildPolicies("sub-1", morning), buildPolicies("sub-1", night))
}

func TestBuildPoliciesGivesEveryLineAndAnExpiredPolicy(t *testing.T) {
	idPattern := regexp.MustCompile(`^(AU|HG|VJ)-\d{4}-\d{6}$`)
	for _, holder := range []string{"a", "b", "c", "0b7e2c4a-1d1f-4c6e-9a77-3e5f0f6d2a10"} {
		policies := buildPolicies(holder, fixedNow)
		require.Len(t, policies, 4)

		auto, home, travel, expired := policies[0], policies[1], policies[2], policies[3]
		assert.Equal(t, lineAuto, auto.Line)
		assert.Equal(t, lineHome, home.Line)
		assert.Equal(t, lineTravel, travel.Line)
		assert.Equal(t, lineAuto, expired.Line)

		for _, p := range policies[:3] {
			assert.Equal(t, statusActive, p.Status)
			assert.Truef(t, !fixedNow.Before(p.ValidFrom) && fixedNow.Before(p.ValidTo),
				"%s must be active now: %s – %s", p.PolicyID, p.ValidFrom, p.ValidTo)
			assert.GreaterOrEqual(t, p.ValidTo.Sub(fixedNow), 35*24*time.Hour)
		}
		assert.Equal(t, statusExpired, expired.Status)
		assert.Equal(t, fixedNow.UTC().Truncate(24*time.Hour).AddDate(0, 0, -30), expired.ValidTo)

		for _, p := range policies {
			assert.Regexp(t, idPattern, p.PolicyID)
			assert.NotEmpty(t, p.InsuredObject)
			assert.NotEmpty(t, p.CoveredIncidents)
		}
		assert.Regexp(t, `^[A-Z]{3}-\d{3}$`, auto.InsuredObject)
		assert.Equal(t, []string{"water_damage"}, home.CoveredIncidents)
		assert.Equal(t, []string{"flight_delay"}, travel.CoveredIncidents)
	}
}

func TestTheAutoPolicyCoversGlassOnlyForSomeHolders(t *testing.T) {
	covers, lacks := false, false
	for i := 0; i < 50; i++ {
		auto := buildPolicies("holder-"+string(rune('a'+i%26))+string(rune('a'+i/26)), fixedNow)[0]
		if contains(auto.CoveredIncidents, "glass") {
			covers = true
		} else {
			lacks = true
		}
		assert.True(t, contains(auto.CoveredIncidents, "collision"))
	}
	assert.True(t, covers, "some holder must have glass covered")
	assert.True(t, lacks, "some holder must not have glass covered")
}

func TestHandlerServesThePoliciesOfAHolder(t *testing.T) {
	h := newHandler(0, seedData{}, clock)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/holders/sub-1/policies", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var body policiesResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, buildPolicies("sub-1", fixedNow), body.Policies)
}

func TestHandlerServesTheSeededPolicies(t *testing.T) {
	from := time.Date(2026, 1, 1, 5, 0, 0, 0, time.UTC)
	seed := map[string][]policy{
		"seeded": {{PolicyID: "AU-2026-000001", Line: lineAuto, Product: "Autos", InsuredObject: "ABC-123",
			Status: statusActive, ValidFrom: from, ValidTo: from.AddDate(1, 0, 0), CoveredIncidents: []string{"collision"}}},
		"without-policies": {},
	}
	h := newHandler(0, seedData{policies: seed}, clock)

	var body policiesResponse
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/holders/seeded/policies", nil))
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Equal(t, seed["seeded"], body.Policies)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/holders/without-policies/policies", nil))
	assert.JSONEq(t, `{"policies":[]}`, rec.Body.String())
}

func TestHandlerRejectsAnInvalidHolder(t *testing.T) {
	h := newHandler(0, seedData{}, clock)

	for _, id := range []string{"%20", strings.Repeat("x", maxHolderIDLength+1)} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/holders/"+id+"/policies", nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code, id)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/holders/"+strings.Repeat("x", maxHolderIDLength)+"/policies", nil))
	assert.Equal(t, http.StatusOK, rec.Code, "128 characters is the limit")
}

func TestHandlerServesHealth(t *testing.T) {
	rec := httptest.NewRecorder()
	newHandler(0, seedData{}, clock).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHandlerAppliesTheArtificialDelay(t *testing.T) {
	h := newHandler(30*time.Millisecond, seedData{}, clock)
	start := time.Now()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/holders/sub-1/policies", nil))
	assert.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond)
}

func TestLoadSeed(t *testing.T) {
	seed, err := loadSeed("")
	require.NoError(t, err)
	assert.Empty(t, seed.policies)
	assert.Empty(t, seed.profiles)

	dir := t.TempDir()
	good := filepath.Join(dir, "seed.json")
	require.NoError(t, os.WriteFile(good, []byte(`{"holders":{"sub-1":[{"policy_id":"HG-2026-000002","line":"home",
		"product":"Hogar","insured_object":"Calle 1","status":"active","valid_from":"2026-01-01T05:00:00Z",
		"valid_to":"2027-01-01T05:00:00Z","covered_incidents":["water_damage"]}],"sub-2":[]}}`), 0o600))
	seed, err = loadSeed(good)
	require.NoError(t, err)
	require.Len(t, seed.policies["sub-1"], 1)
	assert.Equal(t, "HG-2026-000002", seed.policies["sub-1"][0].PolicyID)
	assert.Empty(t, seed.policies["sub-2"])
	assert.Empty(t, seed.profiles)

	empty := filepath.Join(dir, "empty.json")
	require.NoError(t, os.WriteFile(empty, []byte(`{}`), 0o600))
	seed, err = loadSeed(empty)
	require.NoError(t, err)
	assert.Empty(t, seed.policies)

	bad := filepath.Join(dir, "bad.json")
	require.NoError(t, os.WriteFile(bad, []byte(`{not json`), 0o600))
	_, err = loadSeed(bad)
	assert.Error(t, err)

	_, err = loadSeed(filepath.Join(dir, "missing.json"))
	assert.Error(t, err)
}

func TestEveryPolicyCarriesTheTermsOfEachCoverage(t *testing.T) {
	for _, p := range buildPolicies("sub-1", fixedNow) {
		require.Len(t, p.Coverages, len(p.CoveredIncidents), p.PolicyID)
		for i, c := range p.Coverages {
			assert.Equal(t, p.CoveredIncidents[i], c.Incident)
			assert.Equal(t, "COP", c.Currency)
			assert.Positive(t, c.InsuredValueMinor)
			assert.GreaterOrEqual(t, c.DeductibleBasisPoints, 0)
			assert.Less(t, c.DeductibleBasisPoints, 10_000)
		}
	}
	home := buildPolicies("sub-1", fixedNow)[1]
	assert.Equal(t, coverage{Incident: "water_damage", InsuredValueMinor: 32_000_000_000, Currency: "COP",
		DeductibleBasisPoints: 500}, home.Coverages[0], "the figures of screen O6")
}

func TestHandlerServesOnePolicyOfTheHolder(t *testing.T) {
	h := newHandler(0, seedData{}, clock)
	want := buildPolicies("sub-1", fixedNow)[1]

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/holders/sub-1/policies/"+want.PolicyID, nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var got policy
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, want, got)
}

func TestHandlerHidesThePoliciesOfAnotherHolder(t *testing.T) {
	h := newHandler(0, seedData{}, clock)
	theirs := buildPolicies("sub-2", fixedNow)[0].PolicyID

	for _, id := range []string{theirs, "AU-2026-999999"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/holders/sub-1/policies/"+id, nil))
		assert.Equal(t, http.StatusNotFound, rec.Code, id)
	}
}

func TestHandlerServesTheProfileOfTheHolder(t *testing.T) {
	h := newHandler(0, seedData{}, clock)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/holders/sub-1", nil))

	require.Equal(t, http.StatusOK, rec.Code)
	var got holder
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, buildHolder("sub-1"), got)
}

func TestHandlerServesTheSeededProfile(t *testing.T) {
	seeded := holder{HolderID: "seeded", FullName: "Carolina Pérez Ruiz", DocumentType: "CC", DocumentID: "1020345678",
		Email: "carolina.perez@correo.example", Phone: "+57 301 234 5678",
		PayoutAccount: payoutAccount{BankCode: "1007", BankName: "Banco Aliado", AccountType: "savings",
			AccountNumber: "0123-4567-89"}}
	h := newHandler(0, seedData{profiles: map[string]holder{"seeded": seeded}}, clock)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/holders/seeded", nil))

	var got holder
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, seeded, got)
}

func TestBuildHolderIsDeterministicAndPays(t *testing.T) {
	assert.Equal(t, buildHolder("sub-1"), buildHolder("sub-1"))
	assert.NotEqual(t, buildHolder("sub-1").DocumentID, buildHolder("sub-2").DocumentID)

	for i := 0; i < 200; i++ {
		h := buildHolder("holder-" + strconv.Itoa(i))
		assert.Regexp(t, `^\d{4}-\d{4}-\d{2}$`, h.PayoutAccount.AccountNumber)
		assert.False(t, strings.HasSuffix(h.PayoutAccount.AccountNumber, "89"),
			"89 is the ending the gateway stub rejects")
		assert.Regexp(t, `^[a-z]+\.\d+@correo\.example$`, h.Email)
		assert.NotEmpty(t, h.FullName)
		assert.Equal(t, "savings", h.PayoutAccount.AccountType)
	}
}

func TestLoadSeedReadsTheProfiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seed.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"profiles":{"sub-1":{"full_name":"Carolina Pérez Ruiz",
		"payout_account":{"bank_code":"1007","account_number":"0123-4567-89"}}}}`), 0o600))

	seed, err := loadSeed(path)

	require.NoError(t, err)
	assert.Equal(t, "sub-1", seed.profiles["sub-1"].HolderID, "the key is the holder id")
	assert.Equal(t, "0123-4567-89", seed.profiles["sub-1"].PayoutAccount.AccountNumber)
	assert.Empty(t, seed.policies)
}

func TestEnvInt(t *testing.T) {
	t.Setenv("DELAY", "250")
	assert.Equal(t, 250, envInt("DELAY"))
	t.Setenv("DELAY", "-5")
	assert.Equal(t, 0, envInt("DELAY"))
	t.Setenv("DELAY", "abc")
	assert.Equal(t, 0, envInt("DELAY"))
}

func TestOrDefault(t *testing.T) {
	assert.Equal(t, "x", orDefault("", "x"))
	assert.Equal(t, "y", orDefault("y", "x"))
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}
