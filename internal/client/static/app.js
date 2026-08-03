const presets = {
  health: { method: 'GET', path: '/health', headers: {}, body: '' },
  metrics: { method: 'GET', path: '/metrics', headers: {}, body: '' },
  create: {
    method: 'POST', path: '/nsmf-pdusession/v1/sm-contexts',
    headers: { 'Content-Type': 'application/json', 'X-Request-ID': 'ui-demo-001' },
    body: {
      supi: 'imsi-452040000000001', gpsi: 'msisdn-84900000001', pduSessionId: 1,
      dnn: 'v-internet', sNssai: { sst: 1, sd: '000001' },
      servingNfId: 'ui-amf-001', anType: '3GPP_ACCESS'
    }
  },
  invalid: {
    method: 'POST', path: '/nsmf-pdusession/v1/sm-contexts',
    headers: { 'Content-Type': 'application/json' },
    body: { supi: '', pduSessionId: 0, dnn: '', sNssai: { sst: 0, sd: '' } }
  },
  method: { method: 'GET', path: '/nsmf-pdusession/v1/sm-contexts', headers: {}, body: '' },
  missing: { method: 'GET', path: '/not-found', headers: {}, body: '' }
};

const $ = (id) => document.getElementById(id);
const state = { history: [], requestNumber: 0 };

document.addEventListener('DOMContentLoaded', async () => {
  bindEvents();
  applyPreset('create');
  try {
    const config = await fetch('/api/config').then(response => response.json());
    $('gateway-target').textContent = config.gatewayUrl;
  } catch (_) {
    $('gateway-target').textContent = 'bridge unavailable';
  }
});

function bindEvents() {
  document.querySelectorAll('[data-preset]').forEach(button => button.addEventListener('click', () => applyPreset(button.dataset.preset)));
  $('send').addEventListener('click', () => sendCurrentRequest());
  $('run-scenario').addEventListener('click', runScenario);
  document.addEventListener('keydown', event => {
    if (event.ctrlKey && event.key === 'Enter') sendCurrentRequest();
  });
  document.querySelectorAll('.tab').forEach(button => button.addEventListener('click', () => selectTab(button)));
}

function applyPreset(name) {
  const preset = presets[name];
  if (!preset) return;
  $('method').value = preset.method;
  $('path').value = preset.path;
  $('headers').value = JSON.stringify(preset.headers, null, 2);
  $('body').value = preset.body ? JSON.stringify(preset.body, null, 2) : '';
  document.querySelectorAll('[data-preset]').forEach(button => button.classList.toggle('active', button.dataset.preset === name));
  hideError();
}

async function sendCurrentRequest({ quiet = false, overrideBody = null } = {}) {
  let headers;
  try {
    headers = JSON.parse($('headers').value || '{}');
    if (!headers || Array.isArray(headers) || typeof headers !== 'object') throw new Error('Headers phải là JSON object.');
  } catch (error) {
    showError(`Headers JSON không hợp lệ: ${error.message}`);
    return null;
  }
  const request = {
    method: $('method').value,
    path: $('path').value.trim(),
    headers,
    body: overrideBody === null ? $('body').value : JSON.stringify(overrideBody)
  };
  hideError();
  if (!quiet) setBusy(true);
  try {
    const bridgeResponse = await fetch('/api/request', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(request)
    });
    const result = await bridgeResponse.json();
    if (!bridgeResponse.ok) throw new Error(result.error || `Bridge HTTP ${bridgeResponse.status}`);
    renderResponse(result);
    addHistory(request, result);
    return result;
  } catch (error) {
    showError(error.message);
    renderBridgeError(error.message);
    return null;
  } finally {
    if (!quiet) setBusy(false);
  }
}

