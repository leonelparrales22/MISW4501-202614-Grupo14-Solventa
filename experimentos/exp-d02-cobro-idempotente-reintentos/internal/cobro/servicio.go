// Package cobro orquesta el journey de cobro: crear la aceptación,
// procesar el intento de cobro contra la pasarela, y decidir qué hacer
// según el resultado (confirmar, reintentar, o fallar definitivamente).
package cobro

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"solventa/exp-d02-cobro-idempotente-reintentos/internal/domain"
	"solventa/exp-d02-cobro-idempotente-reintentos/internal/pasarela"
)

// PasarelaClient es la interfaz mínima que Servicio necesita del cliente
// de pasarela (permite reemplazarlo por un fake en servicio_test.go).
type PasarelaClient interface {
	Cobrar(ctx context.Context, idempotencyKey, aceptacionID string, montoCentavos int64) (pasarela.Resultado, error)
}

// Store es la interfaz mínima que Servicio necesita del store (permite
// reemplazarlo por un fake en servicio_test.go).
type Store interface {
	CrearAceptacion(ctx context.Context, a domain.Aceptacion) error
	CrearIntentoPendiente(ctx context.Context, aceptacionID, idempotencyKey string) (domain.IntentoCobro, error)
	// ReclamarPendiente es el claim atómico (ADR-01): ver
	// internal/store.Store.ReclamarPendiente para el detalle. Devuelve
	// reclamado=false sin error si el intento ya no estaba "pendiente"
	// (otro llamador lo ganó, o ya está confirmado/fallido).
	ReclamarPendiente(ctx context.Context, aceptacionID string) (intento domain.IntentoCobro, reclamado bool, err error)
	MarcarConfirmado(ctx context.Context, intentoID string) error
	MarcarFallidoReintentar(ctx context.Context, intentoID string, proximoIntentoEn time.Time, errMsg string) error
	MarcarFallidoDefinitivo(ctx context.Context, intentoID string, errMsg string) error
}

type Config struct {
	// BackoffBase y BackoffMax acotan el crecimiento exponencial del
	// intervalo entre reintentos.
	BackoffBase time.Duration
	BackoffMax  time.Duration
	// Ventana es el plazo máximo (24h en producción, acortable en
	// pruebas) desde la creación del intento antes de marcarlo fallido
	// definitivo por vencimiento.
	Ventana time.Duration
}

type Servicio struct {
	store    Store
	pasarela PasarelaClient
	cfg      Config
	ahora    func() time.Time // inyectable para pruebas
}

func New(store Store, p PasarelaClient, cfg Config) *Servicio {
	return &Servicio{store: store, pasarela: p, cfg: cfg, ahora: time.Now}
}

// CrearAceptacion persiste la aceptación y su intento de cobro pendiente
// ANTES de intentar cobrar. El journey nunca pierde una aceptación aunque
// la pasarela esté caída: eso se garantiza aquí, no en ProcesarCobro.
func (s *Servicio) CrearAceptacion(ctx context.Context, clienteID string, montoCentavos int64) (domain.Aceptacion, domain.IntentoCobro, error) {
	a := domain.Aceptacion{
		ID:            uuid.NewString(),
		ClienteID:     clienteID,
		MontoCentavos: montoCentavos,
		Moneda:        "COP",
		CreadaEn:      s.ahora(),
	}
	if err := s.store.CrearAceptacion(ctx, a); err != nil {
		return domain.Aceptacion{}, domain.IntentoCobro{}, fmt.Errorf("creando aceptacion: %w", err)
	}

	key := domain.IdempotencyKeyPara(a.ID)
	intento, err := s.store.CrearIntentoPendiente(ctx, a.ID, key)
	if err != nil {
		return domain.Aceptacion{}, domain.IntentoCobro{}, fmt.Errorf("creando intento de cobro: %w", err)
	}
	return a, intento, nil
}

