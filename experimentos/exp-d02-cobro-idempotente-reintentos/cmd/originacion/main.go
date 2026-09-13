// cmd/originacion es el servicio de originación del experimento EC-D02:
// expone POST /aceptaciones, POST /procesar-cobro/{aceptacion_id},
// GET /cobros/{aceptacion_id} y GET /health. Toda la configuración del
// punto de sensibilidad (backoff, ventana de reintentos, polling del
// worker) se recibe por variables de entorno, para poder acortarla en
// las pruebas sin recompilar.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"solventa/exp-d02-cobro-idempotente-reintentos/internal/cobro"
	"solventa/exp-d02-cobro-idempotente-reintentos/internal/pasarela"
	"solventa/exp-d02-cobro-idempotente-reintentos/internal/reintentos"
	"solventa/exp-d02-cobro-idempotente-reintentos/internal/store"
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

func main() {
	port := envOr("PORT", "8080")
	dbURL := envOr("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/cobros?sslmode=disable")
	pasarelaURL := envOr("PASARELA_URL", "http://localhost:9100")

	// --- Punto de sensibilidad del experimento ---
	backoffBaseMs := envIntOr("BACKOFF_BASE_MS", 500)
	backoffMaxMs := envIntOr("BACKOFF_MAX_MS", 60_000)
	retryWindowSeconds := envIntOr("RETRY_WINDOW_SECONDS", 24*3600) // ADR: ventana de 24h
	workerPollMs := envIntOr("WORKER_POLL_MS", 1000)
	hardTimeoutMs := envIntOr("HARD_TIMEOUT_MS", 700)

	db, err := sql.Open("pgx", dbURL)
	if err != nil {
		log.Fatalf("originacion: abriendo conexión a postgres: %v", err)
	}
	defer db.Close()

	st := store.New(db)
	client := pasarela.New(pasarelaURL, time.Duration(hardTimeoutMs)*time.Millisecond)
	servicio := cobro.New(st, client, cobro.Config{
		BackoffBase: time.Duration(backoffBaseMs) * time.Millisecond,
		BackoffMax:  time.Duration(backoffMaxMs) * time.Millisecond,
		Ventana:     time.Duration(retryWindowSeconds) * time.Second,
	})

	worker := reintentos.New(st, servicio.ProcesarCobro, time.Duration(workerPollMs)*time.Millisecond)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	go worker.Run(ctx)

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		estadoDB := "ok"
		if err := db.PingContext(r.Context()); err != nil {
			estadoDB = "error: " + err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok", "db": estadoDB})
	})

	mux.HandleFunc("/aceptaciones", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			ClienteID     string `json:"cliente_id"`
			MontoCentavos int64  `json:"monto_centavos"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.ClienteID == "" {
			http.Error(w, "cliente_id requerido", http.StatusBadRequest)
			return
		}

		// El journey NUNCA pierde la aceptación aunque la pasarela esté
		// caída: se persiste y se responde 201 antes de intentar cobrar.
		aceptacion, intento, err := servicio.CrearAceptacion(r.Context(), body.ClienteID, body.MontoCentavos)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"aceptacion_id":    aceptacion.ID,
			"intento_cobro_id": intento.ID,
			"idempotency_key":  intento.IdempotencyKey,
			"estado":           intento.Estado,
			"creada_en":        aceptacion.CreadaEn,
		})
	})

	mux.HandleFunc("/procesar-cobro/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		aceptacionID := strings.TrimPrefix(r.URL.Path, "/procesar-cobro/")
		if aceptacionID == "" {
			http.Error(w, "aceptacion_id requerido en la ruta", http.StatusBadRequest)
			return
		}

		intento, err := servicio.ProcesarCobro(r.Context(), aceptacionID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"aceptacion_id":   intento.AceptacionID,
			"estado":          intento.Estado,
			"intentos":        intento.Intentos,
			"idempotency_key": intento.IdempotencyKey,
			"ultimo_error":    intento.UltimoError,
		})
	})

	mux.HandleFunc("/cobros/", func(w http.ResponseWriter, r *http.Request) {
		aceptacionID := strings.TrimPrefix(r.URL.Path, "/cobros/")
		if aceptacionID == "" {
			http.Error(w, "aceptacion_id requerido en la ruta", http.StatusBadRequest)
			return
		}
		intento, encontrado, err := st.ObtenerIntentoPorAceptacion(r.Context(), aceptacionID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !encontrado {
			http.Error(w, "no existe intento de cobro para esa aceptacion", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(intento)
	})

	log.Printf("originacion: escuchando en :%s (backoff=%d-%dms ventana=%ds hard_timeout=%dms poll=%dms)",
		port, backoffBaseMs, backoffMaxMs, retryWindowSeconds, hardTimeoutMs, workerPollMs)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}
