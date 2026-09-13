package pasarela_test

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

	"solventa/exp-d02-cobro-idempotente-reintentos/internal/pasarela"
)

func TestCobrar_Exito_PropagaIdempotencyKey(t *testing.T) {
	var keyRecibida string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		keyRecibida = r.Header.Get("Idempotency-Key")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"estado": "aprobado", "idempotency_key": keyRecibida})
	}))
	defer srv.Close()

	client := pasarela.New(srv.URL, time.Second)
	resultado, err := client.Cobrar(context.Background(), "cobro-a1", "a1", 15000)

	require.NoError(t, err)
	assert.Equal(t, "aprobado", resultado.Estado)
	assert.Equal(t, "cobro-a1", keyRecibida)
}

func TestCobrar_RechazoExplicito_EsErrRechazado(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rechazado", http.StatusPaymentRequired)
	}))
	defer srv.Close()

	client := pasarela.New(srv.URL, time.Second)
	_, err := client.Cobrar(context.Background(), "cobro-a2", "a2", 1000)

	require.Error(t, err)
	assert.True(t, errors.Is(err, pasarela.ErrRechazado))
	assert.False(t, errors.Is(err, pasarela.ErrAmbiguo))
}

func TestCobrar_TimeoutDeContexto_EsErrAmbiguo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Timeout del cliente mucho menor que el delay del servidor: simula el
	// caso ambiguo (el request llega y se procesa, pero el cliente nunca
	// ve la respuesta a tiempo).
	client := pasarela.New(srv.URL, 20*time.Millisecond)
	_, err := client.Cobrar(context.Background(), "cobro-a3", "a3", 1000)

	require.Error(t, err)
	assert.True(t, errors.Is(err, pasarela.ErrAmbiguo))
	assert.False(t, errors.Is(err, pasarela.ErrRechazado))
}

func TestCobrar_ConexionRechazada_EsErrAmbiguo(t *testing.T) {
	// Puerto que casi con certeza no tiene nada escuchando: simula la
	// caída total de la pasarela (Toxiproxy con enabled:false).
	client := pasarela.New("http://127.0.0.1:1", 200*time.Millisecond)
	_, err := client.Cobrar(context.Background(), "cobro-a4", "a4", 1000)

	require.Error(t, err)
	assert.True(t, errors.Is(err, pasarela.ErrAmbiguo))
}
