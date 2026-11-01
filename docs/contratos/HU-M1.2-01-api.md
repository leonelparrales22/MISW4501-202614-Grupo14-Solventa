# Contrato de API — HU-M1.2-01 · Billetera de pólizas con consulta sin conexión

> **Fuente para el cliente móvil (Kotlin).** Describe lo que implementa el servicio `origination`
> (`internal/wallet`, `internal/adapters/policycore`, `internal/adapters/http/wallet.go`).
> Contrato formal: `services/origination/api/openapi.yaml` (0.13.0). Alimenta la pantalla **M4 «Mis pólizas»** y el detalle de la póliza
> (M5) de la app, que guarda la lista cifrada en el teléfono para mostrarla sin conexión. Es la misma ruta de la pantalla **A1** del portal
> (HU-W1.5-02): los campos nuevos son compatibles con la versión 0.12.0.

## 1. Flujo

```
App (con red) ──GET /v1/policies──▶ origination ─┬─▶ pólizas que emitió originación (vida, con su renovación)
                                                 └─▶ núcleo de pólizas: /holders/{sub}/policies + /holders/{sub}
     ◀── items + partial ──────────────────────────┘
App ─▶ compara con su copia (cambios) ─▶ reemplaza la copia cifrada y anota la hora de sincronización
App (sin red) ─▶ muestra la copia y la hora de la última sincronización
```

- **Quién es el titular.** El `sub` del ID token de Cognito; nunca viaja como parámetro. Cada asegurado ve solo sus pólizas.
- **Dos fuentes.** Las pólizas que emitió originación (`source: origination`, `line: life`) y las del núcleo de pólizas
  (`source: policy_core`: Autos, Hogar, Viaje y Vida), cada una con el perfil del titular como asegurado principal.
- **Lista parcial.** Si el núcleo no responde, la respuesta sigue siendo `200` con `partial: true` y solo las pólizas de originación.
  **El cliente no debe reemplazar su copia con una lista parcial**: las que faltan no están canceladas, solo no llegaron.
- **Cambios.** El servicio no guarda versiones por dispositivo: la app detecta las renovaciones, cancelaciones y modificaciones
  comparando su copia con la lista nueva (ver §3).

## 2. Endpoint

### `GET /v1/policies` → `200`

ID token con rol `insured` (`401` sin token, `403` con otro rol). `Cache-Control: no-store`.

```json
{
  "items": [
    {
      "policy_id": "AU-2025-118204",
      "source": "policy_core",
      "line": "auto",
      "product": "Autos",
      "insured_object": "KJT-482",
      "status": "active",
      "holder_name": "Carolina Pérez Ruiz",
      "holder": { "full_name": "Carolina Pérez Ruiz", "document_type": "CC", "document_id": "1020304050" },
      "premium": null,
      "currency": "COP",
      "insured_amount": 72500000,
      "coverages": [
        { "code": "collision", "insured_amount": 72500000 },
        { "code": "theft", "insured_amount": 72500000 }
      ],
      "coverage_start": "2025-10-02T00:00:00Z",
      "coverage_end": "2026-10-02T00:00:00Z",
      "renewal_of": null,
      "renewal_window": null,
      "can_renew": false,
      "renewal": null
    }
  ],
  "partial": false
}
```

| Campo | Qué es |
|---|---|
| `policy_id` | Número de la póliza (criterio 2) |
| `source` | `origination` o `policy_core`. Solo las de originación se renuevan desde Solventa |
| `line` | Tipo de seguro: `life`, `auto`, `home` o `travel`. **El cliente lo nombra en su idioma**; `product` es el nombre que da el núcleo, en español |
| `insured_object` | Lo asegurado: una placa, una dirección, un viaje; `null` si la fuente no lo dice |
| `status` | `active`, `expired` o `cancelled`. Las de originación son `active` hasta que termina la cobertura y `expired` después |
| `holder` | Asegurado principal: nombre y documento, **sin datos de contacto** (criterio 2). `holder_name` se conserva de 0.12.0 |
| `premium` | Prima mensual; `null` en las del núcleo, que no guarda primas |
| `insured_amount` | Valor asegurado de la póliza; en las del núcleo, el de su mayor cobertura |
| `coverages[]` | Cada cobertura con su monto asegurado (criterio 2). `code`: `death`, `total_permanent_disability` (originación) o `collision`, `theft`, `third_party_damage`, `glass`, `other`, `water_damage`, `flight_delay`, `death` (núcleo) |
| `coverage_start`, `coverage_end` | Vigencia, RFC 3339 en UTC (criterio 2) |
| `renewal_of` | La póliza que esta renueva; `null` si es nueva o es del núcleo |
| `renewal_window`, `can_renew`, `renewal` | Estado de la renovación de HU-W1.5-02; `null`, `false` y `null` en las del núcleo |
| `partial` | `true` si el núcleo no respondió y sus pólizas faltan |

Los montos van en unidades de la moneda (pesos), no en centavos.

**Errores.** `401` / `403` como cualquier ruta humana. `503 renewal_unavailable` si falla la base de originación (las pólizas que emitió no se
pueden leer); la app conserva su copia y lo reintenta en la siguiente sincronización.

## 3. Cómo detecta cambios la app (criterio 4)

Al sincronizar, la app compara la copia que tenía con la lista nueva, póliza por póliza:

| Caso | Cambio |
|---|---|
| La póliza tiene `renewal_of` igual a una de la copia, o conserva el número y su `coverage_end` es posterior | Renovada |
| Conserva el número y pasa a `status: cancelled` | Cancelada |
| Conserva el número y cambia otro dato (coberturas, montos, objeto, vigencia de inicio, estado) | Modificada |

La primera sincronización de una cuenta no avisa cambios: no hay copia con qué comparar. Una lista con `partial: true` no se compara.

## 4. Lo que queda fuera

- **Primas de las pólizas del núcleo:** el núcleo no las guarda (`premium: null`).
- **Cancelación en originación:** el módulo `cancellation` sigue reservado; las pólizas de originación nunca salen `cancelled`.
- **Notificación *push*** de los cambios: es de notificaciones (HU-M3.x). Esta historia avisa dentro de la app.
