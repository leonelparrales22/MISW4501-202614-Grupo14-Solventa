# Servicio `partners` (socios B2B)

Gestiona a los socios de distribución (seguro embebido): alta, credenciales, límites de consumo y desempeño de sus integraciones.

**Estado:** esqueleto; solo expone `GET /health`.

## Responsabilidades

| Módulo (`internal/`) | Qué hace |
|---|---|
| `onboarding` | Incorporación de nuevos socios (objetivo: integrar un socio en ≤ 1 semana sin tocar el núcleo) |
| `credentials` | Generación y rotación de credenciales/tokens y asignación de límites de consumo; secretos en AWS Secrets Manager |
| `dashboard` | Volumen de cotizaciones, pólizas emitidas, conversión, disponibilidad y tiempos de respuesta por socio |

El aislamiento y las cuotas por socio se aplican en Amazon API Gateway (*usage plans* y *API keys*, HA-01); este servicio administra su ciclo de vida.

## Datos y eventos

- **BD propia:** PostgreSQL (socios).
- **Publica / consume:** por definir. Ver [catálogo de eventos](../../docs/events/README.md).

## Historias asociadas

Épica web 3 (F3.1, F3.2) y HA-01.

## Desarrollo

```
go run ./services/partners/cmd/partners
```
