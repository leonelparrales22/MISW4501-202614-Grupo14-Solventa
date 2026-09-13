// Package rating es el motor de tarifas, pero simulado. No es lo que se mide
// en este experimento, así que devuelve algo fijo a partir del puntaje y ya.
// Lo importante es que no cueste tiempo.
package rating

import "github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/experimentos/exp-l01/internal/dominio"

// Cotizacion es lo que devuelve el motor.
type Cotizacion struct {
	Prima      float64  `json:"prima"`
	Coberturas []string `json:"coberturas"`
}

// Motor calcula prima y coberturas. Va en proceso, sin red.
type Motor struct{}

// Calcular aplica una regla fija: prima base más un ajuste por puntaje.
func (Motor) Calcular(p dominio.Perfil) Cotizacion {
	prima := 100.0 + p.Puntaje*10.0
	cob := []string{"vida", "incapacidad"}
	if p.Parcial {
		// Si el perfil salió incompleto solo ofrecemos la cobertura base.
		cob = cob[:1]
	}
	return Cotizacion{Prima: prima, Coberturas: cob}
}
