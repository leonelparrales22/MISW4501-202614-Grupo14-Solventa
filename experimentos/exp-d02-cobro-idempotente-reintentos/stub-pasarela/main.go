// stub-pasarela simula una pasarela de pago externa para el experimento
// EC-D02. Se pone detrás de Toxiproxy para inyectar caída total y
// respuesta ambigua de forma controlada.
//
// Punto clave #1: cada request se registra EN MEMORIA de forma inmediata
// al recibirse, ANTES de construir/enviar la respuesta. Así el caso
// "ambiguo" es real: si Toxiproxy corta la respuesta con el toxic
// timeout(stream=downstream), el stub ya "procesó" el request aunque el
// cliente nunca se entere.
//
// Punto clave #2: el stub distingue "requests recibidos" (conteos) de
// "cobros reales nuevos" (cargos_reales). Con SOPORTA_IDEMPOTENCIA=true
// (default, el comportamiento esperable de un proveedor de pagos real que
// respeta la idempotency_key), un reintento de red tras una respuesta
// ambigua sube "conteos" pero NUNCA "cargos_reales" para la misma clave:
// el proveedor devuelve el mismo resultado sin volver a cobrar. Si
// "cargos_reales" muestra más de 1 para la misma clave, eso sí es la
// duplicidad genuina que el experimento busca detectar -contar filas en
// la base de datos propia del servicio de originación NUNCA lo revela,
// porque el UNIQUE de idempotency_key siempre deja esa tabla "limpia"-.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

type solicitudCobro struct {
	IdempotencyKey string `json:"idempotency_key"`
	AceptacionID   string `json:"aceptacion_id"`
	MontoCentavos  int64  `json:"monto_centavos"`
}

type respuestaCobro struct {
	Estado         string `json:"estado"`
	IdempotencyKey string `json:"idempotency_key"`
}

// registro protege, con mutex, dos métricas distintas por
// idempotency_key -distinción central para interpretar correctamente el
// escenario "ambiguo":
//   - conteos: cuántas veces el stub RECIBIÓ un request con esa clave (un
//     reintento de red legítimo tras una respuesta ambigua cuenta aquí,
//     y es normal que sea > 1).
//   - cargosReales: cuántas veces esa clave se procesó como un cobro
//     NUEVO -es decir, cuántas veces realmente "se movió dinero"-. Con
//     soportaIdempotencia=true (el comportamiento por defecto, el de un
//     proveedor de pagos real que respeta idempotency keys) esto queda
//     siempre en 1 por clave, sin importar cuántos requests haya
//     recibido: la segunda vez que ve la misma clave, el proveedor
//     devuelve el mismo resultado sin volver a cobrar. Con
//     soportaIdempotencia=false se simula un proveedor que NO dedupe, para
//     mostrar qué pasaría si esa dependencia no se cumple (fila 2 de la
//     tabla de interpretación del diseño).
//
// Una condición de carrera aquí corrompería silenciosamente la métrica de
// la que depende la conclusión del experimento, así que se usa un mutex
// simple en vez de confiar en que las escrituras concurrentes
// "probablemente" no colisionen.
type registro struct {
	mu                  sync.Mutex
	soportaIdempotencia bool
	porClave            map[string]int
	total               int
	cargosReales        map[string]int
}

func nuevoRegistro(soportaIdempotencia bool) *registro {
	return &registro{
		soportaIdempotencia: soportaIdempotencia,
		porClave:            make(map[string]int),
		cargosReales:        make(map[string]int),
	}
}

// registrarRequest cuenta un request recibido y decide, según
// soportaIdempotencia, si debe procesarse como un cargo nuevo o como una
// repetición idempotente de un cargo ya hecho.
func (r *registro) registrarRequest(clave string) (esCargoNuevo bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.porClave[clave]++
	r.total++

	if r.soportaIdempotencia && r.cargosReales[clave] > 0 {
		return false
	}
	r.cargosReales[clave]++
	return true
}

func (r *registro) snapshot() (conteos map[string]int, cargosReales map[string]int, total int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	conteos = make(map[string]int, len(r.porClave))
	for k, v := range r.porClave {
		conteos[k] = v
	}
	cargosReales = make(map[string]int, len(r.cargosReales))
	for k, v := range r.cargosReales {
		cargosReales[k] = v
	}
	return conteos, cargosReales, r.total
}

func main() {
	port := envOr("PORT", "9100")
	// SOPORTA_IDEMPOTENCIA=true (por defecto) simula un proveedor de
	// pagos real que respeta la idempotency_key: pasarlo a "false"
	// simula el caso pesimista en el que esa dependencia no se cumple,
	// para el escenario documentado en la fila 2 de la tabla de
	// interpretación del diseño.
	soportaIdempotencia := envOr("SOPORTA_IDEMPOTENCIA", "true") != "false"
	reg := nuevoRegistro(soportaIdempotencia)

	mux := http.NewServeMux()

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})

	mux.HandleFunc("/cobros", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var body solicitudCobro
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.IdempotencyKey == "" {
			http.Error(w, "idempotency_key requerido", http.StatusBadRequest)
			return
		}

		// Se registra ANTES de decidir la respuesta: el "proveedor" ya
		// procesó el cobro en este punto, pase lo que pase después con
		// la respuesta. esCargoNuevo distingue un cobro real de una
		// repetición idempotente (ver comentario de tipo registro).
		esCargoNuevo := reg.registrarRequest(body.IdempotencyKey)
		log.Printf("PASARELA request_recibido idempotency_key=%s aceptacion=%s cargo_nuevo=%v", body.IdempotencyKey, body.AceptacionID, esCargoNuevo)

		if r.Header.Get("X-Forzar-Rechazo") == "true" {
			http.Error(w, "cobro rechazado por el emisor", http.StatusPaymentRequired)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(respuestaCobro{Estado: "aprobado", IdempotencyKey: body.IdempotencyKey})
	})

	mux.HandleFunc("/debug/cobros", func(w http.ResponseWriter, r *http.Request) {
		conteos, cargosReales, total := reg.snapshot()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			// conteos: requests de red recibidos por clave (un reintento
			// tras respuesta ambigua es normal que suba esto a >1).
			"conteos": conteos,
			// cargos_reales: cobros NUEVOS procesados por clave. Con
			// soporta_idempotencia=true (default) nunca debería superar 1
			// por clave; si lo hace, es la duplicidad real que el
			// experimento busca detectar.
			"cargos_reales":        cargosReales,
			"soporta_idempotencia": soportaIdempotencia,
			"total_requests":       total,
		})
	})

	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	log.Printf("stub-pasarela: escuchando en :%s", port)
	log.Fatal(srv.ListenAndServe())
}
