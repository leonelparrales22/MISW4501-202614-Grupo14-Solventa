package httpadapter_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/pkg/observability"
	httpadapter "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/services/origination/internal/adapters/http"
)

func TestWithObservabilityKeepsTheCorrelationIDOnEveryRoute(t *testing.T) {
	router := httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithObservability("origination"))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set(observability.CorrelationHeader, "demo-1")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "demo-1", rec.Header().Get(observability.CorrelationHeader))
}

func TestWithObservabilityGeneratesACorrelationIDWhenMissing(t *testing.T) {
	router := httpadapter.NewRouter(&fakeResolver{}, httpadapter.WithObservability("origination"))

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unknown", nil))

	assert.Equal(t, http.StatusNotFound, rec.Code)
	assert.NotEmpty(t, rec.Header().Get(observability.CorrelationHeader))
}
