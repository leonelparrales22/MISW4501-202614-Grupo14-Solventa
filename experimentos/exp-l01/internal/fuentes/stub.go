// Package fuentes simula a los proveedores externos: Open Finance, Open Data,
// KYC, y de la 4 a la 9 que son inventadas para poder trazar la curva.
// Cada /fuente/{id} se demora un tiempo aleatorio que sigue una lognormal con
// el p50 y el p95 que le configuremos. Así cada ejecución es repetible.
package fuentes

import (
	"encoding/json"
	"log"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"strconv"
	"time"
)

// Config es el perfil de latencia de las fuentes.
//
//	normal    → P50 = 60 ms,  P95 = 200 ms
//	degradado → P50 = 150 ms, P95 = 600 ms
//
// PctSinRespuesta es qué porcentaje de consultas se quedan colgadas más allá
// de cualquier corte. Simula un proveedor que se cayó pero no cierra la conexión.
type Config struct {
	P50             time.Duration
	P95             time.Duration
	PctSinRespuesta float64
	EsperaColgada   time.Duration
}

// ConfigDesdeEnv lee LAT_P50_MS, LAT_P95_MS y PCT_SIN_RESPUESTA.
func ConfigDesdeEnv() Config {
	return Config{
		P50:             milis("LAT_P50_MS", 60),
		P95:             milis("LAT_P95_MS", 200),
		PctSinRespuesta: flotante("PCT_SIN_RESPUESTA", 0),
		EsperaColgada:   milis("ESPERA_COLGADA_MS", 5000),
	}
}

// Servidor es la fuente simulada HTTP.
type Servidor struct {
	mux   *http.ServeMux
	cfg   Config
	mu    float64 // media del logaritmo
	sigma float64 // desviación del logaritmo
}

// Nuevo saca mu y sigma de la lognormal a partir del p50 y el p95:
//
//	mediana = e^mu               → mu    = ln(p50)
//	p95     = e^(mu + 1.6449 σ)  → sigma = (ln(p95) − ln(p50)) / 1.6449
func Nuevo(cfg Config) *Servidor {
	p50 := float64(cfg.P50.Milliseconds())
	p95 := float64(cfg.P95.Milliseconds())
	if p95 <= p50 {
		p95 = p50 * 1.5
	}
	s := &Servidor{
		mux:   http.NewServeMux(),
		cfg:   cfg,
		mu:    math.Log(p50),
		sigma: (math.Log(p95) - math.Log(p50)) / 1.6449,
	}
	s.mux.HandleFunc("GET /fuente/{id}", s.consultar)
	s.mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	log.Printf("fuentes: p50=%s p95=%s pct_sin_respuesta=%.2f (mu=%.3f sigma=%.3f)",
		cfg.P50, cfg.P95, cfg.PctSinRespuesta, s.mu, s.sigma)
	return s
}

func (s *Servidor) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// consultar espera lo que le tocó y responde. Si el cliente ya canceló (se le
// venció el corte), salimos de una: no tiene sentido dejar goroutines
// dormidas esperando a alguien que ya se fue.
func (s *Servidor) consultar(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	espera := s.muestrear()
	if s.cfg.PctSinRespuesta > 0 && rand.Float64() < s.cfg.PctSinRespuesta {
		espera = s.cfg.EsperaColgada
	}

	select {
	case <-time.After(espera):
	case <-r.Context().Done():
		return // el orquestador ya canceló, no hay a quién responderle
	}

	idNum, _ := strconv.Atoi(id)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"fuente":      "fuente-" + id,
		"cliente":     r.URL.Query().Get("cliente"),
		"valor":       float64(idNum%7) + rand.Float64(), // un valor cualquiera, lo que importa es la latencia
		"latencia_ms": espera.Milliseconds(),
	})
}

// muestrear saca una duración de la lognormal(mu, sigma).
func (s *Servidor) muestrear() time.Duration {
	z := rand.NormFloat64()
	ms := math.Exp(s.mu + s.sigma*z)
	return time.Duration(ms * float64(time.Millisecond))
}

// --- lectura de entorno ---

func milis(clave string, def int) time.Duration {
	if v := os.Getenv(clave); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return time.Duration(n) * time.Millisecond
		}
	}
	return time.Duration(def) * time.Millisecond
}

func flotante(clave string, def float64) float64 {
	if v := os.Getenv(clave); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}
