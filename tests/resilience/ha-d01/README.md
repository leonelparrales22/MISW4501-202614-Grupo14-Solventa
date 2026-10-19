# HA-D01 — Oferta continua ante caída de Open Finance

Comprueba, con el servicio de originación real, que mientras Open Finance está caído o responde por encima del corte de 700 ms **el 100 % de las solicitudes de oferta recibe respuesta**, normal o provisional, y que el circuito se recupera solo.

## Qué levanta

`docker-compose.yml`: Redis, el stub de Open Finance (`stubs/openfinance`), Toxiproxy delante del stub y el servicio `origination` apuntando a Toxiproxy. Toxiproxy es quien inyecta la caída y la latencia; el stub solo sirve perfiles.

## Cómo correrlo

```bash
cd tests/resilience/ha-d01
./run.sh
```

Requiere Docker con `docker compose`. El script levanta el ambiente, ejecuta las fases y lo destruye al terminar.

| Fase | Falla inyectada | Resultado esperado (k6 `outage.js`) |
|---|---|---|
| 1. Línea base | ninguna | 100 % `mode=normal` (`open_finance`, o `cache` si el cliente ya tiene perfil completo); calienta la caché de los clientes `warm-*` |
| 2. Caída total | proxy deshabilitado | 100 % HTTP 200: `warm-*` reutilizan su perfil completo (`normal`, `cache`, con `profile_date`) sin consultar; clientes nuevos `provisional` con `base_actuarial` (HU-W1.2-02) |
| 3. Recuperación | proxy restablecido | tras la ventana de 5 s y 3 sondas, 100 % `normal` |
| 4. Latencia | 900 ms (> 700 ms) | igual que la fase 2 |
| 5. Recuperación | restablecido | igual que la fase 3 |

Los umbrales de k6 son `http_req_failed == 0` y `checks == 1`; si alguna solicitud falla o llega un modo inesperado, el script termina con error. Al final imprime las transiciones `closed → open → half-open → closed` del circuito, tomadas de los logs de originación.

## Control manual

```bash
./toxiproxy.sh down      # caída total
./toxiproxy.sh latency   # 900 ms de latencia
./toxiproxy.sh up        # restablecer
```

Con el ambiente arriba (`docker compose up -d --build --wait origination`) la API queda en `http://localhost:8180`.

## Trazabilidad

HA-D01 · EC-D01 (experimento previo en la rama `Experimento1_circuitBreakerCache`) · Estrategia de pruebas: inyección de fallas con Toxiproxy y carga con k6.
