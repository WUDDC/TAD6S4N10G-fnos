const $ = (id) => document.getElementById(id);
const DEFAULT_CURVE = [
  { temp_c: 40, pwm_percent: 60 },
  { temp_c: 55, pwm_percent: 70 },
  { temp_c: 70, pwm_percent: 85 },
  { temp_c: 80, pwm_percent: 100 },
];
const GPIO_ACTIONS = [
  ['none', '无动作'],
  ['log', '仅记录日志'],
  ['refresh_storage', '刷新硬盘仓位'],
  ['smart_check', '刷新仓位并检查 SMART'],
  ['reapply_plugin', '重新应用插件配置'],
];
const CURVE_MIN_POINTS = 2;
const CURVE_MAX_POINTS = 8;
const CHART = { left: 52, right: 616, top: 20, bottom: 222 };
let currentStatus = null;
let editableCurve = DEFAULT_CURVE.map((point) => ({ ...point }));
let selectedCurveIndex = 0;
let draggedCurveIndex = -1;
let uiBusy = false;

function baseUrl(path) {
  const base = window.location.pathname.endsWith('/') ? window.location.pathname : `${window.location.pathname}/`;
  return new URL(path, `${window.location.origin}${base}`).toString();
}

async function request(path, options = {}) {
  const response = await fetch(baseUrl(path), {
    cache: 'no-store',
    headers: { 'Content-Type': 'application/json', ...(options.headers || {}) },
    ...options,
  });
  const body = await response.json().catch(() => ({}));
  if (!response.ok) throw new Error(body.error || `HTTP ${response.status}`);
  return body;
}

function formatTemperature(value, available = true) {
  const number = Number(value);
  return available && Number.isFinite(number) ? `${number.toFixed(1)} °C` : '不可用';
}

