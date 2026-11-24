package main

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/domain"
)

// lazyDB is a pool that never connects until it is used: enough to wire the
// list without a database.
func lazyDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", "postgres://origination@127.0.0.1:1/origination?sslmode=disable")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestExpiringListIsMountedOnlyWithTheCharge(t *testing.T) {
	without, err := expiringOptions(false, domain.DefaultRenewalPolicy, nil, noVerifier{})
	require.NoError(t, err)
	assert.Empty(t, without, "without the premium charge there are no policies to list")

	with, err := expiringOptions(true, domain.DefaultRenewalPolicy, lazyDB(t), noVerifier{})
	require.NoError(t, err)
	require.Len(t, with, 1)

	router := httpadapter.NewRouter(nil, append([]httpadapter.Option{httpadapter.WithObservability(serviceName)}, with...)...)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/policies/expiring", nil))
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "the route is mounted behind the authentication")
}

func TestExpiringListNeedsTheDatabase(t *testing.T) {
	_, err := newExpiringService(domain.DefaultRenewalPolicy, nil)
	assert.ErrorContains(t, err, "DATABASE_URL")

	_, err = expiringOptions(true, domain.DefaultRenewalPolicy, nil, noVerifier{})
	assert.ErrorContains(t, err, "DATABASE_URL")

	_, err = newExpiringService(domain.RenewalPolicy{}, lazyDB(t))
	assert.Error(t, err, "the renewal rules must be valid")
}
