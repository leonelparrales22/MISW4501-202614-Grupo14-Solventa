// Package domain define las entidades del experimento EC-D02: la
// aceptación de una oferta y su intento de cobro asociado.
package domain

import "time"

// EstadoIntento es el estado de un Intento_Cobro.
type EstadoIntento string

const (
	EstadoPendiente EstadoIntento = "pendiente"
	// EstadoEnProceso es el estado intermedio introducido por la
	// corrección del ADR-01 (ver internal/store.ReclamarPendiente): un
	// intento pasa por aquí mientras se llama a la pasarela, para que un
	// segundo llamador concurrente (otro worker, o el disparo síncrono
	// del journey) no pueda reclamarlo también. La transición
	// pendiente -> en_proceso es un UPDATE condicional atómico: Postgres
	// garantiza que solo uno de dos llamadores concurrentes la gane.
	EstadoEnProceso  EstadoIntento = "en_proceso"
	EstadoConfirmado EstadoIntento = "confirmado"
	// EstadoFallido es un fallo definitivo: rechazo explícito de la
	// pasarela o vencimiento de la ventana de reintentos (24h). Un intento
	// en este estado NUNCA se vuelve a reintentar.
	EstadoFallido EstadoIntento = "fallido"
)

// Aceptacion representa la aceptación de una oferta por parte de un
// cliente, que dispara el cobro de la prima.
type Aceptacion struct {
	ID            string
	ClienteID     string
	MontoCentavos int64
	Moneda        string
	CreadaEn      time.Time
}

// IntentoCobro es la entidad bajo prueba del experimento: su
// IdempotencyKey, con constraint único en base de datos, es la última
// línea de defensa contra un cobro duplicado.
type IntentoCobro struct {
	ID               string
	AceptacionID     string
	IdempotencyKey   string
	Estado           EstadoIntento
	Intentos         int
	ProximoIntentoEn time.Time
	CreadoEn         time.Time
	ActualizadoEn    time.Time
	UltimoError      string
}

// IdempotencyKeyPara calcula la clave de idempotencia de una aceptación.
//
// Decisión de diseño central del experimento: la clave es DETERMINÍSTICA
// a partir del aceptacionID, nunca aleatoria por intento. Si cada intento
// generara su propia clave, el constraint único de la base de datos no
// protegería nada — toda la hipótesis de EC-D02 descansa en que reintentar
// el mismo cobro siempre reutiliza la misma clave.
func IdempotencyKeyPara(aceptacionID string) string {
	return "cobro-" + aceptacionID
}
