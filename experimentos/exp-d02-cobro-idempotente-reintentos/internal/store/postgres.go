// Package store persiste Aceptacion e IntentoCobro en PostgreSQL.
//
// A propósito, las operaciones de escritura de este paquete NO usan
// SELECT ... FOR UPDATE ni ningún lock explícito alrededor del ciclo
// leer-intento -> llamar-pasarela -> escribir-resultado (ver
// internal/cobro.Servicio.ProcesarCobro). Ese es el diseño "ingenuo" que
// el experimento EC-D02 debe ejecutar primero: la hipótesis bajo prueba
// es si el UNIQUE de idempotency_key basta por sí solo, sin más
// coordinación, incluso ante dos workers concurrentes. Si la prueba de
// concurrencia (scripts/concurrencia) revela que no basta, el lock se
// agrega después como corrección documentada, no antes.
package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"solventa/exp-d02-cobro-idempotente-reintentos/internal/domain"
)

// ErrNoEncontrado se devuelve cuando no existe la fila solicitada.
var ErrNoEncontrado = errors.New("no encontrado")

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store {
	return &Store{db: db}
}

func (s *Store) CrearAceptacion(ctx context.Context, a domain.Aceptacion) error {
	creadaEn := a.CreadaEn
	if creadaEn.IsZero() {
		creadaEn = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO aceptacion (id, cliente_id, monto_centavos, moneda, creada_en) VALUES ($1, $2, $3, $4, $5)`,
		a.ID, a.ClienteID, a.MontoCentavos, a.Moneda, creadaEn,
	)
	return err
}

// CrearIntentoPendiente inserta el intento de cobro en estado "pendiente"
// para una aceptación. Usa INSERT ... ON CONFLICT (idempotency_key) DO
// NOTHING y luego siempre relee la fila resultante (exista ya o se acabe
// de crear), de modo que llamarla dos veces -incluso desde goroutines
// concurrentes- nunca produce dos filas para la misma aceptación: es el
// propio motor de PostgreSQL, no la lógica de Go, quien decide cuál
// escritura "gana".
func (s *Store) CrearIntentoPendiente(ctx context.Context, aceptacionID, idempotencyKey string) (domain.IntentoCobro, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO intento_cobro (aceptacion_id, idempotency_key)
		 VALUES ($1, $2)
		 ON CONFLICT (idempotency_key) DO NOTHING`,
		aceptacionID, idempotencyKey,
	)
	if err != nil {
		return domain.IntentoCobro{}, err
	}
	return s.obtenerPorIdempotencyKey(ctx, idempotencyKey)
}

func (s *Store) ObtenerIntentoPorAceptacion(ctx context.Context, aceptacionID string) (domain.IntentoCobro, bool, error) {
	intento, err := s.escanearUno(ctx,
		`SELECT id, aceptacion_id, idempotency_key, estado, intentos, proximo_intento_en,
		        creado_en, actualizado_en, COALESCE(ultimo_error, '')
		 FROM intento_cobro WHERE aceptacion_id = $1`,
		aceptacionID,
	)
	if errors.Is(err, ErrNoEncontrado) {
		return domain.IntentoCobro{}, false, nil
	}
	if err != nil {
		return domain.IntentoCobro{}, false, err
	}
	return intento, true, nil
}

func (s *Store) obtenerPorIdempotencyKey(ctx context.Context, idempotencyKey string) (domain.IntentoCobro, error) {
	return s.escanearUno(ctx,
		`SELECT id, aceptacion_id, idempotency_key, estado, intentos, proximo_intento_en,
		        creado_en, actualizado_en, COALESCE(ultimo_error, '')
		 FROM intento_cobro WHERE idempotency_key = $1`,
		idempotencyKey,
	)
}

func (s *Store) escanearUno(ctx context.Context, query string, args ...any) (domain.IntentoCobro, error) {
	row := s.db.QueryRowContext(ctx, query, args...)
	var it domain.IntentoCobro
	var estado string
	err := row.Scan(&it.ID, &it.AceptacionID, &it.IdempotencyKey, &estado, &it.Intentos,
		&it.ProximoIntentoEn, &it.CreadoEn, &it.ActualizadoEn, &it.UltimoError)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.IntentoCobro{}, ErrNoEncontrado
	}
	if err != nil {
		return domain.IntentoCobro{}, err
	}
	it.Estado = domain.EstadoIntento(estado)
	return it, nil
}

