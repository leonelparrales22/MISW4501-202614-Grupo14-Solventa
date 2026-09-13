package reintentos_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"solventa/exp-d02-cobro-idempotente-reintentos/internal/domain"
	"solventa/exp-d02-cobro-idempotente-reintentos/internal/reintentos"
)

type storeFake struct {
	mu         sync.Mutex
	pendientes []domain.IntentoCobro
}

func (s *storeFake) IntentosPendientesParaReintentar(ctx context.Context, ahora time.Time) ([]domain.IntentoCobro, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var vencidos []domain.IntentoCobro
	for _, it := range s.pendientes {
		if !it.ProximoIntentoEn.After(ahora) {
			vencidos = append(vencidos, it)
		}
	}
	return vencidos, nil
}

func TestWorker_ProcesaSoloLosVencidos(t *testing.T) {
	st := &storeFake{pendientes: []domain.IntentoCobro{
		{AceptacionID: "vencido", ProximoIntentoEn: time.Now().Add(-time.Second)},
		{AceptacionID: "futuro", ProximoIntentoEn: time.Now().Add(time.Hour)},
	}}

	var mu sync.Mutex
	var procesados []string
	procesar := func(ctx context.Context, aceptacionID string) (domain.IntentoCobro, error) {
		mu.Lock()
		procesados = append(procesados, aceptacionID)
		mu.Unlock()
		return domain.IntentoCobro{}, nil
	}

	w := reintentos.New(st, procesar, 5*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	w.Run(ctx)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, procesados, "vencido")
	assert.NotContains(t, procesados, "futuro")
}

func TestWorker_ContinuaTrasErrorDeUnIntento(t *testing.T) {
	st := &storeFake{pendientes: []domain.IntentoCobro{
		{AceptacionID: "falla", ProximoIntentoEn: time.Now().Add(-time.Second)},
		{AceptacionID: "ok", ProximoIntentoEn: time.Now().Add(-time.Second)},
	}}

	var mu sync.Mutex
	var procesados []string
	procesar := func(ctx context.Context, aceptacionID string) (domain.IntentoCobro, error) {
		mu.Lock()
		procesados = append(procesados, aceptacionID)
		mu.Unlock()
		if aceptacionID == "falla" {
			return domain.IntentoCobro{}, assertError{}
		}
		return domain.IntentoCobro{}, nil
	}

	w := reintentos.New(st, procesar, 5*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	w.Run(ctx)

	mu.Lock()
	defer mu.Unlock()
	assert.Contains(t, procesados, "falla")
	assert.Contains(t, procesados, "ok")
}

type assertError struct{}

func (assertError) Error() string { return "error simulado" }
