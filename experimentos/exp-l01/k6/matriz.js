// Guion de carga de EXP-L01. Cada vez que se corre es una combinación de la matriz.
//
// Variables de entorno:
//   BASE_URL   http://orquestador:8080
//   N_FUENTES  solo para etiquetar (el orquestador ya viene configurado con N)
//   CACHE_HIT  0..1, qué fracción de solicitudes repite un cliente que ya se perfiló
//   RPS_MAX    tasa a la que se sostiene la carga (83 ≈ 5.000 cotizaciones/min)
//
// Métricas que agregamos nosotros:
//   tramo_cache / tramo_fanout / tramo_agg / tramo_rating   ms de cada etapa, sacados de Server-Timing
//   perfiles_parciales                                      tasa 0..1
//   desde_cache                                             tasa 0..1, cuántas salieron de Redis
//
// Al final imprime ESTADO_FINAL con lo que devuelve /debug/estado. Ahí es donde
// vemos si quedaron goroutines o conexiones colgadas (hipótesis b).

import http from 'k6/http';
import { check, sleep } from 'k6';
import { Trend, Rate } from 'k6/metrics';

const BASE      = __ENV.BASE_URL  || 'http://localhost:8080';
const N         = __ENV.N_FUENTES || '?';
const CACHE_HIT = parseFloat(__ENV.CACHE_HIT || '0');
const RPS_MAX   = parseInt(__ENV.RPS_MAX || '83', 10);

const tramoCache  = new Trend('tramo_cache',  true);
const tramoFanout = new Trend('tramo_fanout', true);
const tramoAgg    = new Trend('tramo_agg',    true);
const tramoRating = new Trend('tramo_rating', true);
const parciales   = new Rate('perfiles_parciales');
const desdeCache  = new Rate('desde_cache');

export const options = {
  scenarios: {
    rampa: {
      executor: 'ramping-arrival-rate',
      startRate: 8,          // ≈ 500 cotizaciones/min
      timeUnit: '1s',
      preAllocatedVUs: 100,
      maxVUs: 400,
      stages: [
        { duration: '1m', target: RPS_MAX },   // subir
        { duration: '3m', target: RPS_MAX },   // sostener: estos 3 min son los que cuentan
      ],
    },
  },
  // El criterio de aceptación de la ficha (enunciado §6.1, perfilamiento en línea).
  // Si no se cumple, k6 termina con código distinto de 0.
  thresholds: {
    http_req_duration: ['p(95)<400', 'p(99)<800'],
    http_req_failed:   ['rate<0.01'],
  },
  summaryTrendStats: ['avg', 'min', 'med', 'p(90)', 'p(95)', 'p(99)', 'max'],
  tags: { experimento: 'EXP-L01', n_fuentes: String(N), cache_hit: String(CACHE_HIT) },
};

// Tenemos un grupo de 200 clientes "calientes". Cuando CACHE_HIT > 0, esa
// fracción de solicitudes usa uno de esos ids y debería encontrarlo en Redis.
// Los demás son ids nuevos que nunca van a estar en caché.
const POOL = 200;
const CABECERAS = { headers: { 'Content-Type': 'application/json' } };

function clienteID() {
  if (CACHE_HIT > 0 && Math.random() < CACHE_HIT) {
    return `caliente-${Math.floor(Math.random() * POOL)}`;
  }
  return `frio-${__VU}-${__ITER}-${Date.now()}`;
}

export function setup() {
  // Antes de arrancar metemos los 200 clientes calientes en caché. Si no, los
  // primeros segundos serían puros fallos de caché y la tasa medida no sería la que pedimos.
  if (CACHE_HIT > 0) {
    for (let i = 0; i < POOL; i++) {
      http.post(`${BASE}/cotizaciones`, JSON.stringify({ cliente_id: `caliente-${i}` }), CABECERAS);
    }
  }
  const estado = http.get(`${BASE}/debug/estado`);
  console.log(`ESTADO_INICIAL ${estado.body}`);
  return { inicio: Date.now() };
}

export default function () {
  const res = http.post(`${BASE}/cotizaciones`, JSON.stringify({ cliente_id: clienteID() }), CABECERAS);

  check(res, {
    'HTTP 200': (r) => r.status === 200,
    'tiene Server-Timing': (r) => !!r.headers['Server-Timing'],
  });

  // La cabecera viene así: "cache;dur=1.20, fanout;dur=118.40, agg;dur=0.05, rating;dur=0.01"
  // La partimos y cada pedazo va a su métrica.
  const st = res.headers['Server-Timing'] || '';
  for (const parte of st.split(',')) {
    const m = parte.trim().match(/^(\w+);dur=([\d.]+)/);
    if (!m) continue;
    const v = parseFloat(m[2]);
    switch (m[1]) {
      case 'cache':  tramoCache.add(v);  break;
      case 'fanout': tramoFanout.add(v); break;
      case 'agg':    tramoAgg.add(v);    break;
      case 'rating': tramoRating.add(v); break;
    }
  }

  if (res.status === 200) {
    try {
      const b = res.json();
      parciales.add(b.perfil_parcial ? 1 : 0);
      desdeCache.add(b.desde_cache ? 1 : 0);
    } catch (_) { /* si el cuerpo no es JSON lo dejamos pasar */ }
  }
}

export function teardown() {
  // Esperamos un par de segundos para que las cancelaciones que quedaron en
  // vuelo terminen. Si medimos de una, contaríamos cosas que ya van de salida.
  sleep(2);
  const estado = http.get(`${BASE}/debug/estado`);
  console.log(`ESTADO_FINAL ${estado.body}`);
}

// En AWS no hay carpeta compartida para --summary-export, así que el resumen
// se imprime por consola y CloudWatch lo captura. En local sigue saliendo el
// JSON a resultados/ igual que antes; esto es adicional.
export function handleSummary(data) {
  const d = data.metrics.http_req_duration ? data.metrics.http_req_duration.values : {};
  const p = data.metrics.perfiles_parciales ? data.metrics.perfiles_parciales.values : {};
  const e = data.metrics.http_req_failed ? data.metrics.http_req_failed.values : {};
  console.log(
    `RESUMEN_CORTO p50=${(d.med || 0).toFixed(1)}ms p95=${(d['p(95)'] || 0).toFixed(1)}ms ` +
    `p99=${(d['p(99)'] || 0).toFixed(1)}ms max=${(d.max || 0).toFixed(0)}ms ` +
    `parciales=${((p.rate || 0) * 100).toFixed(1)}% errores=${((e.rate || 0) * 100).toFixed(2)}%`
  );
  return {
    stdout: 'RESUMEN_JSON ' + JSON.stringify(data) + '\n',
  };
}
