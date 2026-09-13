package cobro_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"solventa/exp-d02-cobro-idempotente-reintentos/internal/cobro"
	"solventa/exp-d02-cobro-idempotente-reintentos/internal/domain"
	"solventa/exp-d02-cobro-idempotente-reintentos/internal/pasarela"
)

// --- fakes ---

type storeFake struct {
	aceptaciones map[string]domain.Aceptacion
	intentos     map[string]*domain.IntentoCobro // por aceptacion_id
	seq          int
}

func nuevoStoreFake() *storeFake {
	return &storeFake{
		aceptaciones: map[string]domain.Aceptacion{},
		intentos:     map[string]*domain.IntentoCobro{},
	}
}

func (s *storeFake) CrearAceptacion(ctx context.Context, a domain.Aceptacion) error {
	s.aceptaciones[a.ID] = a
	return nil
}

func (s *storeFake) CrearIntentoPendiente(ctx context.Context, aceptacionID, idempotencyKey string) (domain.IntentoCobro, error) {
	if existente, ok := s.intentos[aceptacionID]; ok {
		return *existente, nil
	}
	s.seq++
	it := &domain.IntentoCobro{
		ID:             "intento-" + aceptacionID,
		AceptacionID:   aceptacionID,
		IdempotencyKey: idempotencyKey,
		Estado:         domain.EstadoPendiente,
		CreadoEn:       time.Now(),
		ActualizadoEn:  time.Now(),
	}
	s.intentos[aceptacionID] = it
	return *it, nil
}

func (s *storeFake) ObtenerIntentoPorAceptacion(ctx context.Context, aceptacionID string) (domain.IntentoCobro, bool, error) {
	it, ok := s.intentos[aceptacionID]
	if !ok {
		return domain.IntentoCobro{}, false, nil
	}
	return *it, true, nil
}

// ReclamarPendiente replica, de forma simplificada (sin concurrencia real
// -el fake es de un solo hilo en estos tests-), el UPDATE condicional del
// store real: solo transiciona si el estado actual es "pendiente".
func (s *storeFake) ReclamarPendiente(ctx context.Context, aceptacionID string) (domain.IntentoCobro, bool, error) {
	it, ok := s.intentos[aceptacionID]
	if !ok {
		return domain.IntentoCobro{}, false, nil
	}
	if it.Estado != domain.EstadoPendiente {
		return *it, false, nil
	}
	it.Estado = domain.EstadoEnProceso
	return *it, true, nil
}

func (s *storeFake) MarcarConfirmado(ctx context.Context, intentoID string) error {
	for _, it := range s.intentos {
		if it.ID == intentoID {
			it.Estado = domain.EstadoConfirmado
		}
	}
	return nil
}

func (s *storeFake) MarcarFallidoReintentar(ctx context.Context, intentoID string, proximoIntentoEn time.Time, errMsg string) error {
	for _, it := range s.intentos {
		if it.ID == intentoID {
			it.Estado = domain.EstadoPendiente
			it.Intentos++
			it.ProximoIntentoEn = proximoIntentoEn
			it.UltimoError = errMsg
		}
	}
	return nil
}

func (s *storeFake) MarcarFallidoDefinitivo(ctx context.Context, intentoID string, errMsg string) error {
	for _, it := range s.intentos {
		if it.ID == intentoID {
			it.Estado = domain.EstadoFallido
			it.UltimoError = errMsg
		}
	}
	return nil
}

type pasarelaFake struct {
	llamadas       int
	comportamiento func(llamada int) (pasarela.Resultado, error)
}

func (p *pasarelaFake) Cobrar(ctx context.Context, idempotencyKey, aceptacionID string, montoCentavos int64) (pasarela.Resultado, error) {
	p.llamadas++
	return p.comportamiento(p.llamadas)
}

func cfgPrueba() cobro.Config {
	return cobro.Config{
		BackoffBase: 10 * time.Millisecond,
		BackoffMax:  100 * time.Millisecond,
		Ventana:     time.Hour,
	}
}

// --- tests ---

func TestCrearAceptacionYProcesarCobro_Exito_MarcaConfirmado(t *testing.T) {
	st := nuevoStoreFake()
	pf := &pasarelaFake{comportamiento: func(int) (pasarela.Resultado, error) {
		return pasarela.Resultado{Estado: "aprobado"}, nil
	}}
	servicio := cobro.New(st, pf, cfgPrueba())

	aceptacion, intentoInicial, err := servicio.CrearAceptacion(context.Background(), "cliente-1", 15000)
	require.NoError(t, err)
	assert.Equal(t, domain.EstadoPendiente, intentoInicial.Estado)
	assert.Equal(t, domain.IdempotencyKeyPara(aceptacion.ID), intentoInicial.IdempotencyKey)

	intento, err := servicio.ProcesarCobro(context.Background(), aceptacion.ID)
	require.NoError(t, err)
	assert.Equal(t, domain.EstadoConfirmado, intento.Estado)
	assert.Equal(t, 1, pf.llamadas)
}

