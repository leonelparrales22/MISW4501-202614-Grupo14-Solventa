package adaptadores

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/experimentos/exp-l01/internal/dominio"
)

// FuenteHTTP es la que habla HTTP con una fuente. Cada consulta lleva su
// propio corte (los 120 ms por dependencia del enunciado), y ese corte sale
// del contexto padre, así que si se vence el deadline total esta también se cancela.
type FuenteHTTP struct {
	nombre  string
	url     string
	corte   time.Duration
	cliente *http.Client
}

// NuevaFuenteHTTP arma una fuente contra esa url con ese corte. Todas usan
// el mismo http.Client, pues así el pool de conexiones se ve en un solo lugar.
func NuevaFuenteHTTP(nombre, url string, corte time.Duration, cliente *http.Client) *FuenteHTTP {
	return &FuenteHTTP{nombre: nombre, url: url, corte: corte, cliente: cliente}
}

func (f *FuenteHTTP) Nombre() string { return f.nombre }

// Consultar hace el GET con context.WithTimeout(ctx, corte). Si se vence el
// corte o el deadline de arriba, http.Client cancela lo que esté en vuelo y
// suelta la conexión. Justo eso es lo que queremos comprobar en el experimento.
func (f *FuenteHTTP) Consultar(ctx context.Context, clienteID string) (dominio.Senal, error) {
	ctx, cancelar := context.WithTimeout(ctx, f.corte)
	defer cancelar()

	inicio := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.url+"?cliente="+clienteID, nil)
	if err != nil {
		return dominio.Senal{Fuente: f.nombre}, err
	}
	resp, err := f.cliente.Do(req)
	if err != nil {
		return dominio.Senal{Fuente: f.nombre, Latencia: time.Since(inicio)}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return dominio.Senal{Fuente: f.nombre, Latencia: time.Since(inicio)},
			fmt.Errorf("fuente %s: HTTP %d", f.nombre, resp.StatusCode)
	}
	var cuerpo struct {
		Valor float64 `json:"valor"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cuerpo); err != nil {
		return dominio.Senal{Fuente: f.nombre, Latencia: time.Since(inicio)}, err
	}
	return dominio.Senal{Fuente: f.nombre, Valor: cuerpo.Valor, Latencia: time.Since(inicio)}, nil
}

// ContadorConexiones lleva la cuenta de cuántas conexiones TCP hay abiertas.
// Nos sirve para demostrar que al cancelar no queda nada colgado, que es la
// hipótesis (b) del experimento.
type ContadorConexiones struct {
	abiertas atomic.Int64
	total    atomic.Int64
}

// Abiertas dice cuántas conexiones hay vivas ahora mismo.
func (c *ContadorConexiones) Abiertas() int64 { return c.abiertas.Load() }

// Total dice cuántas se han abierto desde que arrancó.
func (c *ContadorConexiones) Total() int64 { return c.total.Load() }

// Marcar devuelve un DialContext que cuenta cada apertura y cada cierre.
func (c *ContadorConexiones) Marcar(base *net.Dialer) func(ctx context.Context, red, dir string) (net.Conn, error) {
	return func(ctx context.Context, red, dir string) (net.Conn, error) {
		conn, err := base.DialContext(ctx, red, dir)
		if err != nil {
			return nil, err
		}
		c.abiertas.Add(1)
		c.total.Add(1)
		return &conexionContada{Conn: conn, contador: c}, nil
	}
}

type conexionContada struct {
	net.Conn
	contador *ContadorConexiones
	una      sync.Once
}

func (cc *conexionContada) Close() error {
	cc.una.Do(func() { cc.contador.abiertas.Add(-1) })
	return cc.Conn.Close()
}

// NuevoTransporte arma el http.Transport con el contador puesto y un pool
// lo bastante grande para las consultas en paralelo.
func NuevoTransporte(contador *ContadorConexiones, maxConexPorHost int) *http.Transport {
	return &http.Transport{
		DialContext:         contador.Marcar(&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}),
		MaxIdleConns:        maxConexPorHost * 4,
		MaxIdleConnsPerHost: maxConexPorHost,
		MaxConnsPerHost:     maxConexPorHost,
		IdleConnTimeout:     90 * time.Second,
		ForceAttemptHTTP2:   false,
	}
}
