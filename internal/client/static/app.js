const presets = {
  health: { method: 'GET', path: '/health', headers: {}, body: '' },
  metrics: { method: 'GET', path: '/metrics', headers: {}, body: '' },
  backends: { method: 'GET', path: '/gateway/backends', headers: {}, body: '' },
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
const state = { history: [], requestNumber: 0, routingMode: '', performanceRoute: '/api/performance/round-robin' };

document.addEventListener('DOMContentLoaded', async () => {
  bindEvents();
  applyPreset('create');
  try {
    const config = await fetch('/api/config').then(response => response.json());
    $('gateway-target').textContent = config.gatewayUrl;
  } catch (_) {
    $('gateway-target').textContent = 'bridge unavailable';
  }
  await Promise.all([
    refreshBackends(),
    loadPerformance('/api/performance/round-robin', false)
  ]);
});

function bindEvents() {
  document.querySelectorAll('[data-preset]').forEach(button => button.addEventListener('click', () => applyPreset(button.dataset.preset)));
  $('send').addEventListener('click', () => sendCurrentRequest());
  $('run-scenario').addEventListener('click', runScenario);
  $('refresh-backends').addEventListener('click', refreshBackends);
  $('run-performance').addEventListener('click', runPerformance);
  document.querySelectorAll('[data-performance-route]').forEach(button => button.addEventListener('click', () => loadPerformance(button.dataset.performanceRoute)));
  document.addEventListener('keydown', event => {
    if (event.ctrlKey && event.key === 'Enter') sendCurrentRequest();
  });
  document.querySelectorAll('.tab').forEach(button => button.addEventListener('click', () => selectTab(button)));
}

async function loadPerformance(route, scrollToReport = true) {
  const errorBox = $('performance-error');
  errorBox.classList.add('hidden');
  document.querySelectorAll('[data-performance-route]').forEach(button => button.classList.toggle('active', button.dataset.performanceRoute === route));
  try {
    const response = await fetch(route, { headers: { 'Accept': 'application/json' } });
    const report = await response.json();
    if (!response.ok) throw new Error(report.error || `Performance API HTTP ${response.status}`);
    state.performanceRoute = route;
    renderPerformance(report);
    if (scrollToReport) $('performance-report').scrollIntoView({ behavior: 'smooth', block: 'start' });
  } catch (error) {
    $('performance-rows').innerHTML = '<tr><td colspan="6" class="muted">Không đọc được kết quả performance.</td></tr>';
    errorBox.textContent = error.message;
    errorBox.classList.remove('hidden');
  }
}

function renderPerformance(report) {
  $('performance-title').textContent = report.displayName;
  $('performance-route').textContent = report.route;
  const environment = report.environment;
  $('performance-environment').textContent = `${environment.protocol} · gửi ${environment.targetRequests} requests/${environment.durationSeconds}s · cần ≥ ${environment.targetSuccessfulRequests} HTTP 201 · ${environment.connections} connections · ${environment.streamsPerConnection} streams/connection · ${environment.concurrentStreams} concurrent streams`;
  
  if (report.observedResources) {
    if ($('diag-cpu')) $('diag-cpu').textContent = report.observedResources.gatewayCpuRange || 'Chưa đo';
    if ($('diag-ram')) $('diag-ram').textContent = report.observedResources.gatewayRamPeak || 'Chưa đo / 1 GiB';
    if ($('diag-error')) $('diag-error').textContent = report.observedResources.errorAnalysis || 'Chưa đo';
  }

  if (!report.measurements.length) {
    $('performance-rows').innerHTML = '<tr><td colspan="6" class="muted">Chưa có kết quả. Nhấn Run E2E test để bắt đầu đo thật.</td></tr>';
    $('performance-measured-at').textContent = 'Chưa chạy bài đo trong phiên UI này.';
  } else {
    $('performance-rows').innerHTML = report.measurements.map(measurement => {
      const errorNote = measurement.failedReason
        ? `<span class="error-tag" title="${escapeHTML(measurement.failedReason)}">target missed</span>`
        : (measurement.failedRequests > 0 ? `<span class="error-tag" title="Target vẫn đạt; các request còn lại chưa hoàn tất trong cửa sổ một giây">within allowance</span>` : '');
      return `
    <tr class="${measurement.average ? 'average-row' : ''}">
      <td>${escapeHTML(measurement.run)}</td>
      <td>${formatNumber(measurement.successfulTps, 2)}</td>
      <td>${formatNumber(measurement.latencyP50Millis, 3)} ms</td>
      <td>${formatNumber(measurement.latencyP95Millis, 3)} ms</td>
      <td>${formatNumber(measurement.latencyP99Millis, 3)} ms</td>
      <td>${formatNumber(measurement.sentRequests, 0)} / ${formatNumber(measurement.failedRequests, 0)} ${errorNote}</td>
    </tr>
    `;
    }).join('');
    $('performance-measured-at').textContent = `Đo xong lúc ${new Date(report.measuredAt).toLocaleString('vi-VN')}.`;
  }
  const labels = {
    run: 'Lần đo', successfulTps: 'TPS thành công', latencyP50Millis: 'p50',
    latencyP95Millis: 'p95', latencyP99Millis: 'p99', sentRequests: 'Request đã gửi', failedRequests: 'Request lỗi'
  };
  $('performance-fields').innerHTML = Object.entries(report.fields).map(([field, explanation]) => `
    <div><strong>${escapeHTML(labels[field] || field)}</strong><span>${escapeHTML(explanation)}</span></div>
  `).join('');
}

async function runPerformance() {
  const route = state.performanceRoute;
  const expectedMode = {
    '/api/performance/round-robin': 'round_robin',
    '/api/performance/weighted': 'weighted',
    '/api/performance/load': 'load'
  }[route];
  const errorBox = $('performance-error');
  errorBox.classList.add('hidden');
  if (state.routingMode && state.routingMode !== expectedMode) {
    errorBox.textContent = `Route này cần Gateway mode ${expectedMode}, nhưng Gateway đang chạy ${state.routingMode}.`;
    errorBox.classList.remove('hidden');
    return;
  }
  const config = {
    runs: Number($('performance-runs').value),
    warmupSeconds: Number($('performance-warmup').value),
    durationSeconds: Number($('performance-duration').value),
    connections: Number($('performance-connections').value),
    streamsPerConnection: Number($('performance-streams').value),
    requestTimeoutSeconds: Number($('performance-timeout').value)
  };
  const estimatedSeconds = config.warmupSeconds + config.runs * config.durationSeconds;
  const button = $('run-performance');
  const routeButtons = document.querySelectorAll('[data-performance-route]');
  button.disabled = true;
  routeButtons.forEach(routeButton => { routeButton.disabled = true; });
  button.textContent = `Running… khoảng ${estimatedSeconds}s`;
  $('performance-rows').innerHTML = '<tr><td colspan="6" class="muted">Đang gửi tải thật đến Gateway, vui lòng chờ...</td></tr>';
  $('performance-measured-at').textContent = `Đang chạy ${config.runs} lần đo qua HTTP/2 h2c.`;
  try {
    const response = await fetch(route, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'Accept': 'application/json' },
      body: JSON.stringify(config)
    });
    const report = await response.json();
    if (!response.ok) throw new Error(report.error || `Performance API HTTP ${response.status}`);
    renderPerformance(report);
  } catch (error) {
    $('performance-rows').innerHTML = '<tr><td colspan="6" class="muted">Bài đo không hoàn thành.</td></tr>';
    $('performance-measured-at').textContent = 'Không có kết quả mới.';
    errorBox.textContent = error.message;
    errorBox.classList.remove('hidden');
  } finally {
    button.disabled = false;
    routeButtons.forEach(routeButton => { routeButton.disabled = false; });
    button.textContent = 'Run E2E test';
  }
}

