// Package pasarela es el cliente HTTP hacia el stub de pasarela de pago
// (stub-pasarela/), típicamente puesto detrás de Toxiproxy para inyectar
// fallas controladas.
package pasarela

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrAmbiguo indica que la pasarela no respondió a tiempo (timeout de
// contexto, conexión rechazada, conexión cortada a mitad de respuesta).
// No sabemos si el proveedor procesó el cobro o no: SÍ se debe reintentar,
// reutilizando la misma idempotency_key.
var ErrAmbiguo = errors.New("pasarela no respondió a tiempo")

// ErrRechazado indica que la pasarela respondió explícitamente rechazando
// el cobro (4xx). No es ambiguo: NO se debe reintentar.
var ErrRechazado = errors.New("pasarela rechazó el cobro")

type Resultado struct {
	Estado         string `json:"estado"`
	IdempotencyKey string `json:"idempotency_key"`
}

type Client struct {
	httpClient *http.Client
	baseURL    string
}

func New(baseURL string, timeout time.Duration) *Client {
	return &Client{
		httpClient: &http.Client{Timeout: timeout},
		baseURL:    baseURL,
	}
}

type solicitudCobro struct {
	IdempotencyKey string `json:"idempotency_key"`
	AceptacionID   string `json:"aceptacion_id"`
	MontoCentavos  int64  `json:"monto_centavos"`
}

// Cobrar propaga la idempotency_key en el header Idempotency-Key y en el
// body, y clasifica el resultado en tres familias que el llamador nunca
// debe mezclar: éxito, ErrRechazado (fallo definitivo) y ErrAmbiguo
// (reintentable). El http.Client de Go no reintenta nada por sí mismo —
// la clasificación se hace a mano aquí.
func (c *Client) Cobrar(ctx context.Context, idempotencyKey, aceptacionID string, montoCentavos int64) (Resultado, error) {
	body, err := json.Marshal(solicitudCobro{
		IdempotencyKey: idempotencyKey,
		AceptacionID:   aceptacionID,
		MontoCentavos:  montoCentavos,
	})
	if err != nil {
		return Resultado{}, fmt.Errorf("codificando solicitud de cobro: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/cobros", bytes.NewReader(body))
	if err != nil {
		return Resultado{}, fmt.Errorf("construyendo request de cobro: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", idempotencyKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		// Cualquier error de transporte (timeout de contexto, conexión
		// rechazada por caída total, conexión cortada por el toxic
		// "timeout" de Toxiproxy) es, por definición, ambiguo: no
		// sabemos si el request llegó a procesarse del lado del
		// proveedor antes de que la respuesta se perdiera.
		return Resultado{}, fmt.Errorf("%w: %v", ErrAmbiguo, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		return Resultado{}, fmt.Errorf("%w: status %d", ErrRechazado, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return Resultado{}, fmt.Errorf("%w: status inesperado %d", ErrAmbiguo, resp.StatusCode)
	}

	var resultado Resultado
	if err := json.NewDecoder(resp.Body).Decode(&resultado); err != nil {
		return Resultado{}, fmt.Errorf("decodificando respuesta de cobro: %w", err)
	}
	return resultado, nil
}
