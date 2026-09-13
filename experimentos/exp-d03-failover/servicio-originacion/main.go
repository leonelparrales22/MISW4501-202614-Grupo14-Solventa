// Stub del servicio de originación para el experimento EXP-D03.
//
// Expone la interfaz de negocio (POST /oferta) que persiste una fila en
// PostgreSQL, la interfaz de control (GET /health) que consulta el monitor
// de salud del balanceador, y dos interfaces de apoyo exclusivas del
// ambiente de experimentación: la extracción de evidencia
// (GET /admin/ofertas) y la inyección de fallas (POST /admin/falla).
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

type configuracion struct {
	puerto          string
	urlBase         string
	instancia       string
	modoHealth      string
	tokenAdmin      string
	poolMaxLifetime time.Duration
	poolMaxIdleTime time.Duration
	poolMaxOpen     int
	poolMaxIdle     int
	cpuTrabajo      time.Duration
}

var (
	colgado   atomic.Bool
	listo     atomic.Bool
	erroresDB atomic.Int64
	ultimoLog atomic.Int64
	cfg       configuracion
	db        *sql.DB
)

func env(nombre, porDefecto string) string {
	if v := os.Getenv(nombre); v != "" {
		return v
	}
	return porDefecto
}

func envDuracion(nombre string, porDefecto time.Duration) time.Duration {
	v := os.Getenv(nombre)
	if v == "" {
		return porDefecto
	}
	if v == "0" {
		return 0
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		log.Fatalf("variable %s invalida: %v", nombre, err)
	}
	return d
}

func envEntero(nombre string, porDefecto int) int {
	v := os.Getenv(nombre)
	if v == "" {
		return porDefecto
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Fatalf("variable %s invalida: %v", nombre, err)
	}
	return n
}

// urlBaseDesdePartes arma la cadena de conexión cuando la contraseña llega
// como secreto independiente (Secrets Manager en ECS) en vez de una URL completa.
func urlBaseDesdePartes() string {
	if os.Getenv("DB_HOST") == "" {
		return ""
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s&connect_timeout=3",
		url.QueryEscape(env("DB_USER", "solventa")),
		url.QueryEscape(os.Getenv("DB_PASSWORD")),
		os.Getenv("DB_HOST"),
		env("DB_PORT", "5432"),
		env("DB_NAME", "originacion"),
		env("DB_SSLMODE", "require"))
}

