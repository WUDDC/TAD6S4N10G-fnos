const $ = (id) => document.getElementById(id);
const DEFAULT_CURVE = [
  { temp_c: 40, pwm_percent: 60 },
  { temp_c: 55, pwm_percent: 70 },
  { temp_c: 70, pwm_percent: 85 },
  { temp_c: 80, pwm_percent: 100 },
];
let currentStatus = null;

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

function curveFromInputs() {
  const temperatures = [...document.querySelectorAll('.curve-temp')];
  const speeds = [...document.querySelectorAll('.curve-pwm')];
  return temperatures.map((input, index) => ({
    temp_c: Number(input.value),
    pwm_percent: Number(speeds[index].value),
  }));
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
  const left = 52;
  const right = 616;
  const top = 20;
  const bottom = 222;
  const x = (temp) => left + ((Math.max(20, Math.min(100, temp)) - 20) / 80) * (right - left);
  const y = (speed) => bottom - ((Math.max(30, Math.min(100, speed)) - 30) / 70) * (bottom - top);
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
  curve.forEach((point) => {
    svg.append(svgElement('circle', { cx: x(point.temp_c), cy: y(point.pwm_percent), r: 6, fill: '#07110f', stroke: '#8cf3c9', 'stroke-width': 3 }));
  });
  const actualTemp = Number(currentStatus?.fan_control?.temperature_c);
  if (Number.isFinite(actualTemp) && actualTemp > 0) {
    svg.append(svgElement('line', { x1: x(actualTemp), y1: top, x2: x(actualTemp), y2: bottom, stroke: '#ffb76b', 'stroke-width': 2, 'stroke-dasharray': '6 5' }));
    svg.append(svgElement('text', { x: x(actualTemp), y: 14, fill: '#ffca8f', 'font-size': 12, 'text-anchor': 'middle' }, `当前 ${actualTemp.toFixed(1)}°C`));
  }
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
  const curve = Array.isArray(fan.curve) && fan.curve.length === 4 ? fan.curve : DEFAULT_CURVE;
  document.querySelectorAll('.curve-temp').forEach((input, index) => { input.value = curve[index].temp_c; });
  document.querySelectorAll('.curve-pwm').forEach((input, index) => { input.value = curve[index].pwm_percent; });
}

function render(status, keepInputs = false) {
  currentStatus = status;
  const pkg = status.packages?.[0] || {};
  const cpuTemperature = status.cpu_temperature || {};
  const fanStatus = status.fan_control || {};
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

  const healthy = status.supported && !status.last_error && !fanStatus.last_error;
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
  }
  renderFanChart();
}

function showMessage(message, error = false) {
  $('message').textContent = message;
  $('message').className = `message${error ? ' error' : ''}`;
}

function setBusy(busy) {
  document.querySelectorAll('button').forEach((button) => { button.disabled = busy; });
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
  };
  if (config.pl2_w < config.pl1_w) {
    showMessage('PL2 不能低于 PL1。', true);
    return;
  }
  if (config.fan.enabled && !config.fan.device_id) {
    showMessage('启用风扇曲线前必须检测到有转速反馈的风扇。', true);
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
    showMessage('功耗与风扇配置已保存并应用。');
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

document.querySelectorAll('.curve-temp, .curve-pwm').forEach((input) => input.addEventListener('input', renderFanChart));
refresh();
setInterval(() => refresh(true), 5000);
