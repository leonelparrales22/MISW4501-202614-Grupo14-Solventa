// Generador de carga del experimento EXP-D03.
//
// Modelo abierto (open model, constant-arrival-rate): emite RPS solicitudes por segundo
// sin importar cuánto tarde el sistema en responder, para que la ventana de
// error se cuente completa. Cada solicitud registra una muestra de la métrica
// `oferta_ms` con etiquetas suficientes para el análisis: identificador de la
// solicitud, instancia que respondió, código de estado y resultado.

import http from 'k6/http';
import { Trend, Counter } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const RUN_ID = __ENV.RUN_ID || 'prueba';
const CONFIG = __ENV.CONFIG || 'local';
const API_KEY = __ENV.API_KEY || '';
const RPS = Number(__ENV.RPS || 50);
const DURACION = __ENV.DURACION || '10m';

// Perfil de carga: `constante` (matriz) o `rampa` (ejecuciones de carga alta:
// escalones de 2 minutos hasta RPS_MAX, sin inyección de fallas).
const PERFIL = __ENV.PERFIL || 'constante';
const RPS_MAX = Number(__ENV.RPS_MAX || 450);

function escenario() {
  if (PERFIL === 'rampa') {
    const escalon = Math.round(RPS_MAX / 4);
    return {
      executor: 'ramping-arrival-rate',
      startRate: RPS,
      timeUnit: '1s',
      preAllocatedVUs: 300,
      maxVUs: 900,
      gracefulStop: '6s',
      stages: [
        { target: RPS, duration: '1m' },
        { target: escalon, duration: '10s' }, { target: escalon, duration: '110s' },
        { target: escalon * 2, duration: '10s' }, { target: escalon * 2, duration: '110s' },
        { target: escalon * 3, duration: '10s' }, { target: escalon * 3, duration: '110s' },
        { target: RPS_MAX, duration: '10s' }, { target: RPS_MAX, duration: '110s' },
        { target: RPS, duration: '10s' }, { target: RPS, duration: '50s' },
      ],
    };
  }
  return {
    executor: 'constant-arrival-rate',
    rate: RPS,
    timeUnit: '1s',
    duration: DURACION,
    preAllocatedVUs: 300,
    maxVUs: 600,
    gracefulStop: '6s',
  };
}

export const options = {
  scenarios: { carga: escenario() },
  systemTags: ['status', 'method', 'name', 'scenario', 'error_code', 'expected_response'],
  summaryTrendStats: ['avg', 'p(95)', 'p(99)', 'max'],
};

const ofertaMs = new Trend('oferta_ms', true);
const ofertaTotal = new Counter('oferta_total');

function uuid() {
  const hex = '0123456789abcdef';
  let s = '';
  for (let i = 0; i < 36; i++) {
    if (i === 8 || i === 13 || i === 18 || i === 23) {
      s += '-';
    } else if (i === 14) {
      s += '4';
    } else if (i === 19) {
      s += hex[(Math.random() * 4) | 8];
    } else {
      s += hex[(Math.random() * 16) | 0];
    }
  }
  return s;
}

export default function () {
  const id = uuid();
  const clienteId = 'cli-' + Math.floor(Math.random() * 100000);
  const cuerpo = JSON.stringify({ id: id, cliente_id: clienteId, run_id: RUN_ID });
  const cabeceras = { 'Content-Type': 'application/json' };
  if (API_KEY) {
    cabeceras['x-api-key'] = API_KEY;
  }

  const inicio = Date.now();
  const res = http.post(`${BASE_URL}/oferta`, cuerpo, {
    headers: cabeceras,
    timeout: '5s',
    tags: { name: 'oferta' },
  });
  const transcurrido = Date.now() - inicio;

  let instancia = '';
  if (res.status === 201) {
    try {
      instancia = res.json('instancia') || '';
    } catch (e) {
      instancia = '';
    }
  } else if (res.headers && res.headers['X-Instancia']) {
    instancia = res.headers['X-Instancia'];
  }

  const etiquetas = {
    rid: id,
    instancia: instancia,
    status: String(res.status),
    ok: res.status === 201 ? '1' : '0',
    error: res.error_code ? String(res.error_code) : '0',
    run_id: RUN_ID,
    config: CONFIG,
  };
  ofertaMs.add(transcurrido, etiquetas);
  ofertaTotal.add(1, etiquetas);
}
