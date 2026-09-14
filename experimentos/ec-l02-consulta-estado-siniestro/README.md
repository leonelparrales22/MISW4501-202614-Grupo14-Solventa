# API de Siniestros

API REST en Go para consultar, registrar y actualizar siniestros, usando
**PostgreSQL** como fuente de verdad y **Redis** como caché de lectura para
responder aun cuando la base de datos se degrada.

## Endpoints

| Método | Ruta                | Descripción                                                      |
|--------|---------------------|------------------------------------------------------------------|
| GET    | `/siniestros`       | Lista siniestros. Filtros opcionales: `?poliza_id=` y `?estado=`  |
| GET    | `/siniestros/{id}`  | Consulta un siniestro por su id (uuid)                            |
| POST   | `/siniestros`       | Registra un nuevo siniestro                                       |
| PATCH  | `/siniestros/{id}`  | Cambia el `estado_actual` del siniestro                           |
| GET    | `/health`           | Estado de Postgres/Redis y configuración de degradación           |

Toda respuesta de un siniestro incluye:

- `actualizado_en`: cuándo cambió por última vez el registro. Permite saber
  qué tan vieja es la respuesta cuando viene de la caché.
- `stale`: `true` si el dato salió de Redis en vez de Postgres.
- `origen`: `"postgres"` o `"redis"`.

Además se exponen las cabeceras `X-Stale` y `X-Origen-Datos` para monitoreo.

## Modo degradado (tolerancia a fallos)

1. Cada lectura contra Postgres se ejecuta con un timeout de **900 ms**
   (`DB_QUERY_TIMEOUT_MS`).
2. Si Postgres no responde a tiempo —o falla—, la respuesta se arma desde
   Redis y se marca `stale: true`.
3. Si Postgres responde que el siniestro **no existe**, se devuelve `404`:
   la base contestó, no hay nada que degradar.
4. Si Postgres no responde y el dato tampoco está en caché, se devuelve
   `503` con un mensaje explícito.

Las **escrituras nunca degradan**: `POST` y `PATCH` van siempre a Postgres
(timeout `DB_WRITE_TIMEOUT_MS`, 5 s por defecto).

### La caché nunca está fría

Se escribe en Redis en dos momentos:

- **Al crear (`POST`) y al actualizar (`PATCH`)**: el nuevo estado queda en
  caché de inmediato, así que si Postgres se degrada justo después, la
  respuesta `stale` ya refleja el último cambio.
- **En cada lectura exitosa**: refresca lo que ya estaba.

Cada siniestro se guarda en la clave `siniestro:<uuid>` y su id se agrega al
conjunto `siniestros:index`, que es lo que permite reconstruir el listado
(con sus filtros y su orden por prioridad) en modo degradado.

Un fallo de Redis nunca tumba una petición: se registra en el log y la API
sigue sirviendo contra Postgres, solo que sin modo degradado.

## Probar el modo degradado

No hace falta poner Postgres lento de verdad: la variable
`SIMULATED_DB_LATENCY_MS` inyecta una demora artificial en cada **lectura**.
Con un valor mayor a 900 ms, toda consulta cae a la caché.

```bash
# 1. Crear un siniestro en modo normal (queda cacheado)
curl -s -X POST http://localhost:8080/siniestros \
  -H "Content-Type: application/json" \
  -d '{"poliza_id":"6f1b1a2a-6c1a-4e29-9a7e-1a1a1a1a1a1a","prioridad":5,"estado_actual":"abierto"}'

# 2. Activar la latencia simulada
SIMULATED_DB_LATENCY_MS=1500 docker compose up -d

# 3. Consultar: responde en ~900ms con stale=true y origen=redis
curl -i http://localhost:8080/siniestros/<id>

# 4. Volver a modo normal
SIMULATED_DB_LATENCY_MS=0 docker compose up -d
```

En **PowerShell** la variable se asigna aparte (el prefijo `VAR=valor` es de bash):

```powershell
$env:SIMULATED_DB_LATENCY_MS=1500; docker compose up -d   # degradado
$env:SIMULATED_DB_LATENCY_MS=0;    docker compose up -d   # normal

# $env: persiste en la sesión; para limpiarla y volver a usar el .env:
Remove-Item Env:SIMULATED_DB_LATENCY_MS
```

