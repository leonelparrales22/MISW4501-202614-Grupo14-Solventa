// Package adaptadores es la librería que va dentro del servicio (la caja
// «library» del diagrama de componentes). La idea es que perfilamiento no
// tenga que saber cómo se habla con cada proveedor: solo conoce la interfaz
// FuenteDatos y listo.
package adaptadores

import (
	"context"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/experimentos/exp-l01/internal/dominio"
)

// FuenteDatos es lo que perfilamiento ve de cada proveedor. Lo importante:
// Consultar tiene que respetar el contexto. Si se vence, cancela y devuelve
// context.DeadlineExceeded, no se queda esperando.
type FuenteDatos interface {
	Nombre() string
	Consultar(ctx context.Context, clienteID string) (dominio.Senal, error)
}
