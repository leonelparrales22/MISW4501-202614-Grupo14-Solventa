// k6/volumen.js genera un lote de aceptaciones (journey completo:
// crear + intentar procesar el cobro una vez) a una tasa sostenida
// moderada, para verificar a volumen -no solo con una aceptación aislada-
// que el 100% de las aceptaciones queda persistido y con un desenlace
// consistente (confirmado, o fallido/pendiente si la pasarela está
// caída), sin duplicados.
//
// Uso: docker compose run --rm k6 run /scripts/volumen.js
// Variables: VUS, DURATION (por defecto 5 VUs, 10s).
import http from "k6/http";
import { check, sleep } from "k6";
import { Rate } from "k6/metrics";

const aceptacionCreada = new Rate("aceptacion_creada");

export const options = {
  scenarios: {
    volumen: {
      executor: "constant-vus",
      vus: Number(__ENV.VUS || 5),
      duration: __ENV.DURATION || "10s",
    },
  },
  summaryTrendStats: ["avg", "min", "med", "max", "p(95)", "p(99)"],
};

const BASE_URL = __ENV.BASE_URL || "http://originacion:8080";

export default function () {
  const resCrear = http.post(
    `${BASE_URL}/aceptaciones`,
    JSON.stringify({ cliente_id: `cliente-vu${__VU}-iter${__ITER}`, monto_centavos: 15000 }),
    { headers: { "Content-Type": "application/json" } }
  );

  const creada = check(resCrear, { "aceptacion creada (201)": (r) => r.status === 201 });
  aceptacionCreada.add(creada);

  if (creada) {
    const aceptacionId = JSON.parse(resCrear.body).aceptacion_id;
    // Dispara un primer intento síncrono, igual que haría el journey real
    // al recibir la aceptación; el worker de reintentos se encarga del
    // resto si la pasarela no responde.
    http.post(`${BASE_URL}/procesar-cobro/${aceptacionId}`);
  }

  sleep(0.2);
}