async function runScenario() {
  const count = Math.max(1, Math.min(100, Number($('scenario-count').value) || 6));
  const button = $('run-scenario');
  button.disabled = true;
  button.textContent = `Running 0/${count}`;
  applyPreset('create');
  const distribution = new Map();
  const sequence = [];
  for (let index = 0; index < count; index++) {
    const suffix = Date.now().toString().slice(-7) + String(index).padStart(2, '0');
    const payload = { ...presets.create.body, supi: `imsi-45204${suffix}`, gpsi: `msisdn-84${suffix}`, pduSessionId: index + 1, servingNfId: `ui-scenario-${suffix}` };
    const result = await sendCurrentRequest({ quiet: true, overrideBody: payload });
    button.textContent = `Running ${index + 1}/${count}`;
    if (!result) break;
    const parsed = parseBody(result.body);
    const node = parsed && parsed.handledBy ? parsed.handledBy : `HTTP ${result.statusCode}`;
    distribution.set(node, (distribution.get(node) || 0) + 1);
    sequence.push(node);
    renderDistribution(distribution, sequence, count);
  }
  button.disabled = false;
  button.textContent = 'Run scenario';
}

function renderDistribution(distribution, sequence, total) {
  const max = Math.max(...distribution.values(), 1);
  $('distribution').classList.remove('empty');
  $('distribution').innerHTML = [...distribution.entries()].map(([node, value]) =>
    `<div class="bar" style="--height:${Math.max(15, value / max * 100)}%"><strong title="${escapeHTML(node)}">${escapeHTML(shortNode(node))}</strong><small>${value} / ${total} requests</small></div>`
  ).join('');
  $('sequence').textContent = sequence.join(' → ');
}

function renderResponse(result) {
  const status = $('status');
  status.textContent = `${result.statusCode} ${result.status.replace(/^\d+\s*/, '')}`;
  status.className = `status ${result.statusCode < 400 ? 'success' : result.statusCode < 500 ? 'client-error' : 'server-error'}`;
  $('protocol').textContent = result.protocol;
  $('latency').textContent = `${Number(result.latencyMs).toFixed(2)} ms`;
  const parsed = parseBody(result.body);
  $('response-body').textContent = parsed ? JSON.stringify(parsed, null, 2) : result.body;
  $('response-headers').textContent = JSON.stringify(result.headers, null, 2);
}

function renderBridgeError(message) {
  $('status').textContent = 'Bridge error';
  $('status').className = 'status server-error';
  $('protocol').textContent = '—'; $('latency').textContent = '— ms';
  $('response-body').textContent = message;
}

function addHistory(request, result) {
  const parsed = parseBody(result.body) || {};
  state.history.unshift({ number: ++state.requestNumber, request: `${request.method} ${request.path}`, status: result.statusCode, node: parsed.handledBy || parsed.instanceId || '—', latency: Number(result.latencyMs).toFixed(2) });
  state.history = state.history.slice(0, 20);
  $('history').innerHTML = state.history.map(item => `<tr><td>${item.number}</td><td title="${escapeHTML(item.request)}">${escapeHTML(item.request)}</td><td>${item.status}</td><td title="${escapeHTML(item.node)}">${escapeHTML(shortNode(item.node))}</td><td>${item.latency} ms</td></tr>`).join('');
}

function selectTab(button) {
  document.querySelectorAll('.tab').forEach(tab => tab.classList.toggle('active', tab === button));
  $('response-body').classList.toggle('hidden', button.dataset.tab !== 'response-body');
  $('response-headers').classList.toggle('hidden', button.dataset.tab !== 'response-headers');
}

function parseBody(body) { try { return JSON.parse(body); } catch (_) { return null; } }
function shortNode(node) { return node.length > 14 ? `${node.slice(0, 12)}…` : node; }
function escapeHTML(value) { return String(value).replace(/[&<>'"]/g, char => ({ '&':'&amp;', '<':'&lt;', '>':'&gt;', "'":'&#39;', '"':'&quot;' })[char]); }
function showError(message) { $('request-error').textContent = message; $('request-error').classList.remove('hidden'); }
function hideError() { $('request-error').classList.add('hidden'); }
function setBusy(busy) { $('send').disabled = busy; $('send').innerHTML = busy ? 'Sending…' : 'Send request <kbd>Ctrl ↵</kbd>'; }
