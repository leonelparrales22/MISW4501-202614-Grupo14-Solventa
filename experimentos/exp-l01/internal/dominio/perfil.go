// Package dominio tiene los tipos que usan todos los demás paquetes. Va
// aparte para que nadie dependa de nadie y no se armen ciclos de import.
package dominio

import (
	"strconv"
	"time"
)

// Senal es lo que responde una fuente externa cuando le preguntamos por un cliente.
type Senal struct {
	Fuente   string        `json:"fuente"`
	Valor    float64       `json:"valor"`
	Latencia time.Duration `json:"latencia_ns"`
}

// Perfil es el resultado de perfilar: qué señales llegaron, cuáles no, y el
// puntaje que salió con lo que había.
type Perfil struct {
	ClienteID  string   `json:"cliente_id"`
	Senales    []Senal  `json:"senales"`
	Faltantes  []string `json:"faltantes"`
	Parcial    bool     `json:"parcial"`
	Puntaje    float64  `json:"puntaje"`
	DesdeCache bool     `json:"desde_cache"`
}

// Tramos guarda cuánto duró cada etapa del recorrido. Se manda en la cabecera
// Server-Timing para que k6 pueda ver en cuál se fue el tiempo.
type Tramos struct {
	Cache  time.Duration
	Fanout time.Duration
	Agg    time.Duration
	Rating time.Duration
}

// ServerTiming arma la cabecera con el formato que esperan el navegador y k6:
// "cache;dur=1.20, fanout;dur=118.40, ...".
func (t Tramos) ServerTiming() string {
	ms := func(d time.Duration) string {
		return strconv.FormatFloat(float64(d)/float64(time.Millisecond), 'f', 2, 64)
	}
	return "cache;dur=" + ms(t.Cache) +
		", fanout;dur=" + ms(t.Fanout) +
		", agg;dur=" + ms(t.Agg) +
		", rating;dur=" + ms(t.Rating)
}
