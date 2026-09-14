package main

import (
	"time"

	"github.com/google/uuid"
)

// Orígenes posibles de una respuesta de lectura.
const (
	origenPostgres = "postgres"
	origenRedis    = "redis"
)

// Siniestro representa un registro de la tabla siniestros.
type Siniestro struct {
	ID           uuid.UUID `json:"id"`
	PolizaID     uuid.UUID `json:"poliza_id"`
	Prioridad    int       `json:"prioridad"`
	EstadoActual string    `json:"estado_actual"`
	// ActualizadoEn indica cuándo cambió por última vez el siniestro.
	// Es lo que permite al cliente saber qué tan vieja es la respuesta
	// cuando viene de la caché (stale=true).
	ActualizadoEn time.Time `json:"actualizado_en"`
}

// RegistrarSiniestroInput es el cuerpo esperado al crear un siniestro.
// El id se genera en el servidor, por eso no se incluye aquí.
type RegistrarSiniestroInput struct {
	PolizaID     uuid.UUID `json:"poliza_id"`
	Prioridad    int       `json:"prioridad"`
	EstadoActual string    `json:"estado_actual"`
}

// ActualizarEstadoInput es el cuerpo esperado en PATCH /siniestros/{id}.
type ActualizarEstadoInput struct {
	EstadoActual string `json:"estado_actual"`
}

// SiniestroResponse es lo que se devuelve al cliente: el siniestro más
// los metadatos de frescura. Al estar embebido, el JSON sale plano.
type SiniestroResponse struct {
	Siniestro
	// Stale es true cuando el dato no vino de Postgres sino de la caché,
	// es decir, cuando la respuesta puede estar desactualizada.
	Stale bool `json:"stale"`
	// Origen indica de dónde salió el dato: "postgres" o "redis".
	Origen string `json:"origen"`
}

func nuevaRespuesta(s Siniestro, origen string) SiniestroResponse {
	return SiniestroResponse{
		Siniestro: s,
		Stale:     origen == origenRedis,
		Origen:    origen,
	}
}

func nuevasRespuestas(siniestros []Siniestro, origen string) []SiniestroResponse {
	respuestas := make([]SiniestroResponse, 0, len(siniestros))
	for _, s := range siniestros {
		respuestas = append(respuestas, nuevaRespuesta(s, origen))
	}
	return respuestas
}