func TestProcesarCobro_YaConfirmado_NoVuelveALlamarPasarela(t *testing.T) {
	st := nuevoStoreFake()
	st.intentos["a1"] = &domain.IntentoCobro{
		ID: "i1", AceptacionID: "a1", IdempotencyKey: "cobro-a1",
		Estado: domain.EstadoConfirmado, CreadoEn: time.Now(),
	}
	pf := &pasarelaFake{comportamiento: func(int) (pasarela.Resultado, error) {
		t.Fatal("no debería llamar la pasarela si ya está confirmado")
		return pasarela.Resultado{}, nil
	}}
	servicio := cobro.New(st, pf, cfgPrueba())

	intento, err := servicio.ProcesarCobro(context.Background(), "a1")
	require.NoError(t, err)
	assert.Equal(t, domain.EstadoConfirmado, intento.Estado)
	assert.Equal(t, 0, pf.llamadas)
}

func TestProcesarCobro_Ambiguo_ProgramaReintentoConMismaKey(t *testing.T) {
	st := nuevoStoreFake()
	st.intentos["a2"] = &domain.IntentoCobro{
		ID: "i2", AceptacionID: "a2", IdempotencyKey: "cobro-a2",
		Estado: domain.EstadoPendiente, CreadoEn: time.Now(),
	}
	pf := &pasarelaFake{comportamiento: func(int) (pasarela.Resultado, error) {
		return pasarela.Resultado{}, pasarela.ErrAmbiguo
	}}
	servicio := cobro.New(st, pf, cfgPrueba())

	intento, err := servicio.ProcesarCobro(context.Background(), "a2")
	require.NoError(t, err)
	assert.Equal(t, domain.EstadoPendiente, intento.Estado)
	assert.Equal(t, 1, intento.Intentos)
	assert.True(t, intento.ProximoIntentoEn.After(time.Now()))
	assert.Equal(t, "cobro-a2", intento.IdempotencyKey)
}

func TestProcesarCobro_RechazoExplicito_NoReintenta(t *testing.T) {
	st := nuevoStoreFake()
	st.intentos["a3"] = &domain.IntentoCobro{
		ID: "i3", AceptacionID: "a3", IdempotencyKey: "cobro-a3",
		Estado: domain.EstadoPendiente, CreadoEn: time.Now(),
	}
	pf := &pasarelaFake{comportamiento: func(int) (pasarela.Resultado, error) {
		return pasarela.Resultado{}, pasarela.ErrRechazado
	}}
	servicio := cobro.New(st, pf, cfgPrueba())

	intento, err := servicio.ProcesarCobro(context.Background(), "a3")
	require.NoError(t, err)
	assert.Equal(t, domain.EstadoFallido, intento.Estado)
	assert.Equal(t, 1, pf.llamadas)
}

func TestProcesarCobro_VentanaVencida_MarcaFallidoAunqueSeaAmbiguo(t *testing.T) {
	st := nuevoStoreFake()
	st.intentos["a4"] = &domain.IntentoCobro{
		ID: "i4", AceptacionID: "a4", IdempotencyKey: "cobro-a4",
		Estado: domain.EstadoPendiente, CreadoEn: time.Now().Add(-2 * time.Hour),
	}
	pf := &pasarelaFake{comportamiento: func(int) (pasarela.Resultado, error) {
		return pasarela.Resultado{}, pasarela.ErrAmbiguo
	}}
	cfg := cfgPrueba()
	cfg.Ventana = time.Hour // la aceptación ya tiene 2h, la ventana es de 1h
	servicio := cobro.New(st, pf, cfg)

	intento, err := servicio.ProcesarCobro(context.Background(), "a4")
	require.NoError(t, err)
	assert.Equal(t, domain.EstadoFallido, intento.Estado)
}

func TestComputeBackoff_CreceExponencialYSeAcotaEnMax(t *testing.T) {
	base := 10 * time.Millisecond
	max := 100 * time.Millisecond

	assert.Equal(t, base, cobro.ComputeBackoff(1, base, max))
	assert.Equal(t, 20*time.Millisecond, cobro.ComputeBackoff(2, base, max))
	assert.Equal(t, 40*time.Millisecond, cobro.ComputeBackoff(3, base, max))
	assert.Equal(t, max, cobro.ComputeBackoff(10, base, max))
}
