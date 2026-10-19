# Contrato de API — HU-W1.3-02 · Cobro de la prima y activación de la cobertura

> **Fuente para el front (Angular).** Describe lo que implementa el servicio `origination` (`internal/billing`,
> `internal/adapters/{http/charge.go,postgres,payments}`) para la captura del medio de pago. Contrato formal:
> `services/origination/api/openapi.yaml` **v0.9.0**. Alimenta la pantalla **E4 «Paga tu primera prima»**
> (`/embebido/pago`) y su variante **E4c «Pago rechazado»**. Continúa el contrato de
> [HA-D02](HA-D02-api.md), que describe el mecanismo de cobro que esta historia dispara.

## 1. Qué se actualiza en HA-D02

HA-D02 dejó el cobro funcionando de punta a punta: al aprobarse la suscripción se persiste un cobro pendiente y un
*worker* lo cobra en segundo plano. **Lo que faltaba es el medio de pago**: la pasarela recibía referencia, cliente y
monto, pero nunca una tarjeta.

Esta historia inserta ese paso. El cobro se sigue creando al aceptar —el punto de no pérdida no se mueve— pero su
primer intento no vence hasta que el cliente entrega un medio de pago en E4.

```
aceptación ──▶ suscripción aprueba ──▶ cobro pendiente, primer intento sin vencer
                                        │  (clave cobro-<acceptance_id>, ventana de 24 h ya corriendo)
                                        │
                           E4 ──POST …/payment-method──▶ primer intento vence ahora
                                        │
                                        ├─ pasarela responde 2xx ─▶ confirmed ─▶ cobertura activa
                                        ├─ pasarela no responde ──▶ pending: mecanismo de HA-D02 sin cambios
                                        ├─ pasarela rechaza ──────▶ failed (payment_rejected) ─▶ E4c
                                        └─ ventana vencida sin tarjeta ─▶ failed (payment_method_missing)
```

**Lo que no cambia:** el cobro se persiste antes de cualquier llamada a la pasarela (sin pérdida); la clave
`cobro-<acceptance_id>` sigue siendo determinista (sin duplicados); el reclamo atómico, las esperas crecientes, la
ventana de 24 horas y el `resume` de operaciones quedan intactos. **`pkg/idempotency` no se ve afectado**, y por eso el cobro
que espera tarjeta sigue en estado `pending` en lugar de estrenar un estado nuevo.

**Lo que sí cambia:** `domain.NewCharge` dejaba `NextAttemptAt` en el instante de la aceptación; ahora lo deja sin fijar
hasta que llega el medio de pago. Un cobro puede estar `pending` sin un limite, y no se es clara la razón real del por qué no sale de un estado Pending, ya que no es un fallo de la pasarela.

**La ventana de 24 horas sigue contando desde la aceptación**. Quien tarde más de un
día en pagar cae en excepción sin que se haya intentado ningún cobro; por se detalla la información dada como motivo, `payment_method_missing` y no`retry_window_expired`, que no sería una información clara en la bandeja de operaciones.

## 2. El medio de pago no pasa por Solventa

El frame E4 pide número de tarjeta, vencimiento y código de seguridad. **Esos datos no llegan a este servicio.** El
navegador los envía directamente a la pasarela, que devuelve un token; el *front* nos manda solo ese token con la marca
y los últimos cuatro dígitos.

```
navegador ──tarjeta──▶ pasarela (tokeniza) ──tok_…──▶ navegador
navegador ──tok_…────▶ origination
```

Mantiene a Solventa fuera del alcance de PCI-DSS, que es lo que la propia pantalla promete al usuario. El servicio
nunca registra, persiste ni traza el número de la tarjeta.

PSE no tokeniza: el *front* envía `method: "pse"` con el banco elegido y la pasarela resuelve la redirección.

## 3. `POST /v1/acceptances/{acceptance_id}/charge/payment-method` → `202`

Entrega el medio de pago de un cobro pendiente y hace que su primer intento venza de inmediato. Lo llama E4 tras
tokenizar. Solo el asegurado dueño de la aceptación.

Cuerpo con tarjeta de crédito:

```json
{ "method": "card", "payment_token": "tok_7b3c1f9a", "brand": "visa", "last4": "1111", "installments": 1 }
```

Cuerpo con PSE:

```json
{ "method": "pse", "bank_code": "1007" }
```

- `method`: `card` o `pse`.
- `payment_token`: obligatorio con `card`; de 8 a 128 caracteres de `[A-Za-z0-9_-]`. **Nunca un número de tarjeta**: el
  servicio rechaza con `400 invalid_request` cualquier valor de 13 a 19 dígitos, para que un error del *front* no
  arrastre un PAN hasta aquí.
