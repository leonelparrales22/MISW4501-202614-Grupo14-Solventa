package main

import (
	"log"
	"os"
	"strconv"
	"time"
)

// Config agrupa toda la parametrización que viene del entorno.
// Nada de esto está quemado en el código: así el mismo binario sirve
// para local, docker-compose o AWS (RDS + ElastiCache).
type Config struct {
	// DatabaseURL es la cadena de conexión a Postgres.
	DatabaseURL string
	// RedisURL es la cadena de conexión a Redis (redis://host:puerto/db).
	RedisURL string
	// DBTimeout es el tiempo máximo que esperamos a Postgres en una consulta
	// de lectura antes de degradar a la caché. Por defecto 900ms.
	DBTimeout time.Duration
	// DBWriteTimeout es el tiempo máximo para escrituras (crear/actualizar).
	// Las escrituras no degradan a caché, por eso el margen es más amplio.
	DBWriteTimeout time.Duration
	// LatenciaSimulada inyecta una demora artificial antes de cada consulta
	// de lectura a Postgres. Sirve para probar el modo degradado sin tener
	// que poner la base de datos lenta de verdad. Por defecto 0 (desactivado).
	LatenciaSimulada time.Duration
	// CacheTTL es el tiempo de vida de cada entrada en Redis.
	CacheTTL time.Duration
	// Addr es la dirección donde escucha el servidor HTTP.
	Addr string
}

var cfg Config

func cargarConfig() Config {
	c := Config{
		DatabaseURL:      env("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/siniestros_db?sslmode=disable"),
		RedisURL:         env("REDIS_URL", "redis://localhost:6379/0"),
		DBTimeout:        envMilisegundos("DB_QUERY_TIMEOUT_MS", 900),
		DBWriteTimeout:   envMilisegundos("DB_WRITE_TIMEOUT_MS", 5000),
		LatenciaSimulada: envMilisegundos("SIMULATED_DB_LATENCY_MS", 0),
		CacheTTL:         time.Duration(envEntero("CACHE_TTL_SECONDS", 86400)) * time.Second,
		Addr:             env("ADDR", ":8080"),
	}
	return c
}

func env(clave, porDefecto string) string {
	if v := os.Getenv(clave); v != "" {
		return v
	}
	return porDefecto
}

func envEntero(clave string, porDefecto int) int {
	v := os.Getenv(clave)
	if v == "" {
		return porDefecto
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		log.Printf("valor inválido en %s=%q, se usa %d", clave, v, porDefecto)
		return porDefecto
	}
	return n
}

func envMilisegundos(clave string, porDefecto int) time.Duration {
	return time.Duration(envEntero(clave, porDefecto)) * time.Millisecond
}
