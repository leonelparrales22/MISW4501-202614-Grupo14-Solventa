package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const columnas = `id, poliza_id, prioridad, estado_actual, actualizado_en`

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, mensaje string) {
	writeJSON(w, status, map[string]string{"error": mensaje})
}

// escribirRespuesta manda el cuerpo y además expone la frescura en cabeceras,
// útil para monitoreo y para clientes que no quieran mirar el cuerpo.
func escribirRespuesta(w http.ResponseWriter, status int, origen string, data any) {
	w.Header().Set("X-Origen-Datos", origen)
	w.Header().Set("X-Stale", fmt.Sprintf("%t", origen == origenRedis))
	writeJSON(w, status, data)
}

// simularLatencia aplica la demora artificial configurada en
// SIMULATED_DB_LATENCY_MS. Respeta el contexto, así que si la demora supera
// el timeout de lectura la consulta se cancela igual que lo haría un Postgres
// realmente lento. Solo se aplica a lecturas.
func simularLatencia(ctx context.Context) error {
	if cfg.LatenciaSimulada <= 0 {
		return nil
	}
	select {
	case <-time.After(cfg.LatenciaSimulada):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// listarSiniestros - GET /siniestros
// Soporta filtros opcionales por query params: ?poliza_id=<uuid>&estado=<estado>
// Si Postgres no responde dentro de DB_QUERY_TIMEOUT_MS (900ms por defecto),
// la respuesta se arma desde Redis y se marca con stale=true.
func listarSiniestros(w http.ResponseWriter, r *http.Request) {
	polizaID := r.URL.Query().Get("poliza_id")
	estado := r.URL.Query().Get("estado")

	ctx, cancel := context.WithTimeout(r.Context(), cfg.DBTimeout)
	defer cancel()

	siniestros, err := consultarSiniestrosDB(ctx, polizaID, estado)
	if err == nil {
		guardarVariosEnCache(r.Context(), siniestros)
		escribirRespuesta(w, http.StatusOK, origenPostgres, nuevasRespuestas(siniestros, origenPostgres))
		return
	}

	if r.Context().Err() != nil {
		// El cliente se fue; no hay a quién responderle.
		return
	}
	log.Printf("lectura degradada en GET /siniestros: %v", err)

	cacheados, ok := obtenerTodosDeCache(r.Context())
	if !ok {
		writeError(w, http.StatusServiceUnavailable,
			"postgres no respondió a tiempo y la caché no está disponible")
		return
	}

	escribirRespuesta(w, http.StatusOK, origenRedis,
		nuevasRespuestas(filtrarYOrdenar(cacheados, polizaID, estado), origenRedis))
}

// obtenerSiniestro - GET /siniestros/{id}
// Mismo esquema de degradación que el listado.
func obtenerSiniestro(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "id inválido, debe ser un uuid")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), cfg.DBTimeout)
	defer cancel()

	s, err := consultarSiniestroDB(ctx, id)
	switch {
	case err == nil:
		guardarEnCache(r.Context(), s)
		escribirRespuesta(w, http.StatusOK, origenPostgres, nuevaRespuesta(s, origenPostgres))
		return
	case errors.Is(err, sql.ErrNoRows):
		// Postgres sí respondió: el siniestro no existe.
		writeError(w, http.StatusNotFound, "siniestro no encontrado")
		return
	}

	if r.Context().Err() != nil {
		return
	}
	log.Printf("lectura degradada en GET /siniestros/%s: %v", id, err)

	cacheado, encontrado := obtenerDeCache(r.Context(), id)
	if !encontrado {
		writeError(w, http.StatusServiceUnavailable,
			"postgres no respondió a tiempo y el siniestro no está en caché")
		return
	}

	escribirRespuesta(w, http.StatusOK, origenRedis, nuevaRespuesta(cacheado, origenRedis))
}

// registrarSiniestro - POST /siniestros
func registrarSiniestro(w http.ResponseWriter, r *http.Request) {
	var input RegistrarSiniestroInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "cuerpo de la petición inválido")
		return
	}

	if input.PolizaID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "poliza_id es requerido")
		return
	}
	if strings.TrimSpace(input.EstadoActual) == "" {
		writeError(w, http.StatusBadRequest, "estado_actual es requerido")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), cfg.DBWriteTimeout)
	defer cancel()

	var nuevo Siniestro
	query := `INSERT INTO siniestros (id, poliza_id, prioridad, estado_actual, actualizado_en)
	          VALUES ($1, $2, $3, $4, NOW())
	          RETURNING ` + columnas
	err := db.QueryRowContext(ctx, query, uuid.New(), input.PolizaID, input.Prioridad, input.EstadoActual).
		Scan(&nuevo.ID, &nuevo.PolizaID, &nuevo.Prioridad, &nuevo.EstadoActual, &nuevo.ActualizadoEn)
	if err != nil {
		log.Printf("error registrando siniestro: %v", err)
		writeError(w, http.StatusInternalServerError, "error registrando el siniestro")
		return
	}

	// Escritura en caché apenas se crea: así la caché nunca está fría.
	guardarEnCache(r.Context(), nuevo)

	escribirRespuesta(w, http.StatusCreated, origenPostgres, nuevaRespuesta(nuevo, origenPostgres))
}

