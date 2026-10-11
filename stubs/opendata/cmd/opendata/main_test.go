package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBuildProfileIsDeterministicPerCustomer(t *testing.T) {
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)

	a, b := buildProfile("c1", now), buildProfile("c1", now)

	assert.Equal(t, a, b)
	assert.GreaterOrEqual(t, a.Score, 0.0)
	assert.Less(t, a.Score, 1.0)
	assert.NotEmpty(t, a.Category)
	assert.Equal(t, now, a.UpdatedAt)
}

func TestBuildProfileDiffersFromAnUnsaltedHash(t *testing.T) {
	// The salt keeps the Open Data signal independent from the other stubs.
	differs := 0
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("customer-%d", i)
		h := fnv.New32a()
		_, _ = h.Write([]byte(id))
		if buildProfile(id, time.Now()).Score != float64(h.Sum32()%1000)/1000.0 {
			differs++
		}
	}
	assert.Greater(t, differs, 40)
}

func TestBuildProfileCategoriesFollowTheScore(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		p := buildProfile(fmt.Sprintf("customer-%d", i), time.Now())
		seen[p.Category] = true
		switch {
		case p.Score > 0.8:
			assert.Equal(t, "high_risk", p.Category)
		case p.Score < 0.2:
			assert.Equal(t, "preferred", p.Category)
		default:
			assert.Equal(t, "standard", p.Category)
		}
	}
	assert.Len(t, seen, 3)
}

func TestEnvInt(t *testing.T) {
	t.Setenv("DELAY", "250")
	assert.Equal(t, 250, envInt("DELAY"))
	t.Setenv("DELAY", "-5")
	assert.Equal(t, 0, envInt("DELAY"))
	t.Setenv("DELAY", "abc")
	assert.Equal(t, 0, envInt("DELAY"))
	assert.Equal(t, 0, envInt("UNSET_VAR_FOR_TEST"))
}

func TestOrDefault(t *testing.T) {
	assert.Equal(t, "x", orDefault("", "x"))
	assert.Equal(t, "y", orDefault("y", "x"))
}

func TestHandlerServesProfilesAndHealth(t *testing.T) {
	h := newHandler(0)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/profiles/c1", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var p profile
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &p))
	assert.Equal(t, "c1", p.CustomerID)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	assert.Equal(t, http.StatusOK, rec.Code)

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/profiles/%20", nil))
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestHandlerAppliesTheArtificialDelay(t *testing.T) {
	h := newHandler(30 * time.Millisecond)
	start := time.Now()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/profiles/c1", nil))
	assert.GreaterOrEqual(t, time.Since(start), 30*time.Millisecond)
}
