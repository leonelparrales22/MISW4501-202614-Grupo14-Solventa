package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var db *sql.DB

func main() {
	// Toda la configuración (Postgres, Redis, timeouts, latencia simulada)
	// se lee del entorno: el mismo binario sirve para local, docker-compose
	// o AWS (RDS + ElastiCache) sin tocar código.
	cfg = cargarConfig()

	var err error
	db, err = sql.Open("pgx", cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("error abriendo conexión a la base de datos: %v", err)
	}
	defer db.Close()

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		log.Fatalf("no se pudo conectar a la base de datos: %v", err)
	}
	log.Println("conexión a la base de datos establecida")

	// Redis es caché de lectura, no fuente de verdad: si no está disponible
	// la API arranca igual, solo que sin modo degradado.
	if err := iniciarCache(ctx, cfg.RedisURL); err != nil {
		log.Printf("advertencia: no se pudo conectar a redis (%v); la API queda sin modo degradado", err)
	} else {
		log.Printf("conexión a redis establecida (ttl de caché: %s)", cfg.CacheTTL)
	}
	defer func() {
		if rdb != nil {
			rdb.Close()
		}
	}()

	log.Printf("timeout de lectura contra postgres: %s", cfg.DBTimeout)
	if cfg.LatenciaSimulada > 0 {
		log.Printf("ATENCIÓN: latencia simulada activa (%s); las lecturas degradarán a caché", cfg.LatenciaSimulada)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /siniestros", listarSiniestros)
	mux.HandleFunc("GET /siniestros/{id}", obtenerSiniestro)
	mux.HandleFunc("POST /siniestros", registrarSiniestro)
	mux.HandleFunc("PATCH /siniestros/{id}", actualizarEstadoSiniestro)
	mux.HandleFunc("GET /health", health)

	log.Printf("servidor escuchando en %s", cfg.Addr)
	log.Fatal(http.ListenAndServe(cfg.Addr, mux))
}
