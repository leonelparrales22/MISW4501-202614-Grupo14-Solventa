// Package orquestador es el que recibe la cotización y llama a perfilamiento
// y a rating. Todo en el mismo proceso, sin red de por medio (los «use» del
// diagrama de componentes).
package orquestador

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/experimentos/exp-l01/internal/adaptadores"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/experimentos/exp-l01/internal/cache"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/experimentos/exp-l01/internal/perfilamiento"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/experimentos/exp-l01/internal/rating"
)

// Config son los parámetros del experimento. Todo viene por variables de
// entorno, pues así cada combinación de la matriz es solo cambiar valores y
// no tocar código.
type Config struct {
	Fuentes         int           // cuántas fuentes consultar en paralelo (esto es lo que variamos)
	FuentesURL      string        // dónde están las fuentes simuladas, p. ej. http://fuentes:8081
	CorteDep        time.Duration // límite por fuente (120 ms según el enunciado)
	Deadline        time.Duration // tiempo máximo para todas juntas (700 ms según el enunciado)
	RedisAddr       string        // si va vacío se corre sin caché
	RedisTTL        time.Duration
	MaxConexPorHost int
}

// ConfigDesdeEnv lee las variables de entorno. Si falta alguna usa lo que dice la ficha.
func ConfigDesdeEnv() Config {
	return Config{
		Fuentes:         entero("FUENTES", 3),
		FuentesURL:      texto("FUENTES_URL", "http://localhost:8081"),
		CorteDep:        milis("CORTE_DEP_MS", 120),
		Deadline:        milis("DEADLINE_MS", 700),
		RedisAddr:       texto("REDIS_ADDR", ""),
		RedisTTL:        time.Duration(entero("REDIS_TTL_S", 3600)) * time.Second,
		MaxConexPorHost: entero("MAX_CONEX_HOST", 200),
	}
}

// Servidor es el orquestador HTTP.
type Servidor struct {
	mux      *http.ServeMux
	cfg      Config
	perf     *perfilamiento.Servicio
	motor    rating.Motor
	contador *adaptadores.ContadorConexiones
	redis    *cache.Redis // nil si la caché es nula
	inicio   time.Time
}

// Nuevo arma todo: las N fuentes sobre un transporte con contador, la caché
// (Redis o ninguna), perfilamiento y rating.
func Nuevo(cfg Config) *Servidor {
	contador := &adaptadores.ContadorConexiones{}
	cliente := &http.Client{Transport: adaptadores.NuevoTransporte(contador, cfg.MaxConexPorHost)}

	fuentes := make([]adaptadores.FuenteDatos, 0, cfg.Fuentes)
	for i := 1; i <= cfg.Fuentes; i++ {
		nombre := fmt.Sprintf("fuente-%d", i)
		url := fmt.Sprintf("%s/fuente/%d", cfg.FuentesURL, i)
		fuentes = append(fuentes, adaptadores.NuevaFuenteHTTP(nombre, url, cfg.CorteDep, cliente))
	}

	var c cache.Cache = cache.Nula{}
	var r *cache.Redis
	if cfg.RedisAddr != "" {
		r = cache.NuevaRedis(cfg.RedisAddr, cfg.RedisTTL)
		c = r
	}

	s := &Servidor{
		mux:      http.NewServeMux(),
		cfg:      cfg,
		perf:     perfilamiento.Nuevo(fuentes, c, cfg.Deadline),
		contador: contador,
		redis:    r,
		inicio:   time.Now(),
	}
	s.mux.HandleFunc("POST /cotizaciones", s.cotizar)
	s.mux.HandleFunc("GET /debug/estado", s.estado)
	s.mux.HandleFunc("GET /health", s.health)
	log.Printf("orquestador: fuentes=%d corte_dep=%s deadline=%s redis=%q max_conex_host=%d",
		cfg.Fuentes, cfg.CorteDep, cfg.Deadline, cfg.RedisAddr, cfg.MaxConexPorHost)
	return s
}

func (s *Servidor) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

type solicitudCotizacion struct {
	ClienteID string `json:"cliente_id"`
}

type respuestaCotizacion struct {
	ClienteID     string            `json:"cliente_id"`
	Prima         float64           `json:"prima"`
	Coberturas    []string          `json:"coberturas"`
	PerfilParcial bool              `json:"perfil_parcial"`
	DesdeCache    bool              `json:"desde_cache"`
	Faltantes     []string          `json:"faltantes,omitempty"`
	Tramos        map[string]string `json:"tramos_ms"`
}

// cotizar es el camino crítico: perfilar, tarifar y responder. Va con la
// cabecera Server-Timing para saber en qué etapa se fue el tiempo.
func (s *Servidor) cotizar(w http.ResponseWriter, r *http.Request) {
	var sol solicitudCotizacion
	if err := json.NewDecoder(r.Body).Decode(&sol); err != nil || sol.ClienteID == "" {
		http.Error(w, `{"error":"cliente_id requerido"}`, http.StatusBadRequest)
		return
	}

	perfil, tramos, err := s.perf.Perfilar(r.Context(), sol.ClienteID)
	if err != nil {
		w.Header().Set("Server-Timing", tramos.ServerTiming())
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusServiceUnavailable)
		return
	}

	t := time.Now()
	cot := s.motor.Calcular(perfil)
	tramos.Rating = time.Since(t)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Server-Timing", tramos.ServerTiming())
	_ = json.NewEncoder(w).Encode(respuestaCotizacion{
		ClienteID:     perfil.ClienteID,
		Prima:         cot.Prima,
		Coberturas:    cot.Coberturas,
		PerfilParcial: perfil.Parcial,
		DesdeCache:    perfil.DesdeCache,
		Faltantes:     perfil.Faltantes,
		Tramos: map[string]string{
			"cache":  ms(tramos.Cache),
			"fanout": ms(tramos.Fanout),
			"agg":    ms(tramos.Agg),
			"rating": ms(tramos.Rating),
		},
	})
}

// estado muestra lo que necesitamos para la hipótesis (b): cuántas goroutines,
// conexiones y consultas hay vivas. k6 lo lee al arrancar y al terminar cada ejecución.
func (s *Servidor) estado(w http.ResponseWriter, _ *http.Request) {
	cancel, parc, tot, vuelo := s.perf.Estadisticas()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"goroutines":              runtime.NumGoroutine(),
		"consultas_en_vuelo":      vuelo,
		"conexiones_abiertas":     s.contador.Abiertas(),
		"conexiones_totales":      s.contador.Total(),
		"cancelaciones_efectivas": cancel,
		"perfiles_parciales":      parc,
		"perfiles_calculados":     tot,
		"fuentes":                 s.cfg.Fuentes,
		"corte_dep_ms":            s.cfg.CorteDep.Milliseconds(),
		"deadline_ms":             s.cfg.Deadline.Milliseconds(),
		"uptime_s":                int(time.Since(s.inicio).Seconds()),
	})
}

func (s *Servidor) health(w http.ResponseWriter, r *http.Request) {
	if s.redis != nil {
		ctx, cancelar := context.WithTimeout(r.Context(), 100*time.Millisecond)
		defer cancelar()
		if err := s.redis.Ping(ctx); err != nil {
			http.Error(w, "redis: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func ms(d time.Duration) string {
	return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 2, 64)
}

// --- lectura de entorno ---

func texto(clave, def string) string {
	if v := os.Getenv(clave); v != "" {
		return v
	}
	return def
}

func entero(clave string, def int) int {
	if v := os.Getenv(clave); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func milis(clave string, def int) time.Duration {
	return time.Duration(entero(clave, def)) * time.Millisecond
}
