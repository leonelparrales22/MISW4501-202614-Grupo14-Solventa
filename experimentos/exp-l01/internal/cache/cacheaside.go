// Package cache es el Cache-Aside sobre Redis para no recalcular perfiles.
// Guarda bytes y no el tipo Perfil, pues si este paquete importara dominio
// y dominio importara cache se arma un ciclo de imports.
package cache

import (
	"context"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// Cache es la interfaz de la caché de perfiles.
type Cache interface {
	Obtener(ctx context.Context, clave string) (valor []byte, acierto bool, err error)
	Guardar(ctx context.Context, clave string, valor []byte) error
}

// Redis es la implementación con go-redis. En AWS es ElastiCache, en local
// un contenedor.
type Redis struct {
	rdb *redis.Client
	ttl time.Duration
}

// NuevaRedis se conecta al Redis que le digan. Si Redis no está, no falla:
// el orquestador tiene que arrancar igual y simplemente tratar cada consulta
// como si no hubiera nada en caché.
func NuevaRedis(direccion string, ttl time.Duration) *Redis {
	return &Redis{
		rdb: redis.NewClient(&redis.Options{
			Addr:         direccion,
			DialTimeout:  200 * time.Millisecond,
			ReadTimeout:  50 * time.Millisecond,
			WriteTimeout: 50 * time.Millisecond,
			PoolSize:     100,
		}),
		ttl: ttl,
	}
}

func (r *Redis) Obtener(ctx context.Context, clave string) ([]byte, bool, error) {
	valor, err := r.rdb.Get(ctx, "perfil:"+clave).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return valor, true, nil
}

func (r *Redis) Guardar(ctx context.Context, clave string, valor []byte) error {
	return r.rdb.Set(ctx, "perfil:"+clave, valor, r.ttl).Err()
}

// Ping revisa que Redis responda. Lo usa /health del orquestador.
func (r *Redis) Ping(ctx context.Context) error { return r.rdb.Ping(ctx).Err() }

// Nula es una caché que nunca tiene nada. Sirve para correr sin Redis o
// para forzar la combinación de "caché 0 %".
type Nula struct{}

func (Nula) Obtener(context.Context, string) ([]byte, bool, error) { return nil, false, nil }
func (Nula) Guardar(context.Context, string, []byte) error         { return nil }
