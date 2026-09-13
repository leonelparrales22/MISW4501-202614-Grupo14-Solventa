// Package perfilamiento arma el perfil de riesgo consultando las fuentes
// externas en paralelo. Este es el paquete que estamos midiendo en EXP-L01:
// acá está la concurrencia, el deadline total y el "resolver con lo que llegó".
package perfilamiento

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/experimentos/exp-l01/internal/adaptadores"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/experimentos/exp-l01/internal/cache"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/experimentos/exp-l01/internal/dominio"
)

// Servicio junta las tres cosas: la caché, las consultas en paralelo y la
// agregación con lo que haya llegado.
type Servicio struct {
	fuentes  []adaptadores.FuenteDatos
	cache    cache.Cache
	deadline time.Duration

	cancelaciones atomic.Int64 // consultas que se vencieron
	parciales     atomic.Int64 // perfiles que salieron sin todas las fuentes
	totales       atomic.Int64 // perfiles calculados (los que salen de caché no cuentan)
	enVuelo       atomic.Int64 // consultas en curso ahora mismo; sin carga tiene que dar 0
}

// Nuevo arma el servicio. deadline es el tiempo máximo para todas las fuentes
// juntas (700 ms según el enunciado).
func Nuevo(fuentes []adaptadores.FuenteDatos, c cache.Cache, deadline time.Duration) *Servicio {
	return &Servicio{fuentes: fuentes, cache: c, deadline: deadline}
}

// Perfilar hace todo el recorrido y devuelve el perfil más lo que tardó cada etapa.
func (s *Servicio) Perfilar(ctx context.Context, clienteID string) (dominio.Perfil, dominio.Tramos, error) {
	var tramos dominio.Tramos

	// --- Etapa 1: mirar primero en caché ---
	t0 := time.Now()
	if bytes, acierto, err := s.cache.Obtener(ctx, clienteID); err == nil && acierto {
		var p dominio.Perfil
		if err := json.Unmarshal(bytes, &p); err == nil {
			p.DesdeCache = true
			tramos.Cache = time.Since(t0)
			return p, tramos, nil
		}
	}
	tramos.Cache = time.Since(t0)

	// --- Etapa 2: consultas en paralelo, todas bajo el deadline total ---
	t1 := time.Now()
	ctxFanout, cancelar := context.WithTimeout(ctx, s.deadline)
	defer cancelar()

	type resultado struct {
		senal dominio.Senal
		err   error
	}
	// Ojo: el canal tiene capacidad N a propósito. Así ninguna goroutine se
	// queda bloqueada enviando, y cuando esta función retorna no queda ninguna viva.
	resultados := make(chan resultado, len(s.fuentes))
	for _, f := range s.fuentes {
		s.enVuelo.Add(1)
		go func(f adaptadores.FuenteDatos) {
			defer s.enVuelo.Add(-1)
			sn, err := f.Consultar(ctxFanout, clienteID)
			resultados <- resultado{senal: sn, err: err}
		}(f)
	}

	perfil := dominio.Perfil{ClienteID: clienteID}
	for range s.fuentes {
		r := <-resultados
		if r.err != nil {
			perfil.Faltantes = append(perfil.Faltantes, r.senal.Fuente)
			if errors.Is(r.err, context.DeadlineExceeded) {
				s.cancelaciones.Add(1)
			}
			continue
		}
		perfil.Senales = append(perfil.Senales, r.senal)
	}
	tramos.Fanout = time.Since(t1)

	// --- Etapa 3: agregar con lo que llegó (esto es la degradación) ---
	t2 := time.Now()
	perfil.Parcial = len(perfil.Faltantes) > 0
	perfil.Puntaje = agregar(perfil.Senales)
	s.totales.Add(1)
	if perfil.Parcial {
		s.parciales.Add(1)
	}
	tramos.Agg = time.Since(t2)

	if len(perfil.Senales) == 0 {
		// Si no llegó ninguna señal no hay perfil que dar. Es el único caso que devuelve error.
		return perfil, tramos, errors.New("ninguna fuente respondió dentro del deadline")
	}

	// Guardamos en caché lo que calculamos. Lo ideal sería hacerlo después de
	// responderle al cliente, pero acá es tan barato que preferimos medirlo
	// dentro del recorrido y no esconderlo.
	if bytes, err := json.Marshal(perfil); err == nil {
		_ = s.cache.Guardar(ctx, clienteID, bytes)
	}
	return perfil, tramos, nil
}

// agregar junta las señales que llegaron. Es un promedio y ya: acá medimos
// latencia, no si el puntaje tiene sentido actuarial.
func agregar(senales []dominio.Senal) float64 {
	if len(senales) == 0 {
		return 0
	}
	suma := 0.0
	for _, sn := range senales {
		suma += sn.Valor
	}
	return suma / float64(len(senales))
}

// Estadisticas devuelve los contadores que muestra /debug/estado.
func (s *Servicio) Estadisticas() (cancelaciones, parciales, totales, enVuelo int64) {
	return s.cancelaciones.Load(), s.parciales.Load(), s.totales.Load(), s.enVuelo.Load()
}