- `brand` y `last4`: para mostrar «Visa ···1111» en el resumen y en la póliza. `last4` son exactamente cuatro dígitos.
- `installments`: 1 a 36, por omisión 1.
- `bank_code`: obligatorio con `pse`.

Devuelve el cobro (`Charge`) ya con su `payment_method` y `next_attempt_at` en el instante de la llamada.

**Es idempotente por reemplazo:** llamarlo otra vez sustituye el medio de pago y vuelve a vencer el intento. Es lo que
hace «Intentar con otro medio de pago» de E4c, y por eso no hace falta una ruta aparte para reintentar. Qué cobros lo
admiten y con qué clave de idempotencia se cobra cada intento está en la **sección 4**.

| Error | Cuándo |
|---|---|
| `400 invalid_request` | Cuerpo inválido, token con forma de tarjeta, `last4` que no son cuatro dígitos, `installments` fuera de rango |
| `401` | Sin sesión |
| `403` | La aceptación es de otro cliente |
| `404 charge_not_found` | La aceptación no tiene cobro: no se ha aprobado, está en revisión o no existe |
| `409 charge_not_payable` | El cobro está `in_progress` o `confirmed`, o está `failed` por `retry_window_expired` o `payment_method_missing`. Un cobro **rechazado** sí admite otro medio de pago: ver la sección 4 |
| `503 charge_unavailable` | No se pudo leer o escribir el cobro; reintentar |

De dos llamadas simultáneas gana una; la otra recibe `409`.

## 4. Reintentar con otra tarjeta: la clave de idempotencia cambia

Cuando la pasarela **rechaza** la tarjeta, el frame E4c ofrece «Intentar con otro medio de pago». Eso obliga a mirar de
cerca la clave de idempotencia, porque la pasarela guarda el resultado de cada clave y lo repite:

| Qué se intentaría | Qué responde la pasarela |
|---|---|
| Misma clave, mismo cuerpo | Repite el rechazo guardado. La tarjeta nueva ni se intenta |
| Misma clave, cuerpo distinto (token nuevo) | `422 idempotency_key_reused` |

Por eso **cada medio de pago nuevo se cobra con una clave nueva**:

```
1.ª captura   cobro-<acceptance_id>        (igual que hoy)
2.ª captura   cobro-<acceptance_id>-2
3.ª captura   cobro-<acceptance_id>-3
```

La primera no cambia a propósito: lo que HA-D02 ya hace, y sus pruebas, siguen igual.

### La regla que conserva la garantía

> **Solo se emite una clave nueva tras un rechazo definitivo de la pasarela.** Nunca tras un tiempo de espera, un error
> de red ni ningún resultado ambiguo.

Es la distinción que evita cobrar dos veces. Un rechazo **es una respuesta**: consta que no se cobró, así que intentar
con otra tarjeta es seguro. Un silencio no dice nada —el cobro pudo haberse hecho—, y ahí la clave tiene que seguir
siendo la misma, que es exactamente lo que HA-D02 protege. Los dos caminos quedan así:

```
rechazo   ─▶ failed (payment_rejected) ─▶ E4c ─▶ otra tarjeta ─▶ clave nueva ─▶ pending
silencio  ─▶ pending, reintentos con la MISMA clave ─▶ E4b          (mecanismo de HA-D02, sin cambios)
```

### Qué cobros admiten otro medio de pago

| Estado del cobro | ¿Admite? | Por qué |
|---|---|---|
| `pending` sin medio de pago | Sí | Es la primera captura, el caso normal de E4 |
| `pending` con medio de pago | Sí | El cliente se arrepintió antes de que la pasarela respondiera; se reemplaza y se vuelve a vencer el intento |
| `failed` · `payment_rejected` | **Sí** | Es E4c. Clave nueva |
| `failed` · `retry_window_expired` | No | La pasarela nunca respondió: el resultado es ambiguo y solo operaciones puede reanudarlo |
| `failed` · `payment_method_missing` | No | La ventana venció; la aceptación se conserva, pero el trámite lo retoma operaciones |
| `in_progress` | No | Hay un intento en curso |
| `confirmed` | No | Ya se cobró |

Los casos que no admiten responden `409 charge_not_payable`.

## 5. `GET /v1/acceptances/{acceptance_id}/charge` — campos nuevos

La ruta no cambia; su respuesta adiciona dos campos y un motivo de fallo. **Las tres adiciones son compatibles**: nada
de lo que hoy consume este endpoint deja de funcionar.