// ReclamarPendiente es el "claim" atómico introducido por la corrección
// del ADR-01: transiciona el intento de "pendiente" a "en_proceso" con un
// UPDATE condicional (compare-and-swap) de una sola sentencia. Postgres
// serializa las escrituras concurrentes sobre la misma fila, así que si
// dos llamadores (dos workers, o el disparo síncrono del journey y el
// worker de reintentos) intentan reclamar el mismo intento al mismo
// tiempo, solo uno obtiene reclamado=true; el otro debe retirarse SIN
// llamar la pasarela. Se prefirió este UPDATE condicional sobre un
// `SELECT ... FOR UPDATE` mantenido durante toda la llamada a la
// pasarela para no dejar la fila bloqueada mientras se espera una
// respuesta de red potencialmente lenta.
func (s *Store) ReclamarPendiente(ctx context.Context, aceptacionID string) (domain.IntentoCobro, bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE intento_cobro SET estado = $1, actualizado_en = now()
		 WHERE aceptacion_id = $2 AND estado = $3`,
		domain.EstadoEnProceso, aceptacionID, domain.EstadoPendiente,
	)
	if err != nil {
		return domain.IntentoCobro{}, false, err
	}
	filas, err := res.RowsAffected()
	if err != nil {
		return domain.IntentoCobro{}, false, err
	}
	if filas == 0 {
		// No se ganó el claim: o ya estaba en_proceso (otro llamador
		// concurrente lo ganó primero), o ya estaba confirmado/fallido.
		intento, _, err := s.ObtenerIntentoPorAceptacion(ctx, aceptacionID)
		return intento, false, err
	}
	intento, _, err := s.ObtenerIntentoPorAceptacion(ctx, aceptacionID)
	return intento, true, err
}

// MarcarConfirmado marca el intento como pagado con éxito. Es un UPDATE
// simple por id, sin lock previo (ver comentario del paquete).
func (s *Store) MarcarConfirmado(ctx context.Context, intentoID string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE intento_cobro SET estado = $1, actualizado_en = now() WHERE id = $2`,
		domain.EstadoConfirmado, intentoID,
	)
	return err
}

// MarcarFallidoReintentar registra un fallo ambiguo/transitorio: suma un
// intento, programa el próximo para proximoIntentoEn, y devuelve el
// estado a "pendiente" (estaba "en_proceso" desde ReclamarPendiente) para
// que un futuro llamador pueda volver a reclamarlo.
func (s *Store) MarcarFallidoReintentar(ctx context.Context, intentoID string, proximoIntentoEn time.Time, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE intento_cobro
		 SET estado = $1, intentos = intentos + 1, proximo_intento_en = $2, ultimo_error = $3, actualizado_en = now()
		 WHERE id = $4`,
		domain.EstadoPendiente, proximoIntentoEn, errMsg, intentoID,
	)
	return err
}

// MarcarFallidoDefinitivo marca el intento como fallido sin más
// reintentos: rechazo explícito de la pasarela o vencimiento de la
// ventana de 24h.
func (s *Store) MarcarFallidoDefinitivo(ctx context.Context, intentoID string, errMsg string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE intento_cobro
		 SET estado = $1, ultimo_error = $2, actualizado_en = now()
		 WHERE id = $3`,
		domain.EstadoFallido, errMsg, intentoID,
	)
	return err
}

// IntentosPendientesParaReintentar trae los intentos "pendiente" cuyo
// proximo_intento_en ya venció, para que el worker de reintentos los
// reprocese.
func (s *Store) IntentosPendientesParaReintentar(ctx context.Context, ahora time.Time) ([]domain.IntentoCobro, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, aceptacion_id, idempotency_key, estado, intentos, proximo_intento_en,
		        creado_en, actualizado_en, COALESCE(ultimo_error, '')
		 FROM intento_cobro
		 WHERE estado = $1 AND proximo_intento_en <= $2
		 ORDER BY proximo_intento_en ASC`,
		domain.EstadoPendiente, ahora,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var resultado []domain.IntentoCobro
	for rows.Next() {
		var it domain.IntentoCobro
		var estado string
		if err := rows.Scan(&it.ID, &it.AceptacionID, &it.IdempotencyKey, &estado, &it.Intentos,
			&it.ProximoIntentoEn, &it.CreadoEn, &it.ActualizadoEn, &it.UltimoError); err != nil {
			return nil, err
		}
		it.Estado = domain.EstadoIntento(estado)
		resultado = append(resultado, it)
	}
	return resultado, rows.Err()
}

// EsViolacionDeUnicidad identifica el error de PostgreSQL 23505
// (unique_violation), útil para las pruebas que insertan directamente
// duplicando idempotency_key sin pasar por CrearIntentoPendiente.
func EsViolacionDeUnicidad(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return false
}
