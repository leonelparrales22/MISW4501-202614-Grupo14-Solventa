// Unit tests of the viewer function of the web portal (T5). CloudFront Functions
// have no module system, so the file is evaluated in a sandbox and its handler
// is called with the event shape of a viewer request.
//
//   node --test infra/terraform/modules/web-portal/functions/spa-rewrite.test.mjs

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

const source = readFileSync(fileURLToPath(new URL('./spa-rewrite.js', import.meta.url)), 'utf8');
const handler = vm.runInNewContext(`${source}\nhandler;`, {});

function rewrite(uri) {
  return handler({ request: { uri, method: 'GET', headers: {}, querystring: {}, cookies: {} } }).uri;
}

test('the root and the routes of the SPA get index.html', () => {
  for (const uri of [
    '/',
    '/embebido/oferta/demo-active-1',
    '/embebido/perfil-riesgo/cliente-1',
    '/originacion/bandeja',
    '/originacion/bandeja/',
    '/acceso',
  ]) {
    assert.equal(rewrite(uri), '/index.html', uri);
  }
});

test('files pass through unchanged', () => {
  for (const uri of ['/main-ABC.js', '/config.json', '/i18n/es-CO.json', '/favicon.ico', '/styles-XYZ.css', '/index.html']) {
    assert.equal(rewrite(uri), uri, uri);
  }
});

test('the rest of the request is kept', () => {
  const request = { uri: '/originacion/bandeja', method: 'GET', headers: { host: { value: 'd1.cloudfront.net' } }, querystring: { a: { value: '1' } }, cookies: {} };
  const result = handler({ request });
  assert.equal(result.method, 'GET');
  assert.deepEqual(result.headers, { host: { value: 'd1.cloudfront.net' } });
  assert.deepEqual(result.querystring, { a: { value: '1' } });
});