function formatSize(bytes) {
  let value = Number(bytes);
  if (!Number.isFinite(value) || value <= 0) return '';
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value.toFixed(unit < 3 ? 0 : 1)} ${units[unit]}`;
}

function renderStorageTable(storage = {}) {
  const stateLabels = {
    empty: '空置', present: '已插入', used: '已使用', warning: '告警', unknown: '未知',
  };
  const slots = [...(storage.slots || [])].sort((left, right) => {
    if (left.kind !== right.kind) return left.kind === 'front' ? -1 : 1;
    return left.kind === 'front' ? right.slot - left.slot : left.slot - right.slot;
  });
  const body = $('storage-body');
  body.replaceChildren();
  slots.forEach((slot) => {
    const row = document.createElement('tr');
    row.className = `storage-${slot.state || 'unknown'}`;
    const label = slot.kind === 'front' ? `前置 ${slot.slot}` : `M.2 ${slot.slot}`;
    const deviceDetail = [slot.device, slot.model, slot.serial, formatSize(slot.size_bytes)].filter(Boolean).join(' · ');
    const values = [
      label,
      stateLabels[slot.state] || slot.state || '未知',
      deviceDetail || '—',
      slot.purpose || (slot.state === 'empty' ? '空仓位' : '—'),
      slot.warning || slot.health || '—',
      formatTemperature(slot.temperature_c, Number(slot.temperature_c) > 0),
    ];
    values.forEach((value, index) => {
      const cell = document.createElement(index === 0 ? 'th' : 'td');
      cell.textContent = value;
      row.append(cell);
    });
    body.append(row);
  });
  if (!slots.length) {
    const row = document.createElement('tr');
    const cell = document.createElement('td');
    cell.colSpan = 6;
    cell.textContent = '尚未获得仓位信息';
    row.append(cell);
    body.append(row);
  }
  $('storage-updated').textContent = storage.updated_at
    ? `更新于 ${new Date(storage.updated_at).toLocaleTimeString()}` : '等待刷新';
  $('storage-error').textContent = storage.last_error || '';
  $('storage-error').className = `inline-status${storage.last_error ? ' error' : ''}`;
}

function setupGPIOActions() {
  document.querySelectorAll('.gpio-action').forEach((select) => {
    select.replaceChildren();
    GPIO_ACTIONS.forEach(([value, label]) => {
      const option = document.createElement('option');
      option.value = value;
      option.textContent = label;
      select.append(option);
    });
  });
}

function updateGPIOEnabledState() {
  const enabled = $('gpio-enabled').checked;
  document.querySelectorAll('.gpio-action').forEach((select) => { select.disabled = !enabled; });
}

function fillGPIOInputs(gpio = {}) {
  $('gpio-enabled').checked = Boolean(gpio.enabled);
  const buttons = new Map((gpio.buttons || []).map((button) => [button.id, button]));
  document.querySelectorAll('.gpio-table tbody tr').forEach((row) => {
    const actions = buttons.get(row.dataset.button)?.actions || {};
    row.querySelectorAll('.gpio-action').forEach((select) => {
      select.value = actions[select.dataset.stage] || 'none';
    });
  });
  updateGPIOEnabledState();
}

function gpioConfigFromInputs() {
  return {
    version: 1,
    enabled: $('gpio-enabled').checked,
    buttons: [...document.querySelectorAll('.gpio-table tbody tr')].map((row) => {
      const actions = {};
      row.querySelectorAll('.gpio-action').forEach((select) => { actions[select.dataset.stage] = select.value; });
      return { id: row.dataset.button, actions };
    }),
  };
}

function curveFromInputs() {
  return editableCurve.map((point) => ({ ...point }));
}

function clamp(value, minimum, maximum) {
  return Math.max(minimum, Math.min(maximum, value));
}

function fanMinimumPWM() {
  const value = Number($('fan-min').value);
  return Number.isFinite(value) ? clamp(Math.round(value), 30, 100) : 30;
}

function fanEmergencyTemperature() {
  const value = Number($('fan-emergency').value);
  return Number.isFinite(value) && value > 0 ? clamp(Math.round(value), 70, 100) : 100;
}

function curvePointLimits(index) {
  const previous = editableCurve[index - 1];
  const next = editableCurve[index + 1];
  return {
    minTemp: previous ? Math.floor(previous.temp_c) + 1 : 20,
    maxTemp: next ? Math.ceil(next.temp_c) - 1 : fanEmergencyTemperature(),
    minPWM: Math.max(fanMinimumPWM(), previous ? Math.round(previous.pwm_percent) : 0),
    maxPWM: next ? Math.round(next.pwm_percent) : 100,
  };
}

function setCurvePoint(index, temperature, pwm) {
  if (!editableCurve[index]) return;
  const limits = curvePointLimits(index);
  const minTemp = Math.min(limits.minTemp, limits.maxTemp);
  const maxTemp = Math.max(limits.minTemp, limits.maxTemp);
  const minPWM = Math.min(limits.minPWM, limits.maxPWM);
  const maxPWM = Math.max(limits.minPWM, limits.maxPWM);
  editableCurve[index] = {
    temp_c: clamp(Math.round(temperature), minTemp, maxTemp),
    pwm_percent: clamp(Math.round(pwm), minPWM, maxPWM),
  };
}

function normalizeCurveToControls() {
  let priorPWM = fanMinimumPWM();
  editableCurve = editableCurve.map((point) => {
    const pwm = clamp(Math.max(Math.round(point.pwm_percent), priorPWM), priorPWM, 100);
    priorPWM = pwm;
    return { temp_c: Math.round(point.temp_c), pwm_percent: pwm };
  });
  const maximum = fanEmergencyTemperature();
  for (let index = editableCurve.length - 1; index >= 0; index -= 1) {
    const cap = maximum - (editableCurve.length - 1 - index);
    editableCurve[index].temp_c = Math.min(editableCurve[index].temp_c, cap);
  }
  for (let index = 0; index < editableCurve.length; index += 1) {
    const floor = index === 0 ? 20 : editableCurve[index - 1].temp_c + 1;
    editableCurve[index].temp_c = Math.max(editableCurve[index].temp_c, floor);
  }
  renderFanChart();
}

function findCurveAddCandidate() {
  if (editableCurve.length >= CURVE_MAX_POINTS) return null;
  const maximum = fanEmergencyTemperature();
  let best = null;
  for (let index = 0; index <= editableCurve.length; index += 1) {
    const lower = index === 0 ? 20 : Math.floor(editableCurve[index - 1].temp_c) + 1;
    const upper = index === editableCurve.length ? maximum : Math.ceil(editableCurve[index].temp_c) - 1;
    if (lower > upper) continue;
    const width = upper - lower;
    if (best && width <= best.width) continue;
    const previous = editableCurve[index - 1];
    const next = editableCurve[index];
    let pwm = fanMinimumPWM();
    if (previous && next) pwm = Math.round((previous.pwm_percent + next.pwm_percent) / 2);
    else if (previous) pwm = previous.pwm_percent;
    else if (next) pwm = next.pwm_percent;
    best = {
      index,
      width,
      point: { temp_c: Math.round((lower + upper) / 2), pwm_percent: Math.round(pwm) },
    };
  }
  return best;
}

function updateCurveControls() {
  selectedCurveIndex = clamp(selectedCurveIndex, 0, Math.max(0, editableCurve.length - 1));
  const selected = editableCurve[selectedCurveIndex];
  $('curve-selected').textContent = selected
    ? `节点 ${selectedCurveIndex + 1}/${editableCurve.length} · ${selected.temp_c} °C · ${selected.pwm_percent}%`
    : '—';
  $('curve-add').disabled = uiBusy || !findCurveAddCandidate();
  $('curve-remove').disabled = uiBusy || editableCurve.length <= CURVE_MIN_POINTS;
}

function addCurvePoint() {
  const candidate = findCurveAddCandidate();
  if (!candidate) return;
  editableCurve.splice(candidate.index, 0, candidate.point);
  selectedCurveIndex = candidate.index;
  renderFanChart();
}

function removeSelectedCurvePoint() {
  if (editableCurve.length <= CURVE_MIN_POINTS) return;
  editableCurve.splice(selectedCurveIndex, 1);
  selectedCurveIndex = clamp(selectedCurveIndex, 0, editableCurve.length - 1);
  renderFanChart();
}

function svgElement(name, attributes = {}, text = '') {
  const element = document.createElementNS('http://www.w3.org/2000/svg', name);
  Object.entries(attributes).forEach(([key, value]) => element.setAttribute(key, String(value)));
  if (text) element.textContent = text;
  return element;
}

function renderFanChart() {
  const svg = $('fan-curve-chart');
  const curve = curveFromInputs();
  if (curve.some((point) => !Number.isFinite(point.temp_c) || !Number.isFinite(point.pwm_percent))) return;
  const { left, right, top, bottom } = CHART;
  const x = (temp) => left + ((clamp(temp, 20, 100) - 20) / 80) * (right - left);
  const y = (speed) => bottom - ((clamp(speed, 30, 100) - 30) / 70) * (bottom - top);
  svg.replaceChildren();

  [20, 40, 60, 80, 100].forEach((temp) => {
    svg.append(svgElement('line', { x1: x(temp), y1: top, x2: x(temp), y2: bottom, stroke: 'rgba(150,230,196,.12)' }));
    svg.append(svgElement('text', { x: x(temp), y: 246, fill: '#9bb5aa', 'font-size': 12, 'text-anchor': 'middle' }, `${temp}°`));
  });
  [30, 50, 70, 100].forEach((speed) => {
    svg.append(svgElement('line', { x1: left, y1: y(speed), x2: right, y2: y(speed), stroke: 'rgba(150,230,196,.12)' }));
    svg.append(svgElement('text', { x: 42, y: y(speed) + 4, fill: '#9bb5aa', 'font-size': 12, 'text-anchor': 'end' }, `${speed}%`));
  });
  svg.append(svgElement('polyline', {
    points: curve.map((point) => `${x(point.temp_c)},${y(point.pwm_percent)}`).join(' '),
    fill: 'none', stroke: '#58e6ad', 'stroke-width': 4, 'stroke-linecap': 'round', 'stroke-linejoin': 'round',
  }));
  const actualTemp = Number(currentStatus?.fan_control?.temperature_c);
  if (Number.isFinite(actualTemp) && actualTemp > 0) {
    svg.append(svgElement('line', { x1: x(actualTemp), y1: top, x2: x(actualTemp), y2: bottom, stroke: '#ffb76b', 'stroke-width': 2, 'stroke-dasharray': '6 5' }));
    svg.append(svgElement('text', { x: x(actualTemp), y: 14, fill: '#ffca8f', 'font-size': 12, 'text-anchor': 'middle' }, `当前 ${actualTemp.toFixed(1)}°C`));
  }
  curve.forEach((point, index) => {
    const selected = index === selectedCurveIndex;
    const node = svgElement('circle', {
      cx: x(point.temp_c), cy: y(point.pwm_percent), r: selected ? 9 : 7,
      fill: selected ? '#58e6ad' : '#07110f', stroke: '#8cf3c9', 'stroke-width': 3,
      class: `curve-node${selected ? ' selected' : ''}`, 'data-index': index,
      tabindex: 0, role: 'button', 'aria-label': `节点 ${index + 1}，${point.temp_c} 摄氏度，转速 ${point.pwm_percent}%`,
    });
    svg.append(node);
    const labelY = y(point.pwm_percent) < 44 ? y(point.pwm_percent) + 25 : y(point.pwm_percent) - 14;
    svg.append(svgElement('text', {
      x: x(point.temp_c), y: labelY, fill: selected ? '#8cf3c9' : '#b8d1c6',
      'font-size': 12, 'font-weight': selected ? 800 : 600, 'text-anchor': 'middle', class: 'curve-node-label',
    }, `${point.temp_c}° · ${point.pwm_percent}%`));
  });
  updateCurveControls();
}

function curvePositionFromPointer(event) {
  const svg = $('fan-curve-chart');
  const matrix = svg.getScreenCTM();
  if (!matrix) return null;
  const point = svg.createSVGPoint();
  point.x = event.clientX;
  point.y = event.clientY;
  const local = point.matrixTransform(matrix.inverse());
  return {
    temperature: 20 + ((local.x - CHART.left) / (CHART.right - CHART.left)) * 80,
    pwm: 30 + ((CHART.bottom - local.y) / (CHART.bottom - CHART.top)) * 70,
  };
}

function updateCurveFromPointer(event) {
  if (draggedCurveIndex < 0) return;
  const position = curvePositionFromPointer(event);
  if (!position) return;
  setCurvePoint(draggedCurveIndex, position.temperature, position.pwm);
  renderFanChart();
}

function populateFanDevices(status, keepInputs) {
  const select = $('fan-device');
  const prior = keepInputs ? select.value : (status.config?.fan?.device_id || '');
  const fans = (status.fan_control?.fans || []).filter((fan) => Number(fan.rpm) > 0);
  select.replaceChildren();
  if (!fans.length) {
    const option = document.createElement('option');
    option.value = '';
    option.textContent = '未检测到可控风扇';
    select.append(option);
    select.disabled = true;
    $('fan-device-status').textContent = '需要 IT87 驱动及 fan/pwm 转速节点';
    return;
  }
  fans.forEach((fan) => {
    const option = document.createElement('option');
    option.value = fan.id;
    option.textContent = `${fan.name} fan${fan.channel} · ${fan.rpm} RPM`;
    select.append(option);
  });
  select.disabled = false;
  if (fans.some((fan) => fan.id === prior)) select.value = prior;
  else if (fans.length === 1) select.value = fans[0].id;
  $('fan-device-status').textContent = fans.length === 1 ? '已确认唯一有转速反馈的风扇通道' : `检测到 ${fans.length} 个有转速反馈的通道`;
}

function fillFanInputs(fan = {}) {
  $('fan-enabled').checked = Boolean(fan.enabled);
  $('fan-min').value = fan.min_pwm_percent ?? 60;
  $('fan-emergency').value = fan.emergency_temp_c ?? 85;
  $('fan-poll').value = fan.poll_seconds ?? 2;
  const curve = Array.isArray(fan.curve) && fan.curve.length >= CURVE_MIN_POINTS && fan.curve.length <= CURVE_MAX_POINTS
    && fan.curve.every((point) => Number.isFinite(Number(point.temp_c)) && Number.isFinite(Number(point.pwm_percent)))
    ? fan.curve : DEFAULT_CURVE;
  editableCurve = curve.map((point) => ({ temp_c: Number(point.temp_c), pwm_percent: Number(point.pwm_percent) }));
  selectedCurveIndex = clamp(selectedCurveIndex, 0, editableCurve.length - 1);
}

function render(status, keepInputs = false) {
  currentStatus = status;
  const pkg = status.packages?.[0] || {};
  const cpuTemperature = status.cpu_temperature || {};
  const fanStatus = status.fan_control || {};
  const storageStatus = status.storage || {};
  const gpioStatus = status.gpio || {};
  const selectedFan = fanStatus.fans?.find((fan) => fan.selected) || fanStatus.fans?.find((fan) => Number(fan.rpm) > 0);
  $('cpu-model').textContent = status.cpu_model || '未识别';
  $('cpu-display-label').textContent = cpuTemperature.display_source === 'package_fallback'
    ? 'CPU 温度（Package 回退）'
    : 'CPU 核心最高（RR 口径）';
  $('cpu-display-temperature').textContent = formatTemperature(cpuTemperature.display_c, cpuTemperature.available);
  $('package-temperature').textContent = formatTemperature(cpuTemperature.package_max_c, Number(cpuTemperature.package_sensors) > 0);
  $('current-pl1').textContent = pkg.has_pl1 ? `${pkg.pl1_w} W` : '不可用';
  $('current-pl2').textContent = pkg.has_pl2 ? `${pkg.pl2_w} W` : '不可用';
  $('fan-rpm').textContent = selectedFan ? `${selectedFan.rpm} RPM` : '不可用';
  $('profile').textContent = status.profile?.display || '不支持';
  $('temperature-source').textContent = cpuTemperature.display_source === 'core_max_rr'
    ? `RR 核心最大值（${cpuTemperature.core_sensors} 个 Core）`
    : (cpuTemperature.display_source === 'package_fallback' ? '未找到 Core 标签，回退到 Package' : '未识别');
  $('temperature-sensors').textContent = Array.isArray(status.temperatures) && status.temperatures.length
    ? status.temperatures.map((item) => `${item.label} ${formatTemperature(item.celsius)}`).join(' · ')
    : '未读取到 coretemp';
  $('gpu-runtime').textContent = status.gpu_runtime?.join('，') || '未暴露';
  $('last-apply').textContent = status.last_apply ? new Date(status.last_apply).toLocaleString() : '尚未应用';
  $('last-error').textContent = status.last_error || fanStatus.last_error || '正常';
  $('fan-control').textContent = fanStatus.active
    ? `已启用 · ${Number(fanStatus.temperature_c || 0).toFixed(1)} °C → ${fanStatus.target_pwm_percent || 0}%`
    : (fanStatus.available ? '可用，尚未启用' : '驱动或风扇不可用');
  renderStorageTable(storageStatus);
  $('gpio-status').textContent = gpioStatus.enabled
    ? (gpioStatus.available ? `监听中${gpioStatus.last_event ? ` · 最近：${gpioStatus.last_event}` : ''}` : `已启用但不可用：${gpioStatus.last_error || '无法读取 /dev/port'}`)
    : (gpioStatus.available ? '硬件接口可用，按键映射尚未启用。' : '按键映射默认关闭。');
  $('gpio-status').className = `inline-status${gpioStatus.enabled && (!gpioStatus.available || gpioStatus.last_error) ? ' error' : ''}`;
  $('gpio-diagnostic').textContent = gpioStatus.enabled
    ? (gpioStatus.available ? (gpioStatus.last_event || '监听中') : (gpioStatus.last_error || '不可用'))
    : '未启用';

  const storageWarning = storageStatus.slots?.some((slot) => slot.state === 'warning');
  const gpioError = gpioStatus.enabled && (!gpioStatus.available || gpioStatus.last_error);
  const healthy = status.supported && !status.last_error && !fanStatus.last_error && !storageWarning && !gpioError;
  $('health').textContent = healthy ? '运行正常' : '需要检查';
  $('health').className = `badge ${healthy ? 'ok' : 'error'}`;

  const profile = status.profile || {};
  const maxPL1 = status.effective_max_pl1_w || profile.max_pl1_w || 0;
  const maxPL2 = status.effective_max_pl2_w || profile.max_pl2_w || 0;
  $('pl1').min = profile.min_pl1_w || 1;
  $('pl1').max = maxPL1;
  $('pl2').min = profile.min_pl1_w || 1;
  $('pl2').max = maxPL2;
  $('pl1-range').textContent = `允许 ${$('pl1').min}–${maxPL1} W，推荐 ${profile.default_pl1_w || '—'} W`;
  $('pl2-range').textContent = `不低于 PL1，最高 ${maxPL2} W，推荐 ${profile.default_pl2_w || '—'} W`;
  populateFanDevices(status, keepInputs);
  if (!keepInputs) {
    $('enabled').checked = Boolean(status.config?.enabled);
    $('pl1').value = status.config?.pl1_w ?? profile.default_pl1_w ?? '';
    $('pl2').value = status.config?.pl2_w ?? profile.default_pl2_w ?? '';
    $('interval').value = status.config?.reapply_seconds ?? 30;
    fillFanInputs(status.config?.fan);
    fillGPIOInputs(status.config?.gpio);
  }
  renderFanChart();
}

function showMessage(message, error = false) {
  $('message').textContent = message;
  $('message').className = `message${error ? ' error' : ''}`;
}

function setBusy(busy) {
  uiBusy = busy;
  document.querySelectorAll('button').forEach((button) => { button.disabled = busy; });
  updateCurveControls();
}

async function refresh(keepInputs = false) {
  try {
    render(await request('api/status'), keepInputs);
  } catch (error) {
    showMessage(`读取状态失败：${error.message}`, true);
    $('health').textContent = '连接失败';
    $('health').className = 'badge error';
  }
}

$('config-form').addEventListener('submit', async (event) => {
  event.preventDefault();
  const curve = curveFromInputs();
  const config = {
    enabled: $('enabled').checked,
    pl1_w: Number($('pl1').value),
    pl2_w: Number($('pl2').value),
    reapply_seconds: Number($('interval').value),
    fan: {
      enabled: $('fan-enabled').checked,
      device_id: $('fan-device').value,
      min_pwm_percent: Number($('fan-min').value),
      emergency_temp_c: Number($('fan-emergency').value),
      poll_seconds: Number($('fan-poll').value),
      curve,
    },
    gpio: gpioConfigFromInputs(),
  };
  if (config.pl2_w < config.pl1_w) {
    showMessage('PL2 不能低于 PL1。', true);
    return;
  }
  if (config.fan.enabled && !config.fan.device_id) {
    showMessage('启用风扇曲线前必须检测到有转速反馈的风扇。', true);
    return;
  }
  if (curve.length < CURVE_MIN_POINTS || curve.length > CURVE_MAX_POINTS) {
    showMessage(`风扇曲线必须包含 ${CURVE_MIN_POINTS}–${CURVE_MAX_POINTS} 个节点。`, true);
    return;
  }
  if (curve.some((point, index) => point.pwm_percent < config.fan.min_pwm_percent
      || (index > 0 && (point.temp_c <= curve[index - 1].temp_c || point.pwm_percent < curve[index - 1].pwm_percent)))) {
    showMessage('曲线温度必须严格递增，转速不能随温度升高而下降，且不能低于最低转速。', true);
    return;
  }
  if (config.fan.emergency_temp_c < curve[curve.length - 1].temp_c) {
    showMessage('紧急满速温度不能低于最后一个曲线节点。', true);
    return;
  }
  setBusy(true);
  try {
    render(await request('api/config', { method: 'POST', body: JSON.stringify(config) }));
    showMessage('功耗、拖拽风扇曲线与按键映射配置已保存并应用。');
  } catch (error) {
    showMessage(`应用失败：${error.message}`, true);
  } finally {
    setBusy(false);
  }
});

$('apply-now').addEventListener('click', async () => {
  setBusy(true);
  try {
    render(await request('api/apply', { method: 'POST', body: '{}' }));
    showMessage('已重新应用当前功耗与风扇配置。');
  } catch (error) {
    showMessage(`重应用失败：${error.message}`, true);
  } finally {
    setBusy(false);
  }
});

$('restore').addEventListener('click', async () => {
  if (!window.confirm('确定关闭自动控制，并恢复首次捕获的功耗限制与 BIOS 风扇模式吗？')) return;
  setBusy(true);
  try {
    render(await request('api/restore', { method: 'POST', body: '{}' }));
    showMessage('已关闭自动控制，并恢复原始功耗与风扇配置。');
  } catch (error) {
    showMessage(`恢复失败：${error.message}`, true);
  } finally {
    setBusy(false);
  }
});

const curveChart = $('fan-curve-chart');
curveChart.addEventListener('pointerdown', (event) => {
  const node = event.target.closest?.('.curve-node');
  if (!node) return;
  selectedCurveIndex = Number(node.dataset.index);
  draggedCurveIndex = selectedCurveIndex;
  curveChart.classList.add('dragging');
  curveChart.setPointerCapture(event.pointerId);
  event.preventDefault();
  updateCurveFromPointer(event);
});
curveChart.addEventListener('pointermove', (event) => {
  if (draggedCurveIndex < 0) return;
  event.preventDefault();
  updateCurveFromPointer(event);
});
function finishCurveDrag(event, update = true) {
  if (draggedCurveIndex < 0) return;
  if (update) updateCurveFromPointer(event);
  draggedCurveIndex = -1;
  curveChart.classList.remove('dragging');
  if (curveChart.hasPointerCapture(event.pointerId)) curveChart.releasePointerCapture(event.pointerId);
}
curveChart.addEventListener('pointerup', finishCurveDrag);
curveChart.addEventListener('pointercancel', (event) => finishCurveDrag(event, false));
curveChart.addEventListener('lostpointercapture', () => {
  draggedCurveIndex = -1;
  curveChart.classList.remove('dragging');
});
curveChart.addEventListener('keydown', (event) => {
  const node = event.target.closest?.('.curve-node');
  if (node) selectedCurveIndex = Number(node.dataset.index);
  const point = editableCurve[selectedCurveIndex];
  if (!point) return;
  let temperature = point.temp_c;
  let pwm = point.pwm_percent;
  if (event.key === 'ArrowLeft') temperature -= 1;
  else if (event.key === 'ArrowRight') temperature += 1;
  else if (event.key === 'ArrowDown') pwm -= 1;
  else if (event.key === 'ArrowUp') pwm += 1;
  else if (event.key === 'Delete' || event.key === 'Backspace') {
    event.preventDefault();
    removeSelectedCurvePoint();
    return;
  } else return;
  event.preventDefault();
  setCurvePoint(selectedCurveIndex, temperature, pwm);
  renderFanChart();
  requestAnimationFrame(() => curveChart.querySelector(`.curve-node[data-index="${selectedCurveIndex}"]`)?.focus());
});
$('curve-add').addEventListener('click', addCurvePoint);
$('curve-remove').addEventListener('click', removeSelectedCurvePoint);
$('fan-min').addEventListener('input', () => {
  if ($('fan-min').value !== '') normalizeCurveToControls();
});
$('fan-emergency').addEventListener('input', () => {
  if ($('fan-emergency').value !== '') normalizeCurveToControls();
});
setupGPIOActions();
$('gpio-enabled').addEventListener('change', updateGPIOEnabledState);
renderFanChart();
refresh();
setInterval(() => refresh(true), 5000);