```json
"payment_method": {
  "method": "card", "brand": "visa", "last4": "1111",
  "installments": 1, "bank_code": null, "captured_at": "2026-10-17T14:05:00Z"
},
"payment_attempt": 2
```

`payment_method` es nulo mientras no haya medio de pago.

`payment_attempt` cuenta **cuántos medios de pago ha entregado el cliente**: 0 antes del primero, 1 con el primero, 2
tras un rechazo y otra tarjeta. No se confunde con `attempts`, que cuenta los intentos fallidos de la pasarela dentro
de la ventana y lo maneja el mecanismo de HA-D02. Sirve para dos cosas en E4c: decir «segundo intento» en pantalla, y
saber con qué clave se está cobrando (`cobro-<id>` con 1, `cobro-<id>-2` con 2…).

**Es el campo que el *front* debe mirar para decidir qué pintar**, y no deducirlo de `attempts` y `next_attempt_at`:

| Situación | `status` | `payment_method` | Pantalla |
|---|---|---|---|
| Esperando la tarjeta | `pending` | `null` | **E4** |
| Cobrando | `in_progress` | presente | Procesando |
| Pasarela sin responder | `pending` | presente | E4b · Pago pendiente |
| Rechazado | `failed` + `payment_rejected` | presente | **E4c**, con «Intentar con otro medio de pago» |
| Otra tarjeta tras el rechazo | `pending` | la nueva, `payment_attempt` + 1 | Procesando |
| Sin tarjeta a tiempo | `failed` + `payment_method_missing` | `null` | Excepción |
| Ventana vencida sin respuesta | `failed` + `retry_window_expired` | presente | Excepción, **sin** reintento del cliente |
| Confirmado | `confirmed` | presente | E5 · Póliza emitida |

`failure_reason` gana el valor **`payment_method_missing`**: la ventana de 24 horas venció sin que el cliente entregara
un medio de pago. Aparece también en `GET /v1/issuance-exceptions`, de donde se sirve el monitoreo de HU-W1.3-04.

## 6. Lo que cambia en la pasarela simulada

`stubs/payment-gateway` acepta hoy referencia, cliente, monto y moneda. Esta historia le añade:

- **`POST /tokens`** → `{ "token": "tok_…", "brand": "visa", "last4": "1111" }`. Es lo que llama el navegador. Rechaza
  con `400` una tarjeta que no pase la validación de Luhn.
- **`POST /charges`** acepta `payment_token` y decide con números de prueba convencionales: `4111 1111 1111 1111`
  aprueba, y las terminadas en `0002` rechazan con `insufficient_funds`. **Sin esto, E4c no es alcanzable en una
  demostración.**

El cambio es aditivo: un `POST /charges` sin `payment_token` se sigue comportando como hoy, para no romper a quien ya
lo use.

## 7. Decisiones y por qué

| Decisión | Alternativa descartada |
|---|---|
| El cobro espera con `NextAttemptAt` sin fijar | Un estado nuevo tipo `awaiting_payment_method`, que habría obligado a tocar `pkg/idempotency`, compartido por los cinco servicios |
| La ruta cuelga de `…/charge/payment-method` | `…/payment-method` a secas: el medio de pago pertenece al cobro, no a la aceptación |
| Reemplazar el medio de pago en la misma ruta | Una ruta de reintento aparte, que duplicaría validación y reglas |
| El token lo obtiene el navegador | Tokenizar en el servidor, que metería el PAN en nuestro dominio y a Solventa en alcance PCI-DSS |
| `payment_method` explícito en la respuesta | Que el *front* dedujera el estado de `attempts == 0 && next_attempt_at == null`, acoplamiento implícito que se rompe al cambiar cuándo se incrementa `attempts` |
| Clave nueva solo tras un rechazo definitivo | Clave nueva en cada captura, que tras un silencio de la pasarela podría cobrar dos veces: es justo lo que HA-D02 evita |
| Clave con sufijo `-2`, `-3` | Un identificador aleatorio por intento, que haría imposible reconstruir a mano qué cobro pertenece a qué aceptación al depurar |

## 8. Lo que queda fuera

- **Los eventos `ChargeCompleted` y `PolicyIssued` en SNS con *outbox*** siguen pendientes, como ya anotaba el contrato
  de HA-D02.
- **Guardar el medio de pago para cobros futuros** (renovaciones) no es de esta historia: el token se usa una vez y no
  se reutiliza.
- **El despliegue del *stub* en AWS:** desplegado en staging y en la plataforma (fase AWS de HA-D02); la tokenización pública
  se publica con T5 como `POST /pasarela/tokens` del API Gateway (sin sesión ni API key, 5 req/s), en el dominio del portal web.
