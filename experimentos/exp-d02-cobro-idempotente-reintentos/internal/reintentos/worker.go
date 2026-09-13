// Package reintentos implementa el worker en background que reprocesa
// los intentos de cobro vencidos con backoff exponencial. El cálculo del
// backoff y la decisión de reintentar/confirmar/fallar viven en
// internal/cobro.Servicio.ProcesarCobro; este worker solo hace polling y
// dispara ese mismo procesamiento — el mismo camino de código que usa el
// endpoint POST /procesar-cobro/{id} bajo la prueba de concurrencia.
package reintentos

import (
	"context"
	"log"
	"time"

	"solventa/exp-d02-cobro-idempotente-reintentos/internal/domain"
)

// Store es la interfaz mínima que el worker necesita del store.
type Store interface {
	IntentosPendientesParaReintentar(ctx context.Context, ahora time.Time) ([]domain.IntentoCobro, error)
}

// Procesador reprocesa un intento de cobro dado su aceptacion_id.
// internal/cobro.Servicio.ProcesarCobro satisface esta firma.
type Procesador func(ctx context.Context, aceptacionID string) (domain.IntentoCobro, error)

type Worker struct {
	store     Store
	procesar  Procesador
	poll      time.Duration
	ahoraFunc func() time.Time
}

func New(store Store, procesar Procesador, poll time.Duration) *Worker {
	return &Worker{store: store, procesar: procesar, poll: poll, ahoraFunc: time.Now}
}

// Run bloquea hasta que ctx se cancele: cada `poll`, trae los intentos
// pendientes vencidos y los reprocesa uno por uno.
func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(w.poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.tick(ctx)
		}
	}
}

func (w *Worker) tick(ctx context.Context) {
	pendientes, err := w.store.IntentosPendientesParaReintentar(ctx, w.ahoraFunc())
	if err != nil {
		log.Printf("reintentos: error listando pendientes: %v", err)
		return
	}
	for _, intento := range pendientes {
		if _, err := w.procesar(ctx, intento.AceptacionID); err != nil {
			log.Printf("reintentos: error procesando aceptacion=%s: %v", intento.AceptacionID, err)
		}
	}
}
