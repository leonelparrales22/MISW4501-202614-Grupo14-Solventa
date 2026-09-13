# EC-D02 — Cobro idempotente con reintentos

Experimento de arquitectura (Disponibilidad, responsable Leonel) diseñado en
[`Experimentos-de-Arquitectura_final.md`](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Experimentos-de-Arquitectura_final#experimento-ec-d02-cobro-idempotente-con-reintentos)
de la wiki. Determina si una `idempotency_key` con constraint `UNIQUE` en
PostgreSQL, combinada con reintentos con backoff exponencial (ventana de
24h), garantiza **exactamente un cobro exitoso por aceptación** —sin
duplicados ni pérdidas— ante caída de la pasarela de pago, incluyendo el
caso más peligroso: respuesta ambigua (timeout sin saber si el proveedor
procesó) y dos workers concurrentes procesando la misma aceptación.

Los resultados y el análisis final están en la hoja de wiki
[`EC-D02 — Cobro idempotente con reintentos`](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/EC%E2%80%90D02-%E2%80%94-Cobro-idempotente-con-reintentos).
**Hallazgo clave:** el diseño ingenuo (sin claim atómico) sí permitió una
duplicación real de cobro bajo una condición de carrera genuina; la
corrección (ADR-01) y la evidencia post-corrección están en
[`resultados/RESUMEN.md`](resultados/RESUMEN.md).

## Estructura

```
cmd/originacion/         servicio Go: POST /aceptaciones, POST /procesar-cobro/{id}, GET /cobros/{id}, GET /health
internal/domain/         Aceptacion, IntentoCobro, idempotency_key determinística
internal/store/          repo Postgres: CrearIntentoPendiente, ReclamarPendiente (ADR-01), etc.
internal/pasarela/       cliente HTTP hacia el stub, clasifica ErrAmbiguo vs ErrRechazado
internal/cobro/          orquestador del journey de cobro (equivalente al gateway de EC-D01)
internal/reintentos/     worker en background que reprocesa intentos vencidos
db/schema.sql            esquema (tabla intento_cobro con idempotency_key UNIQUE), compartido con los tests
stub-pasarela/           stub de la pasarela de pago: distingue requests de red vs. cargos reales
scripts/toxiproxy.sh     inyecta/retira fallas (down / ambiguo / up) vía la API de Toxiproxy
scripts/run-escenarios.sh   corre los 4 escenarios cualitativos del diseño
scripts/concurrencia/    runner Go con goroutines para la prueba de concurrencia dirigida
scripts/repetir-concurrencia.sh   repite la prueba de concurrencia N veces
scripts/deploy-ec2.sh    despliegue mínimo en AWS (una sola instancia EC2)
resultados/              evidencia de las corridas (RESUMEN.md + raw/)
```

## Correr en local

```bash
go test ./...                  # pruebas unitarias + integración (Testcontainers, requiere Docker)
docker compose up -d --build   # levanta originación + Postgres + Toxiproxy + stub

curl -X POST localhost:8280/aceptaciones -H 'Content-Type: application/json' \
  -d '{"cliente_id":"cliente-1","monto_centavos":15000}'

./scripts/toxiproxy.sh down     # simula caída total de la pasarela
./scripts/toxiproxy.sh ambiguo  # simula respuesta ambigua (corta la respuesta, no el request)
./scripts/toxiproxy.sh up       # restaura

./scripts/run-escenarios.sh todos          # corre los 4 escenarios cualitativos
./scripts/repetir-concurrencia.sh          # repite la prueba de concurrencia (default 10×)
python3 scripts/analizar-resultados.py     # consolida resultados/raw/*.json en tablas Markdown
```

## Desplegar en AWS (solo para grabar el vídeo)

Correr **después** de validar todo en local — evita gastar tiempo de
instancia depurando bugs en la nube. Requiere AWS CLI configurado
(`aws configure`) con un usuario IAM propio, no root.

```bash
./scripts/deploy-ec2.sh up    # crea la instancia y despliega el stack
./scripts/deploy-ec2.sh ssh   # entra por SSH
./scripts/deploy-ec2.sh down  # TERMINA la instancia (hacerlo apenas grabes)
```