// identificadorInstancia devuelve el id de la tarea cuando corre en ECS
// (metadatos v4) y el nombre de host en cualquier otro caso.
func identificadorInstancia() string {
	if v := os.Getenv("INSTANCE_ID"); v != "" {
		return v
	}
	host, _ := os.Hostname()
	uri := os.Getenv("ECS_CONTAINER_METADATA_URI_V4")
	if uri == "" {
		return host
	}
	cliente := &http.Client{Timeout: 2 * time.Second}
	resp, err := cliente.Get(uri + "/task")
	if err != nil {
		return host
	}
	defer resp.Body.Close()
	var meta struct {
		TaskARN string `json:"TaskARN"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil || meta.TaskARN == "" {
		return host
	}
	return meta.TaskARN[strings.LastIndex(meta.TaskARN, "/")+1:]
}

func leerConfiguracion() configuracion {
	urlBase := urlBaseDesdePartes()
	if urlBase == "" {
		urlBase = env("DATABASE_URL", "postgres://solventa:solventa-local@localhost:5432/originacion?sslmode=disable&connect_timeout=3")
	}
	return configuracion{
		puerto:          env("PORT", "8080"),
		urlBase:         urlBase,
		instancia:       identificadorInstancia(),
		modoHealth:      env("HEALTH_MODE", "superficial"),
		tokenAdmin:      env("ADMIN_TOKEN", "local-token"),
		poolMaxLifetime: envDuracion("POOL_MAX_LIFETIME", 0),
		poolMaxIdleTime: envDuracion("POOL_MAX_IDLE_TIME", 0),
		poolMaxOpen:     envEntero("POOL_MAX_OPEN", 10),
		poolMaxIdle:     envEntero("POOL_MAX_IDLE", 10),
		cpuTrabajo:      envDuracion("CPU_TRABAJO_MS", 0),
	}
}

// consumirCPU emula el costo de cómputo por solicitud del servicio real
// (perfilamiento y rating). Solo se usa en las ejecuciones de carga alta.
func consumirCPU(duracion time.Duration) {
	if duracion <= 0 {
		return
	}
	fin := time.Now().Add(duracion)
	x := uint64(88172645463325252)
	for time.Now().Before(fin) {
		for i := 0; i < 1000; i++ {
			x ^= x << 13
			x ^= x >> 7
			x ^= x << 17
		}
	}
}

func registrar(evento string, campos ...any) {
	linea := time.Now().UTC().Format(time.RFC3339Nano) + " instancia=" + cfg.instancia + " evento=" + evento
	for i := 0; i+1 < len(campos); i += 2 {
		linea += fmt.Sprintf(" %v=%v", campos[i], campos[i+1])
	}
	log.Print(linea)
}

// registrarErrorBase limita el registro de errores de persistencia a uno por
// segundo, porque durante un failover llegan cientos por segundo.
func registrarErrorBase(err error) {
	total := erroresDB.Add(1)
	ahora := time.Now().Unix()
	if ahora != ultimoLog.Swap(ahora) {
		registrar("error_base", "acumulados", total, "detalle", err.Error())
	}
}

func responderJSON(w http.ResponseWriter, codigo int, cuerpo any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Instancia", cfg.instancia)
	w.WriteHeader(codigo)
	_ = json.NewEncoder(w).Encode(cuerpo)
}

// middlewareColgar reproduce la falla silenciosa: el proceso sigue vivo y el
// kernel sigue aceptando conexiones TCP, pero ninguna petición recibe
// respuesta hasta que el cliente cierra la conexión.
func middlewareColgar(siguiente http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if colgado.Load() {
			<-r.Context().Done()
			return
		}
		siguiente.ServeHTTP(w, r)
	})
}

type solicitudOferta struct {
	ID        string `json:"id"`
	ClienteID string `json:"cliente_id"`
	RunID     string `json:"run_id"`
}

func calcularPrima(clienteID string) float64 {
	suma := 0
	for _, c := range clienteID {
		suma += int(c)
	}
	return float64(50 + suma%451)
}

func manejarOferta(w http.ResponseWriter, r *http.Request) {
	var sol solicitudOferta
	if err := json.NewDecoder(r.Body).Decode(&sol); err != nil || len(sol.ID) != 36 {
		responderJSON(w, http.StatusBadRequest, map[string]string{"error": "solicitud_invalida", "instancia": cfg.instancia})
		return
	}
	consumirCPU(cfg.cpuTrabajo)
	prima := calcularPrima(sol.ClienteID)
	ctx, cancelar := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancelar()
	_, err := db.ExecContext(ctx,
		`INSERT INTO ofertas (id, cliente_id, run_id, prima, instancia) VALUES ($1, $2, $3, $4, $5) ON CONFLICT (id) DO NOTHING`,
		sol.ID, sol.ClienteID, sol.RunID, prima, cfg.instancia)
	if err != nil {
		registrarErrorBase(err)
		responderJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "persistencia_no_disponible", "instancia": cfg.instancia})
		return
	}
	responderJSON(w, http.StatusCreated, map[string]any{"id": sol.ID, "instancia": cfg.instancia, "prima": prima})
}

func manejarHealth(w http.ResponseWriter, r *http.Request) {
	if !listo.Load() {
		responderJSON(w, http.StatusServiceUnavailable, map[string]string{"estado": "arrancando", "instancia": cfg.instancia})
		return
	}
	if cfg.modoHealth == "profundo" {
		ctx, cancelar := context.WithTimeout(r.Context(), time.Second)
		defer cancelar()
		if err := db.PingContext(ctx); err != nil {
			responderJSON(w, http.StatusServiceUnavailable, map[string]string{"estado": "base_no_disponible", "instancia": cfg.instancia})
			return
		}
	}
	responderJSON(w, http.StatusOK, map[string]string{"estado": "ok", "instancia": cfg.instancia, "modo": cfg.modoHealth})
}

func autorizado(r *http.Request) bool {
	return r.Header.Get("X-Admin-Token") == cfg.tokenAdmin
}

func manejarListarOfertas(w http.ResponseWriter, r *http.Request) {
	if !autorizado(r) {
		responderJSON(w, http.StatusUnauthorized, map[string]string{"error": "no_autorizado"})
		return
	}
	runID := r.URL.Query().Get("run_id")
	ctx, cancelar := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancelar()
	filas, err := db.QueryContext(ctx, `SELECT id::text FROM ofertas WHERE run_id = $1`, runID)
	if err != nil {
		responderJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	defer filas.Close()
	ids := make([]string, 0, 32768)
	for filas.Next() {
		var id string
		if err := filas.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	responderJSON(w, http.StatusOK, map[string]any{"run_id": runID, "total": len(ids), "ids": ids})
}

type solicitudFalla struct {
	Tipo string `json:"tipo"`
}

func manejarFalla(w http.ResponseWriter, r *http.Request) {
	if !autorizado(r) {
		responderJSON(w, http.StatusUnauthorized, map[string]string{"error": "no_autorizado"})
		return
	}
	var sol solicitudFalla
	if err := json.NewDecoder(r.Body).Decode(&sol); err != nil {
		responderJSON(w, http.StatusBadRequest, map[string]string{"error": "solicitud_invalida"})
		return
	}
	switch sol.Tipo {
	case "hang":
		registrar("falla_inyectada", "tipo", "hang")
		responderJSON(w, http.StatusAccepted, map[string]string{"tipo": "hang", "instancia": cfg.instancia})
		colgado.Store(true)
	case "crash":
		registrar("falla_inyectada", "tipo", "crash")
		responderJSON(w, http.StatusAccepted, map[string]string{"tipo": "crash", "instancia": cfg.instancia})
		go func() {
			time.Sleep(200 * time.Millisecond)
			os.Exit(1)
		}()
	default:
		responderJSON(w, http.StatusBadRequest, map[string]string{"error": "tipo_desconocido"})
	}
}

func abrirBase() error {
	var err error
	db, err = sql.Open("pgx", cfg.urlBase)
	if err != nil {
		return err
	}
	db.SetConnMaxLifetime(cfg.poolMaxLifetime)
	db.SetConnMaxIdleTime(cfg.poolMaxIdleTime)
	db.SetMaxOpenConns(cfg.poolMaxOpen)
	db.SetMaxIdleConns(cfg.poolMaxIdle)

	var ultimo error
	for intento := 1; intento <= 90; intento++ {
		ctx, cancelar := context.WithTimeout(context.Background(), 3*time.Second)
		ultimo = db.PingContext(ctx)
		cancelar()
		if ultimo == nil {
			break
		}
		if intento%10 == 1 {
			registrar("esperando_base", "intento", intento, "detalle", ultimo.Error())
		}
		time.Sleep(time.Second)
	}
	if ultimo != nil {
		return fmt.Errorf("la base no respondio: %w", ultimo)
	}
	ctx, cancelar := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelar()
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS ofertas (
		id uuid PRIMARY KEY,
		cliente_id text NOT NULL,
		run_id text NOT NULL,
		prima numeric NOT NULL,
		instancia text NOT NULL,
		creado_en timestamptz NOT NULL DEFAULT now()
	)`)
	if err != nil {
		return err
	}
	_, _ = db.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS ofertas_run_id ON ofertas (run_id)`)
	return nil
}

func main() {
	log.SetFlags(0)
	cfg = leerConfiguracion()
	registrar("arranque", "puerto", cfg.puerto, "health", cfg.modoHealth, "cpu_trabajo", cfg.cpuTrabajo,
		"pool_max_lifetime", cfg.poolMaxLifetime, "pool_max_idle_time", cfg.poolMaxIdleTime,
		"pool_max_open", cfg.poolMaxOpen, "pool_max_idle", cfg.poolMaxIdle)

	if err := abrirBase(); err != nil {
		registrar("arranque_fallido", "detalle", err.Error())
		os.Exit(1)
	}
	listo.Store(true)
	registrar("base_conectada")

	mux := http.NewServeMux()
	mux.HandleFunc("POST /oferta", manejarOferta)
	mux.HandleFunc("GET /health", manejarHealth)
	mux.HandleFunc("GET /admin/ofertas", manejarListarOfertas)
	mux.HandleFunc("POST /admin/falla", manejarFalla)

	servidor := &http.Server{
		Addr:              ":" + cfg.puerto,
		Handler:           middlewareColgar(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := servidor.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			registrar("servidor_fallido", "detalle", err.Error())
			os.Exit(1)
		}
	}()
	registrar("listo")

	ctx, detener := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer detener()
	<-ctx.Done()

	registrar("apagado_iniciado")
	ctxApagado, cancelar := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancelar()
	_ = servidor.Shutdown(ctxApagado)
	_ = db.Close()
	registrar("apagado_completado")
}
