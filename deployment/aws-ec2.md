# EC-D01 en AWS — ambiente mínimo sobre EC2

Este documento prepara una ejecución **nueva e independiente** de EC-D01 en AWS. Los resultados se deben registrar en `RESULTS-AWS.md`; no se combinan con `RESULTS.md`, que contiene únicamente evidencia local.

## Alcance del ambiente

Una instancia EC2 ejecuta Docker Compose con estos contenedores:

| Contenedor | Función |
|---|---|
| `originacion` | Circuit breaker, timeout y respuesta degradada. |
| `redis` | Caché del perfil de riesgo. |
| `openfinance` | Stub del proveedor externo. |
| `toxiproxy` | Caída, latencia fija e intermitencia del proveedor. |
| `k6` | Tarea efímera de carga, dentro de la misma red Docker. |

Esta es una topología de POC, no una arquitectura productiva: no incorpora API Gateway, ALB, RDS ni ElastiCache.

## Recursos a aprovisionar en `us-east-2`

1. Una instancia EC2 Linux con Docker Engine y Docker Compose plugin. El POC es liviano; selecciona una instancia elegible en el plan de la cuenta que permita ejecutar Docker y la compilación de Go.
2. Un rol de instancia para AWS Systems Manager (`AmazonSSMManagedInstanceCore`) si se usará Session Manager. Así no hace falta abrir SSH.
3. Un security group sin reglas de entrada. Mantén salida HTTPS para descargar imágenes y el código. Los puertos 8080 y 8474 también se enlazan a `localhost` mediante el override de Compose, por lo que no quedan publicados en la red.
4. Acceso al código de la rama del experimento. Antes de crear la instancia, publica la rama o transfiere el repositorio por el mecanismo autorizado por el equipo.

## Preparación de la instancia

Desde la raíz del repositorio en EC2:

```bash
cp deployment/ec2.env.example deployment/ec2.env
docker compose --env-file deployment/ec2.env \
  -f docker-compose.yml -f deployment/docker-compose.ec2.yml up --build -d
docker compose -f docker-compose.yml -f deployment/docker-compose.ec2.yml ps
```

La comprobación normal debe devolver una oferta no provisional:

```bash
curl -X POST http://127.0.0.1:8080/offers \
  -H 'Content-Type: application/json' \
  -d '{"customer_id":"aws-baseline"}'
```

## Corridas AWS

En cada corrida, guarda: fecha/hora, valores del archivo `deployment/ec2.env`, tipo de falla, salida completa de k6 y transiciones del circuit breaker.

### Caída total

1. Precalienta los perfiles de `customer-0` a `customer-4` con solicitudes normales.
2. Deshabilita el proxy:

```bash
curl -X POST http://127.0.0.1:8474/proxies/openfinance \
  -H 'Content-Type: application/json' -d '{"enabled":false}'
```

3. Ejecuta carga:

```bash
EXPECT_PROVISIONAL=true docker compose --env-file deployment/ec2.env \
  --profile load run --rm k6
```

4. Reactiva el proxy antes de cualquier otra corrida:

```bash
curl -X POST http://127.0.0.1:8474/proxies/openfinance \
  -H 'Content-Type: application/json' -d '{"enabled":true}'
```

### Latencia e intermitencia

Usa la API local de Toxiproxy para crear un tóxico de tipo `latency` de 800 ms. Para intermitencia, usa `toxicity: 0.5`. Ejecuta k6 con `EXPECT_PROVISIONAL=any` y extrae las transiciones:

```bash
docker compose -f docker-compose.yml -f deployment/docker-compose.ec2.yml \
  logs --no-color originacion | rg 'circuit_breaker_transition|circuit_breaker_half_open_success'
```

Elimina el tóxico al terminar cada corrida y detén la instancia cuando la evidencia esté recopilada.

## Criterios de aceptación AWS

- 100 % de solicitudes con respuesta normal o degradada.
- p95 de k6 inferior a 700 ms.
- Registro verificable de apertura, `half_open` y recuperación del circuito.
- Ninguna reapertura inmediata durante el escenario de intermitencia para considerar aprobada la configuración de circuit breaker.
