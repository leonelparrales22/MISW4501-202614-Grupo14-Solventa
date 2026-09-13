// expl01 es el único binario del experimento. Con --mode se decide qué arranca:
// el orquestador (que es lo que estamos probando) o las fuentes simuladas.
// Un solo binario para no tener dos Dockerfiles ni dos repos en ECR.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/experimentos/exp-l01/internal/fuentes"
	"github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/experimentos/exp-l01/internal/orquestador"
)

func main() {
	modo := flag.String("mode", "orquestador", "orquestador | fuentes")
	flag.Parse()

	direccion := os.Getenv("ADDR")
	if direccion == "" {
		direccion = ":8080"
	}

	var manejador http.Handler
	switch *modo {
	case "orquestador":
		manejador = orquestador.Nuevo(orquestador.ConfigDesdeEnv())
	case "fuentes":
		manejador = fuentes.Nuevo(fuentes.ConfigDesdeEnv())
	default:
		log.Fatalf("modo desconocido: %q (use orquestador | fuentes)", *modo)
	}

	srv := &http.Server{
		Addr:              direccion,
		Handler:           manejador,
		ReadHeaderTimeout: 5 * time.Second,
		// Ojo: no ponemos WriteTimeout a propósito. El tiempo máximo lo
		// controla el contexto de cada petición, no el servidor.
	}
	log.Printf("expl01 --mode=%s escuchando en %s", *modo, direccion)
	log.Fatal(srv.ListenAndServe())
}