Cambiar de modo **no requiere borrar nada**: Compose solo recrea el contenedor
`api`, mientras `db` y `redis` siguen corriendo y los volúmenes se conservan.

También se puede fijar el valor en el archivo `.env`, que Compose lee
automáticamente. La precedencia es: variable del shell > `.env` > el valor
por defecto del `docker-compose.yml`. Ejecutar `GET /health` confirma cuál
quedó activo.

Sin Docker, exportando la variable antes de arrancar:

```bash
export SIMULATED_DB_LATENCY_MS=1500
go run .
```

`GET /health` confirma si la latencia simulada está activa.

## Ejemplos

```bash
# Registrar
curl -X POST http://localhost:8080/siniestros \
  -H "Content-Type: application/json" \
  -d '{
    "poliza_id": "6f1b1a2a-6c1a-4e29-9a7e-1a1a1a1a1a1a",
    "prioridad": 3,
    "estado_actual": "abierto"
  }'

# Consultar
curl http://localhost:8080/siniestros
curl "http://localhost:8080/siniestros?estado=abierto"
curl http://localhost:8080/siniestros/6f1b1a2a-6c1a-4e29-9a7e-1a1a1a1a1a1a

# Cambiar el estado
curl -X PATCH http://localhost:8080/siniestros/<id> \
  -H "Content-Type: application/json" \
  -d '{"estado_actual": "en_revision"}'
```

## Configuración

Todo se controla por variables de entorno, así el mismo binario sirve para
local, docker-compose o AWS (RDS + ElastiCache) sin tocar código.

| Variable                  | Por defecto                        | Descripción                                            |
|---------------------------|------------------------------------|--------------------------------------------------------|
| `DATABASE_URL`            | `postgres://postgres:postgres@localhost:5432/siniestros_db?sslmode=disable` | Conexión a Postgres                 |
| `REDIS_URL`               | `redis://localhost:6379/0`         | Conexión a Redis                                        |
| `DB_QUERY_TIMEOUT_MS`     | `900`                              | Timeout de lectura antes de degradar a caché            |
| `DB_WRITE_TIMEOUT_MS`     | `5000`                             | Timeout de escritura (no degrada)                       |
| `CACHE_TTL_SECONDS`       | `86400`                            | TTL de cada entrada en Redis                            |
| `SIMULATED_DB_LATENCY_MS` | `0`                                | Demora artificial en lecturas, para probar degradación  |
| `ADDR`                    | `:8080`                            | Dirección de escucha del servidor                       |

Para RDS y ElastiCache:

```bash
export DATABASE_URL="postgres://usuario:password@mi-instancia.us-east-1.rds.amazonaws.com:5432/siniestros_db?sslmode=require"
export REDIS_URL="rediss://mi-cluster.cache.amazonaws.com:6379/0"
```

> En RDS se recomienda `sslmode=require` (o `verify-full` con el certificado
> de AWS) en vez de `disable`. Para ElastiCache con TLS, usar `rediss://`.

Ver `.env.example` para más detalle.

## Crear la tabla

`schema.sql` es idempotente: crea la tabla si no existe y agrega la columna
`actualizado_en` si la base venía de una versión anterior.

```bash
psql "$DATABASE_URL" -f schema.sql
```

> Con docker-compose, `schema.sql` solo se ejecuta la **primera** vez que se
> crea el volumen. Si ya tenías la base creada, aplica la migración a mano:
>
> ```bash
> docker compose exec -T db psql -U postgres -d siniestros_db -f /docker-entrypoint-initdb.d/schema.sql
> ```

## Correr el proyecto

Con Docker (levanta API + Postgres + Redis):

```bash
docker compose up -d --build
```

En local:

```bash
go mod tidy   # descarga dependencias (pgx, google/uuid, go-redis)
go run .
```

El servidor queda escuchando en `http://localhost:8080`.

## Estructura del proyecto

```
siniestros-api/
├── go.mod
├── main.go             # arranque del servidor, conexión a BD y caché, rutas
├── config.go           # configuración leída del entorno
├── models.go           # structs Siniestro y respuestas con stale/origen
├── handlers.go         # endpoints + acceso a datos + degradación
├── cache.go            # capa de caché sobre Redis (go-redis)
├── schema.sql          # DDL de referencia de la tabla
├── docker-compose.yml  # api + postgres + redis
└── .env.example
```
powershell:

$env:SIMULATED_DB_LATENCY_MS=1500; docker compose up -d