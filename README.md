# Solventa — Backend

MISW4501 · Proyecto Final · Grupo 14 (2026-14). Backend en Go de Solventa, una aseguradora digital (insurtech) basada en Finanzas Abiertas.
La documentación de diseño (arquitectura, decisiones, experimentos, backlog) vive en la [Wiki](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki); este repositorio documenta lo que cambia junto con el código.

> **Estado:** estructura inicial. Cada servicio solo expone `GET /health`; la lógica de negocio se construye por sprints según el [plan de trabajo](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Plan-de-trabajo-por-sprints).

## Estructura

```
.
├── services/            Microservicios de negocio (Go). Un módulo, una BD y un despliegue por servicio
│   ├── origination/       Cotización, perfilamiento, rating, emisión, endosos, renovación, cancelación y cobro
│   ├── claims/            Radicación, triage, peritos, siniestros paramétricos y evidencia
│   ├── partners/          Onboarding B2B, credenciales y tablero de desempeño
│   ├── audit/             Registro inmutable de decisiones y consentimientos
│   └── notifications/     Notificaciones push
├── pkg/                 Librerías compartidas (módulo Go)
├── stubs/               Simuladores del ecosistema externo
├── infra/               Terraform y ambiente local
├── tests/               Pruebas transversales (carga, resiliencia, seguridad, E2E)
├── docs/                Contratos de API, catálogo de eventos, runbooks y arquitectura
├── scripts/             Utilidades de desarrollo y CI
└── .github/workflows/   CI/CD con filtros por ruta
```

Cada carpeta tiene su propio README. Empieza por el del servicio con el que vayas a trabajar.

## Arquitectura en una página

- **Estilo:** capas (clientes → gateway → lógica de negocio → persistencia) con microservicios acotados por capacidad de negocio y publicador/suscriptor para procesos no interactivos.
- **Entrada:** Amazon API Gateway (usage plans y API keys por socio) → ALB interno → servicios en ECS Fargate.
- **Datos:** *database per service* (PostgreSQL Multi-AZ). Consistencia fuerte dentro de un servicio y eventual entre servicios.
- **Eventos:** Amazon SNS (un tópico por dominio) + SQS (una cola por consumidor, con DLQ). Los publicadores usan *transactional outbox* y los consumidores son idempotentes.
- **Caché:** ElastiCache Redis (perfil de riesgo en originación; lectura de estado en siniestros).
- **Integraciones externas:** siempre detrás de adaptadores con circuit breaker (≤ 120 ms por dependencia, corte duro a 700 ms). En este proyecto se simulan con `stubs/`.
- Detalle: [Arquitectura final](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Arquitectura_final) y [Recopilación de decisiones](https://github.com/leonelparrales22/MISW4501-202614-Grupo14-Solventa/wiki/Recopilación-Decisiones-de-Arquitectura).

## Estructura de un servicio (Puertos y Adaptadores)

```
services/<service>/
├── cmd/<service>/         main (raíz de composición)
├── api/openapi.yaml       Contrato (validado con Schemathesis)
├── internal/
│   ├── domain/            Entidades y reglas (sin dependencias de infraestructura)
│   ├── application/       Casos de uso
│   ├── ports/             Interfaces de entrada y salida
│   ├── adapters/          http · postgres · messaging (SNS/SQS)
│   └── <módulos>/         Módulos del servicio con frontera propia
├── migrations/            Esquema de la BD propia
├── tests/{integration,contract}
└── Dockerfile
```

## Reglas del repositorio

Las reglas de arquitectura (fronteras entre servicios, Puertos y Adaptadores, base de datos por servicio), el idioma, la Definición de Terminado y el flujo de Git y Pull Requests están en [`AGENTS.md`](AGENTS.md). Las fronteras entre servicios se verifican automáticamente con `depguard` (ver `.golangci.yml`). Las decisiones tomadas y su motivo están en [`docs/decisions`](docs/decisions/README.md).

## Stack

Go 1.23, `chi/v5` para HTTP, `pgx/v5` para PostgreSQL, Testify y Testcontainers-Go para pruebas.

## Requisitos previos

| Herramienta | Versión | Para qué | Instalación |
|---|---|---|---|
| Go | 1.23 o superior | Compilar y probar | https://go.dev/dl/ |
| make | cualquiera | Ejecutar los comandos del proyecto | Incluido en macOS/Linux (Xcode CLT o `build-essential`) |
| golangci-lint | **v2.x** (la configuración usa `version: "2"`; la v1 no la soporta) | `make lint` | macOS: `brew install golangci-lint` · Cualquiera: `go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest` |
| Docker | reciente | Imágenes de los servicios y, más adelante, el ambiente local y las pruebas de integración | https://docs.docker.com/get-docker/ |

Verifica la instalación:

```
go version
golangci-lint --version   # debe indicar v2.x
```

## Comandos

```
make lint    # golangci-lint
make test    # pruebas con cobertura
make build   # compila todos los servicios en bin/
```
