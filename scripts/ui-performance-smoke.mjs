// JavaScript contract smoke (DOM stub, not a browser layout test).
// Usage: node scripts/ui-performance-smoke.mjs <live performance-report.json>
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import vm from 'node:vm';

const root = new URL('../', import.meta.url);
const html = readFileSync(new URL('internal/client/static/index.html', root), 'utf8');
const source = readFileSync(new URL('internal/client/static/app.js', root), 'utf8');
const fixture = JSON.parse(readFileSync(process.argv[2] || new URL('docs/benchmarks/ui-client-2026-10-08/50k.json', root), 'utf8'));
assert.ok(fixture.measurements.some(row => row.latencyP90Millis > 0), 'fixture must contain measured p90');

function element(attributes = '') {
  const classes = new Set();
  return {
    value: attributes.match(/value="([^"]*)"/)?.[1] || '',
    min: attributes.match(/min="([^"]*)"/)?.[1], max: attributes.match(/max="([^"]*)"/)?.[1],
    textContent: '', innerHTML: '', disabled: false, required: false, events: {}, dataset: {},
    classList: { add: name => classes.add(name), remove: name => classes.delete(name), toggle(name, on) { if (on) classes.add(name); else classes.delete(name); } },
    addEventListener(name, callback) { this.events[name] = callback; },
    reportValidity() { const n = Number(this.value); return this.value !== '' && Number.isInteger(n) && n >= Number(this.min ?? -Infinity) && n <= Number(this.max ?? Infinity); },
    scrollIntoView() {}, click() {}, remove() {}
  };
}
const nodes = new Map([...html.matchAll(/<[^>]*\bid="([^"]+)"[^>]*>/g)].map(match => [match[1], element(match[0])]));
const inputs = [...nodes.entries()].filter(([id, node]) => id.startsWith('performance-') && node.min !== undefined).map(([, node]) => node);
const workloads = [...html.matchAll(/data-workload="([^"]+)"/g)].map(match => Object.assign(element(), { dataset: { workload: match[1] } }));
const routes = [...html.matchAll(/data-performance-route="([^"]+)"/g)].map(match => Object.assign(element(), { dataset: { performanceRoute: match[1] } }));
let ready;
let posted;
let downloaded;
const metadata = { ...fixture, measurements: [] };
const backendCount = fixture.healthyBackendsBefore;
const sandbox = {
  console, Blob, Date, Number, JSON, setTimeout: callback => callback(),
  URL: { createObjectURL(blob) { downloaded = blob; return 'blob:test'; }, revokeObjectURL() {} },
  navigator: { clipboard: { async writeText() {} } },
  document: {
    getElementById(id) { assert.ok(nodes.has(id), `missing HTML element ${id}`); return nodes.get(id); },
    addEventListener(name, callback) { if (name === 'DOMContentLoaded') ready = callback; },
    querySelectorAll(selector) {
      if (selector === '.performance-controls input') return inputs;
      if (selector === '.performance-controls input,[data-workload]') return [...inputs, ...workloads];
      if (selector === '[data-workload]') return workloads;
      if (selector === '[data-performance-route]') return routes;
      return [];
    },
    createElement: () => element(), body: { appendChild() {} }
  },
  async fetch(path, options = {}) {
    let payload;
    if (path === '/api/config') payload = { gatewayUrl: 'http://gateway:8080' };
    else if (path === '/api/request') payload = { statusCode: 200, body: JSON.stringify({ count: backendCount, routingMode: 'round_robin', instances: [{ instanceId: 'smoke-pdu', address: '127.0.0.1:8081', weight: 1, activeRequests: 0 }] }) };
    else if (options.method === 'POST') { posted = JSON.parse(options.body); payload = fixture; }
    else payload = metadata;
    return { ok: true, status: 200, async json() { return payload; } };
  }
};
const context = vm.createContext(sandbox);
vm.runInContext(source, context, { filename: fileURLToPath(new URL('internal/client/static/app.js', root)) });
await ready();

for (const [preset, requests, minimum, connections, streams] of [['40k', 40000, 35000, 3, 64], ['15k', 15000, 12000, 15, 32], ['50k', 50000, 35000, 3, 64]]) {
  context.applyWorkloadPreset(preset);
  const config = context.performanceConfig();
  assert.equal(config.targetRequests, requests);
  assert.equal(config.targetSuccessfulRequests, minimum);
  assert.equal(config.connections, connections);
  assert.equal(config.streamsPerConnection, streams);
}
await nodes.get('run-performance').events.click();
assert.deepEqual(posted, { runs: 3, warmupSeconds: 0, durationSeconds: 1, connections: 3, streamsPerConnection: 64, requestTimeoutSeconds: 3, targetRequests: 50000, targetSuccessfulRequests: 35000, pauseSeconds: 2 });
const rows = nodes.get('performance-rows').innerHTML;
assert.equal((rows.match(/<td[ >]/g) || []).length, fixture.measurements.length * 10);
const first = fixture.measurements[0];
assert.ok(rows.includes(Number(first.latencyP90Millis).toLocaleString('vi-VN', { minimumFractionDigits: 3, maximumFractionDigits: 3 }) + ' ms'));
assert.ok(rows.includes('ĐẠT'));
assert.ok(rows.includes(first.targetMet ? 'PASS' : 'FAIL'));
assert.equal(nodes.get('run-performance').disabled, false);
assert.equal(nodes.get('download-performance').disabled, false);
assert.ok(nodes.get('performance-topology').textContent.includes(`${fixture.healthyBackendsBefore} → ${fixture.healthyBackendsAfter}`));

context.downloadPerformance();
assert.deepEqual(JSON.parse(await downloaded.text()), fixture);
console.log('UI performance JS smoke passed: presets, request payload, p90, counts, independent statuses, topology, JSON export. No browser layout test was performed.');
