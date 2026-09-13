// scripts/concurrencia dispara N goroutines contra
// POST /procesar-cobro/{aceptacion_id} para la MISMA aceptación,
// arrancando todas al mismo tiempo (barrera con canal cerrado), y luego
// consulta tanto el estado final en la base de datos propia
// (GET /cobros/{id}) como el conteo real de llamadas que recibió la
// pasarela (GET stub-pasarela/debug/cobros).
//
// Se usa un runner en Go con goroutines, en vez de k6, porque los VUs de
// k6 no garantizan simultaneidad de microsegundos (tienen su propio
// scheduling de conexión/DNS/arranque de iteración) y lo que este
// escenario necesita es una carrera lo más estrecha posible sobre el
// mismo aceptacion_id.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

type respuestaAceptacion struct {
	AceptacionID   string `json:"aceptacion_id"`
	IdempotencyKey string `json:"idempotency_key"`
}

type respuestaCobro struct {
	Estado string `json:"estado"`
}

type respuestaDebug struct {
	Conteos       map[string]int `json:"conteos"`
	CargosReales  map[string]int `json:"cargos_reales"`
	TotalRequests int            `json:"total_requests"`
}

type resultadoCorrida struct {
	AceptacionID        string `json:"aceptacion_id"`
	IdempotencyKey      string `json:"idempotency_key"`
	Goroutines          int    `json:"goroutines"`
	FilasConfirmadasBD  int    `json:"filas_confirmadas_bd"` // siempre 0 o 1: lo garantiza el UNIQUE
	EstadoFinal         string `json:"estado_final"`
	RequestsAlGateway   int    `json:"requests_al_gateway"`   // cuántas veces el gateway RECIBIÓ un request (red)
	CargosRealesGateway int    `json:"cargos_reales_gateway"` // cuántas veces se procesó como cobro NUEVO
	DuplicadoReal       bool   `json:"duplicado_real"`        // cargos_reales_gateway > 1: el hallazgo que importa
}

func main() {
	baseURL := envOr("BASE_URL", "http://localhost:8280")
	pasarelaDebugURL := envOr("PASARELA_DEBUG_URL", "http://localhost:9100")
	n := envIntOr("GOROUTINES", 10)

	resultado, err := correr(baseURL, pasarelaDebugURL, n)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	salida, _ := json.MarshalIndent(resultado, "", "  ")
	fmt.Println(string(salida))
}

func correr(baseURL, pasarelaDebugURL string, n int) (resultadoCorrida, error) {
	client := &http.Client{Timeout: 5 * time.Second}

	aceptacion, err := crearAceptacion(client, baseURL)
	if err != nil {
		return resultadoCorrida{}, fmt.Errorf("creando aceptacion: %w", err)
	}

	var wg sync.WaitGroup
	barrera := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-barrera
			_, _ = procesarCobro(client, baseURL, aceptacion.AceptacionID)
		}()
	}
	close(barrera) // todas arrancan en el mismo instante
	wg.Wait()

	// Pequeño margen para que cualquier escritura asíncrona en curso
	// termine antes de leer el estado final.
	time.Sleep(200 * time.Millisecond)

	estadoFinal, err := obtenerCobro(client, baseURL, aceptacion.AceptacionID)
	if err != nil {
		return resultadoCorrida{}, fmt.Errorf("consultando estado final: %w", err)
	}

	debug, err := obtenerDebugPasarela(client, pasarelaDebugURL)
	if err != nil {
		return resultadoCorrida{}, fmt.Errorf("consultando debug de la pasarela: %w", err)
	}
	requests := debug.Conteos[aceptacion.IdempotencyKey]
	cargosReales := debug.CargosReales[aceptacion.IdempotencyKey]

	filasConfirmadas := 0
	if estadoFinal.Estado == "confirmado" {
		filasConfirmadas = 1
	}

	return resultadoCorrida{
		AceptacionID:        aceptacion.AceptacionID,
		IdempotencyKey:      aceptacion.IdempotencyKey,
		Goroutines:          n,
		FilasConfirmadasBD:  filasConfirmadas,
		EstadoFinal:         estadoFinal.Estado,
		RequestsAlGateway:   requests,
		CargosRealesGateway: cargosReales,
		DuplicadoReal:       cargosReales > 1,
	}, nil
}

func crearAceptacion(client *http.Client, baseURL string) (respuestaAceptacion, error) {
	body, _ := json.Marshal(map[string]any{"cliente_id": "cliente-concurrencia", "monto_centavos": 15000})
	resp, err := client.Post(baseURL+"/aceptaciones", "application/json", bytes.NewReader(body))
	if err != nil {
		return respuestaAceptacion{}, err
	}
	defer resp.Body.Close()
	var out respuestaAceptacion
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return respuestaAceptacion{}, err
	}
	return out, nil
}

func procesarCobro(client *http.Client, baseURL, aceptacionID string) ([]byte, error) {
	resp, err := client.Post(baseURL+"/procesar-cobro/"+aceptacionID, "application/json", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func obtenerCobro(client *http.Client, baseURL, aceptacionID string) (respuestaCobro, error) {
	resp, err := client.Get(baseURL + "/cobros/" + aceptacionID)
	if err != nil {
		return respuestaCobro{}, err
	}
	defer resp.Body.Close()
	var out respuestaCobro
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return respuestaCobro{}, err
	}
	return out, nil
}

func obtenerDebugPasarela(client *http.Client, pasarelaDebugURL string) (respuestaDebug, error) {
	resp, err := client.Get(pasarelaDebugURL + "/debug/cobros")
	if err != nil {
		return respuestaDebug{}, err
	}
	defer resp.Body.Close()
	var out respuestaDebug
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return respuestaDebug{}, err
	}
	return out, nil
}