function formatNumber(value, digits) {
  return Number(value).toLocaleString('vi-VN', { minimumFractionDigits: digits, maximumFractionDigits: digits });
}

async function refreshBackends() {
  const button = $('refresh-backends');
  button.disabled = true;
  button.textContent = 'Refreshing...';
  $('backend-error').classList.add('hidden');
  try {
    const bridgeResponse = await fetch('/api/request', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ method: 'GET', path: '/gateway/backends', headers: {}, body: '' })
    });
    const result = await bridgeResponse.json();
    if (!bridgeResponse.ok) throw new Error(result.error || `Bridge HTTP ${bridgeResponse.status}`);
    if (result.statusCode !== 200) throw new Error(`Gateway HTTP ${result.statusCode}`);
    const payload = parseBody(result.body);
    if (!payload || !Array.isArray(payload.instances)) throw new Error('Gateway trả về danh sách backend không hợp lệ.');
    renderBackends(payload);
  } catch (error) {
    $('backend-count').textContent = '0';
    $('backend-list').innerHTML = '<div class="backend-empty">Không đọc được danh sách backend.</div>';
    $('backend-error').textContent = error.message;
    $('backend-error').classList.remove('hidden');
  } finally {
    button.disabled = false;
    button.textContent = 'Refresh backends';
  }
}

function renderBackends(payload) {
  state.routingMode = payload.routingMode || '';
  $('performance-gateway-mode').textContent = state.routingMode || 'không xác định';
  $('backend-count').textContent = String(payload.count);
  if (payload.instances.length === 0) {
    $('backend-list').innerHTML = '<div class="backend-empty">Gateway chưa có backend healthy.</div>';
    return;
  }
  $('backend-list').innerHTML = payload.instances.map(instance => `
    <article class="backend-card">
      <div><span class="backend-dot"></span><strong>${escapeHTML(instance.instanceId)}</strong></div>
      <code>${escapeHTML(instance.address)}</code>
      <small>weight <b>${instance.weight}</b> · active requests <b>${instance.activeRequests}</b></small>
    </article>
  `).join('');
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