// actualizarEstadoSiniestro - PATCH /siniestros/{id}
// Cambia el estado del siniestro y refresca la caché con el nuevo valor.
func actualizarEstadoSiniestro(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "id inválido, debe ser un uuid")
		return
	}

	var input ActualizarEstadoInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "cuerpo de la petición inválido")
		return
	}
	if strings.TrimSpace(input.EstadoActual) == "" {
		writeError(w, http.StatusBadRequest, "estado_actual es requerido")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), cfg.DBWriteTimeout)
	defer cancel()

	var actualizado Siniestro
	query := `UPDATE siniestros
	          SET estado_actual = $1, actualizado_en = NOW()
	          WHERE id = $2
	          RETURNING ` + columnas
	err = db.QueryRowContext(ctx, query, input.EstadoActual, id).
		Scan(&actualizado.ID, &actualizado.PolizaID, &actualizado.Prioridad,
			&actualizado.EstadoActual, &actualizado.ActualizadoEn)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "siniestro no encontrado")
		return
	}
	if err != nil {
		log.Printf("error actualizando el estado del siniestro %s: %v", id, err)
		writeError(w, http.StatusInternalServerError, "error actualizando el estado del siniestro")
		return
	}

	// Escritura en caché en cada cambio de estado: si Postgres se degrada
	// justo después, la respuesta stale ya refleja el estado nuevo.
	guardarEnCache(r.Context(), actualizado)

	escribirRespuesta(w, http.StatusOK, origenPostgres, nuevaRespuesta(actualizado, origenPostgres))
}

// health - GET /health
// Reporta el estado de las dependencias y la configuración de degradación.
// Sirve para verificar en la demo si la latencia simulada está activa.
func health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	postgresOK := db.PingContext(ctx) == nil
	redisOK := cacheDisponible(ctx)

	status := http.StatusOK
	if !postgresOK && !redisOK {
		status = http.StatusServiceUnavailable
	}

	writeJSON(w, status, map[string]any{
		"postgres":                postgresOK,
		"redis":                   redisOK,
		"db_query_timeout_ms":     cfg.DBTimeout.Milliseconds(),
		"simulated_db_latency_ms": cfg.LatenciaSimulada.Milliseconds(),
		"cache_ttl_seconds":       int(cfg.CacheTTL.Seconds()),
	})
}

// --- Acceso a datos ---

func consultarSiniestrosDB(ctx context.Context, polizaID, estado string) ([]Siniestro, error) {
	if err := simularLatencia(ctx); err != nil {
		return nil, err
	}

	query := `SELECT ` + columnas + ` FROM siniestros WHERE 1=1`
	args := []any{}
	argPos := 1

	if polizaID != "" {
		query += fmt.Sprintf(" AND poliza_id = $%d", argPos)
		args = append(args, polizaID)
		argPos++
	}
	if estado != "" {
		query += fmt.Sprintf(" AND estado_actual = $%d", argPos)
		args = append(args, estado)
		argPos++
	}
	query += " ORDER BY prioridad DESC"

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	siniestros := []Siniestro{}
	for rows.Next() {
		var s Siniestro
		if err := rows.Scan(&s.ID, &s.PolizaID, &s.Prioridad, &s.EstadoActual, &s.ActualizadoEn); err != nil {
			return nil, err
		}
		siniestros = append(siniestros, s)
	}
	return siniestros, rows.Err()
}

func consultarSiniestroDB(ctx context.Context, id uuid.UUID) (Siniestro, error) {
	if err := simularLatencia(ctx); err != nil {
		return Siniestro{}, err
	}

	var s Siniestro
	query := `SELECT ` + columnas + ` FROM siniestros WHERE id = $1`
	err := db.QueryRowContext(ctx, query, id).
		Scan(&s.ID, &s.PolizaID, &s.Prioridad, &s.EstadoActual, &s.ActualizadoEn)
	return s, err
}

// filtrarYOrdenar reproduce sobre los datos de la caché los mismos filtros
// y el mismo orden que aplica la consulta a Postgres.
func filtrarYOrdenar(siniestros []Siniestro, polizaID, estado string) []Siniestro {
	filtrados := make([]Siniestro, 0, len(siniestros))
	for _, s := range siniestros {
		if polizaID != "" && !strings.EqualFold(s.PolizaID.String(), polizaID) {
			continue
		}
		if estado != "" && s.EstadoActual != estado {
			continue
		}
		filtrados = append(filtrados, s)
	}

	sort.SliceStable(filtrados, func(i, j int) bool {
		return filtrados[i].Prioridad > filtrados[j].Prioridad
	})
	return filtrados
}
