package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// La caché guarda cada siniestro serializado en su propia clave
// (siniestro:<uuid>) y mantiene un índice con todos los ids conocidos
// para poder reconstruir el listado cuando Postgres no responde.
const (
	prefijoSiniestro = "siniestro:"
	claveIndice      = "siniestros:index"
	// timeoutLectura es corto a propósito: la caché es el plan B, si además
	// está lenta no tiene sentido seguir esperando al cliente.
	timeoutLectura = 200 * time.Millisecond
	// timeoutEscritura es más holgado: la escritura ocurre cuando Postgres ya
	// respondió y es lo que mantiene la caché caliente, así que vale la pena
	// darle margen (incluye el primer dial y la resolución DNS).
	timeoutEscritura = 2 * time.Second
)

var rdb *redis.Client

func claveSiniestro(id uuid.UUID) string {
	return prefijoSiniestro + id.String()
}

// iniciarCache abre la conexión con Redis. Un fallo aquí no es fatal:
// la API sigue funcionando contra Postgres, solo pierde el modo degradado.
func iniciarCache(ctx context.Context, url string) error {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return err
	}
	rdb = redis.NewClient(opts)

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return rdb.Ping(ctx).Err()
}

// guardarEnCache escribe (o reescribe) un siniestro y lo agrega al índice.
// Se llama en cada creación y en cada actualización, de modo que la caché
// nunca está fría durante la operación normal.
func guardarEnCache(ctx context.Context, s Siniestro) {
	if rdb == nil {
		return
	}

	datos, err := json.Marshal(s)
	if err != nil {
		log.Printf("caché: no se pudo serializar el siniestro %s: %v", s.ID, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeoutEscritura)
	defer cancel()

	pipe := rdb.TxPipeline()
	pipe.Set(ctx, claveSiniestro(s.ID), datos, cfg.CacheTTL)
	pipe.SAdd(ctx, claveIndice, s.ID.String())
	if _, err := pipe.Exec(ctx); err != nil {
		// Un fallo de caché no debe tumbar la petición: Postgres ya respondió.
		log.Printf("caché: no se pudo guardar el siniestro %s: %v", s.ID, err)
	}
}

// guardarVariosEnCache refresca la caché con el resultado de una consulta
// que sí alcanzó a responder desde Postgres.
func guardarVariosEnCache(ctx context.Context, siniestros []Siniestro) {
	for _, s := range siniestros {
		guardarEnCache(ctx, s)
	}
}

// obtenerDeCache devuelve el siniestro cacheado. El segundo valor indica
// si la clave existía.
func obtenerDeCache(ctx context.Context, id uuid.UUID) (Siniestro, bool) {
	if rdb == nil {
		return Siniestro{}, false
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeoutLectura)
	defer cancel()

	datos, err := rdb.Get(ctx, claveSiniestro(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Siniestro{}, false
	}
	if err != nil {
		log.Printf("caché: error leyendo el siniestro %s: %v", id, err)
		return Siniestro{}, false
	}

	var s Siniestro
	if err := json.Unmarshal(datos, &s); err != nil {
		log.Printf("caché: entrada corrupta para el siniestro %s: %v", id, err)
		return Siniestro{}, false
	}
	return s, true
}

// obtenerTodosDeCache reconstruye el listado completo a partir del índice.
// Las entradas que ya expiraron se limpian del índice sobre la marcha.
// El segundo valor indica si se pudo consultar la caché; false significa
// "no sé", distinto de "sé que no hay siniestros".
func obtenerTodosDeCache(ctx context.Context) ([]Siniestro, bool) {
	if rdb == nil {
		return nil, false
	}

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeoutLectura)
	defer cancel()

	ids, err := rdb.SMembers(ctx, claveIndice).Result()
	if err != nil {
		log.Printf("caché: error leyendo el índice de siniestros: %v", err)
		return nil, false
	}
	if len(ids) == 0 {
		return []Siniestro{}, true
	}

	claves := make([]string, 0, len(ids))
	for _, id := range ids {
		claves = append(claves, prefijoSiniestro+id)
	}

	valores, err := rdb.MGet(ctx, claves...).Result()
	if err != nil {
		log.Printf("caché: error leyendo siniestros del índice: %v", err)
		return nil, false
	}

	siniestros := make([]Siniestro, 0, len(valores))
	expirados := []any{}
	for i, v := range valores {
		texto, ok := v.(string)
		if !ok {
			// La clave expiró pero seguía en el índice.
			expirados = append(expirados, ids[i])
			continue
		}
		var s Siniestro
		if err := json.Unmarshal([]byte(texto), &s); err != nil {
			log.Printf("caché: entrada corrupta para el siniestro %s: %v", ids[i], err)
			continue
		}
		siniestros = append(siniestros, s)
	}

	if len(expirados) > 0 {
		if err := rdb.SRem(ctx, claveIndice, expirados...).Err(); err != nil {
			log.Printf("caché: no se pudo limpiar el índice: %v", err)
		}
	}

	return siniestros, true
}

// cacheDisponible hace un ping rápido; se usa en /health.
func cacheDisponible(ctx context.Context) bool {
	if rdb == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, timeoutLectura)
	defer cancel()
	return rdb.Ping(ctx).Err() == nil
}