// ProcesarCobro es el corazón del experimento: lo invoca tanto
// POST /procesar-cobro/{id} (bajo la prueba de concurrencia dirigida)
// como el worker de reintentos en background.
//
// Diseño ORIGINAL bajo prueba (sin ReclamarPendiente): leer el intento →
// si no está confirmado, llamar la pasarela → escribir el resultado, sin
// ningún paso que reclamara el intento antes de la llamada de red. La
// evidencia real (resultados/raw/diseno-ingenuo-evidencia-duplicado-real.txt)
// mostró que ese hueco SÍ permite una llamada real duplicada a la
// pasarela bajo una condición de carrera genuina entre el disparo
// síncrono del journey y el worker de reintentos en background — no hizo
// falta ni siquiera el escenario de concurrencia deliberado para
// observarlo. Eso confirma la fila 3 de la tabla de interpretación del
// diseño del experimento: el UNIQUE de idempotency_key no basta solo.
//
// Diseño CORREGIDO (ADR-01, el que implementa esta versión): el primer
// paso es ReclamarPendiente, un UPDATE condicional atómico que solo un
// llamador concurrente puede ganar. Quien no gana el claim se retira sin
// llamar la pasarela — la garantía ya no depende de que la ventana entre
// leer y escribir sea corta, sino de que Postgres serializa la propia
// transición de estado.
func (s *Servicio) ProcesarCobro(ctx context.Context, aceptacionID string) (domain.IntentoCobro, error) {
	intento, reclamado, err := s.store.ReclamarPendiente(ctx, aceptacionID)
	if err != nil {
		return domain.IntentoCobro{}, fmt.Errorf("reclamando intento: %w", err)
	}
	if intento.ID == "" {
		return domain.IntentoCobro{}, fmt.Errorf("no existe intento de cobro para la aceptacion %s", aceptacionID)
	}
	if !reclamado {
		// Ya lo reclamó otro llamador concurrente (u otro ya lo dejó
		// confirmado/fallido): no se llama la pasarela.
		return intento, nil
	}

	resultado, errCobro := s.pasarela.Cobrar(ctx, intento.IdempotencyKey, aceptacionID, 0)
	if errCobro == nil {
		if err := s.store.MarcarConfirmado(ctx, intento.ID); err != nil {
			return domain.IntentoCobro{}, fmt.Errorf("marcando confirmado: %w", err)
		}
		intento.Estado = domain.EstadoConfirmado
		_ = resultado
		return intento, nil
	}

	if errors.Is(errCobro, pasarela.ErrRechazado) {
		if err := s.store.MarcarFallidoDefinitivo(ctx, intento.ID, errCobro.Error()); err != nil {
			return domain.IntentoCobro{}, fmt.Errorf("marcando fallido: %w", err)
		}
		intento.Estado = domain.EstadoFallido
		intento.UltimoError = errCobro.Error()
		return intento, nil
	}

	// errCobro es ErrAmbiguo (o cualquier otro error de transporte): se
	// trata como reintentable, salvo que la ventana de 24h ya haya
	// vencido.
	if s.ahora().Sub(intento.CreadoEn) >= s.cfg.Ventana {
		if err := s.store.MarcarFallidoDefinitivo(ctx, intento.ID, "ventana de reintentos vencida: "+errCobro.Error()); err != nil {
			return domain.IntentoCobro{}, fmt.Errorf("marcando fallido por ventana vencida: %w", err)
		}
		intento.Estado = domain.EstadoFallido
		return intento, nil
	}

	proximoIntento := s.ahora().Add(ComputeBackoff(intento.Intentos+1, s.cfg.BackoffBase, s.cfg.BackoffMax))
	if err := s.store.MarcarFallidoReintentar(ctx, intento.ID, proximoIntento, errCobro.Error()); err != nil {
		return domain.IntentoCobro{}, fmt.Errorf("programando reintento: %w", err)
	}
	// MarcarFallidoReintentar devuelve el intento a "pendiente" en la BD
	// (estaba "en_proceso" desde el claim) para que pueda reclamarse de
	// nuevo más adelante; se refleja aquí también en el valor devuelto.
	intento.Estado = domain.EstadoPendiente
	intento.Intentos++
	intento.ProximoIntentoEn = proximoIntento
	intento.UltimoError = errCobro.Error()
	return intento, nil
}

// ComputeBackoff calcula un intervalo exponencial acotado entre
// [base*2^(n-1)] y backoffMax, para el n-ésimo intento (n >= 1).
func ComputeBackoff(intentoNumero int, base, max time.Duration) time.Duration {
	if intentoNumero < 1 {
		intentoNumero = 1
	}
	d := base
	for i := 1; i < intentoNumero; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	if d > max {
		return max
	}
	return d
}
