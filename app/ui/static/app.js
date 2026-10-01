const $ = (id) => document.getElementById(id);
// 页面错误采集：白屏等运行期问题在真机无控制台可看，捕获后写入
// localStorage（跨刷新保留最近 20 条），生成调试报告时带出并清空。
const PAGE_ERROR_STORE_KEY = 'tad-page-errors';
try {
  const recordPageError = (detail) => {
    try {
      const entry = `${new Date().toISOString()} ${String(detail).slice(0, 280)}`;
      const stored = JSON.parse(window.localStorage.getItem(PAGE_ERROR_STORE_KEY) || '[]');
      stored.push(entry);
      window.localStorage.setItem(PAGE_ERROR_STORE_KEY, JSON.stringify(stored.slice(-20)));
    } catch (storageError) { /* localStorage 不可用时丢弃 */ }
  };
  window.addEventListener('error', (event) => {
    recordPageError(`${event.message} @${String(event.filename || '').split('/').pop()}:${event.lineno}`);
  });
  window.addEventListener('unhandledrejection', (event) => {
    const reason = event.reason && event.reason.message ? event.reason.message : event.reason;
    recordPageError(`rejection: ${reason}`);
  });
} catch (collectorError) { /* 采集器自身失败不影响页面 */ }
const DEFAULT_CPU_CURVE = [
  { temp_c: 40, pwm_percent: 60 },
  { temp_c: 55, pwm_percent: 70 },
  { temp_c: 70, pwm_percent: 85 },
  { temp_c: 80, pwm_percent: 100 },
];
const DEFAULT_STORAGE_CURVE = [
  { temp_c: 25, pwm_percent: 60 },
  { temp_c: 35, pwm_percent: 85 },
  { temp_c: 50, pwm_percent: 100 },
];
const GPIO_ACTIONS = [
  ['none', '无动作'],
  ['log', '仅记录日志'],
  ['refresh_storage', '刷新硬盘仓位'],
  ['smart_check', '刷新仓位并检查 SMART'],
  ['reapply_plugin', '重新应用插件配置'],
];
const GITHUB_LATEST_RELEASE_API = 'https://api.github.com/repos/luodaoyi/TAD6S4N10G-fnos/releases/latest';
const GPIO_SCRIPT_MAX_COUNT = 32;
const GPIO_SCRIPT_MAX_BODY_BYTES = 65536;
const STORAGE_STATE_LABELS = {
  empty: '空置', present: '已插入', used: '已使用', warning: '告警', unknown: '未知',
};
const STORAGE_ACTIVITY_LABELS = {
  idle: '空闲', light: '轻载', medium: '中载', heavy: '高载', busy: '繁忙', full: '满载',
  sleeping: '休眠', unknown: '未知',
};
const STORAGE_HOT_C = { front: 55, m2: 70 };
const CURVE_MIN_POINTS = 2;
const CURVE_MAX_POINTS = 8;
const CHART = { left: 48, right: 600, top: 28, bottom: 218 };
const CURVE_KINDS = ['cpu', 'hdd', 'nvme'];
const FAN_SLOT_GROUPS = {
  hdd: {
    containerID: 'hdd-slot-options', configField: 'hdd_slot_ids',
    slots: [6, 5, 4, 3, 2, 1].map((slot) => ({ id: `front-${slot}`, label: `SATA ${slot}` })),
  },
  nvme: {
    containerID: 'nvme-slot-options', configField: 'nvme_slot_ids',
    slots: [1, 2, 3, 4].map((slot) => ({ id: `m2-${slot}`, label: `NVMe ${slot}` })),
  },
};
const FAN_CURVE_GROUPS = {
  cpu: { containerID: 'cpu-fan-options', configField: 'cpu_fan_ids' },
  hdd: { containerID: 'hdd-fan-options', configField: 'hdd_fan_ids' },
  nvme: { containerID: 'nvme-fan-options', configField: 'nvme_fan_ids' },
};
const STORAGE_VISUAL_GROUPS = [
  {
    kind: 'front', base: 'images/panel/6SATA面板底图.svg', ids: [6, 5, 4, 3, 2, 1].map((slot) => `front-${slot}`),
    assets: {
      gray: 'images/panel/SATA灰-休眠未使用.svg',
      blue: 'images/panel/SATA蓝-休眠.svg',
      red: 'images/panel/SATA红-报警.svg',
      green: 'images/panel/SATA绿-正常健康.svg',
      empty: 'images/panel/SATA空置.svg',
    },
  },
  {
    kind: 'm2', base: 'images/panel/4M2面板底图.svg', ids: ['m2-4', 'm2-2', 'm2-3', 'm2-1'], flip: new Set(['m2-2', 'm2-1']),
    assets: {
      gray: 'images/panel/M2灰-休眠未使用.svg',
      blue: 'images/panel/M2蓝-休眠.svg',
      red: 'images/panel/M2红-报警.svg',
      green: 'images/panel/M2绿-正常健康.svg',
      empty: 'images/panel/M2空置.svg',
    },
  },
];
const curveEditors = {
  cpu: {
    chartID: 'fan-curve-chart', addID: 'curve-add', removeID: 'curve-remove', selectedID: 'curve-selected',
    curve: DEFAULT_CPU_CURVE.map((point) => ({ ...point })), selectedIndex: 0, draggedIndex: -1, color: '#3f6ff5',
  },
  hdd: {
    chartID: 'disk-curve-chart', addID: 'disk-curve-add', removeID: 'disk-curve-remove', selectedID: 'disk-curve-selected',
    curve: DEFAULT_STORAGE_CURVE.map((point) => ({ ...point })), selectedIndex: 0, draggedIndex: -1, color: '#18a779',
  },
  nvme: {
    chartID: 'nvme-curve-chart', addID: 'nvme-curve-add', removeID: 'nvme-curve-remove', selectedID: 'nvme-curve-selected',
    curve: DEFAULT_STORAGE_CURVE.map((point) => ({ ...point })), selectedIndex: 0, draggedIndex: -1, color: '#a25bd7',
  },
};
let currentStatus = null;
let uiBusy = false;
let gpioScripts = [];
let gpioEditingScriptID = '';
let storageVisualReady = false;
let fanSelectorSignature = null;
let debugReportText = '';

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

function parseSemanticVersion(value) {
  const match = String(value || '').trim().match(/^v?(\d+)\.(\d+)\.(\d+)(?:[-+].*)?$/i);
  return match ? match.slice(1, 4).map(Number) : null;
}

function compareSemanticVersions(left, right) {
  for (let index = 0; index < 3; index += 1) {
    if (left[index] !== right[index]) return left[index] > right[index] ? 1 : -1;
  }
  return 0;
}

async function checkForUpdates() {
  const button = $('check-updates');
  const status = $('update-status');
  const releaseLink = $('update-release-link');
  const currentVersion = parseSemanticVersion(currentStatus?.version);
  releaseLink.hidden = true;
  status.textContent = '正在检查…';
  status.className = 'update-status';
  button.disabled = true;
  try {
    if (!currentVersion) throw new Error('当前运行版本无法比较');
    const response = await fetch(GITHUB_LATEST_RELEASE_API, {
      cache: 'no-store',
      headers: { Accept: 'application/vnd.github+json' },
    });
    if (!response.ok) throw new Error(`GitHub 返回 HTTP ${response.status}`);
    const release = await response.json();
    const latestVersion = parseSemanticVersion(release.tag_name);
    if (!latestVersion) throw new Error('最新 Release 版本格式无法识别');
    if (compareSemanticVersions(latestVersion, currentVersion) > 0) {
      const releaseURL = new URL(release.html_url);
      if (releaseURL.protocol !== 'https:' || releaseURL.hostname !== 'github.com') {
        throw new Error('Release 地址不安全');
      }
      status.textContent = '';
      releaseLink.href = releaseURL.toString();
      releaseLink.textContent = `有新版本 v${latestVersion.join('.')}，前去更新`;
      releaseLink.hidden = false;
    } else {
      status.textContent = `当前 v${currentVersion.join('.')} 已是最新版本`;
      status.className = 'update-status ok';
    }
  } catch (error) {
    status.textContent = `检查失败：${error.message}`;
    status.className = 'update-status error';
  } finally {
    button.disabled = false;
  }
}

function formatTemperature(value, available = true) {
  const number = Number(value);
  return available && Number.isFinite(number) ? `${number.toFixed(1)} °C` : '不可用';
}

function formatUtilization(value) {
  const number = Number(value);
  return Number.isFinite(number) && number >= 0 ? `${Math.round(number)}%` : '';
}

function isStorageHot(slot = {}) {
  const temperature = Number(slot.temperature_c);
  if (!Number.isFinite(temperature) || temperature <= 0) return false;
  const threshold = STORAGE_HOT_C[slot.kind] || STORAGE_HOT_C.front;
  return temperature >= threshold;
}

function storageChip(text, tone = 'muted', options = {}) {
  const chip = document.createElement(options.detail ? 'button' : 'span');
  if (options.detail) chip.type = 'button';
  chip.className = `storage-chip tone-${tone}${options.detail ? ' is-action' : ''}`;
  chip.textContent = text;
  if (options.detail) {
    chip.setAttribute('aria-haspopup', 'dialog');
    chip.title = '点击查看详情';
    chip.addEventListener('click', (event) => {
      event.preventDefault();
      event.stopPropagation();
      openAppModal(options.title || '详细信息', options.detail);
    });
  }
  return chip;
}

function storageHealthLabel(slot = {}) {
  if (slot.warning) return '告警';
  return slot.health || '—';
}

function storageHealthDetail(slot = {}) {
  return String(slot.warning || '').trim();
}

function openAppModal(title, body) {
  const modal = $('app-modal');
  if (!modal) return;
  $('app-modal-title').textContent = title;
  $('app-modal-body').textContent = body;
  modal.hidden = false;
  document.body.classList.add('modal-open');
  modal.dataset.lastFocus = '';
  openAppModal.lastFocus = document.activeElement;
  $('app-modal-close')?.focus();
}

function closeAppModal() {
  const modal = $('app-modal');
  if (!modal || modal.hidden) return;
  modal.hidden = true;
  document.body.classList.remove('modal-open');
  const lastFocus = openAppModal.lastFocus;
  if (lastFocus && typeof lastFocus.focus === 'function') lastFocus.focus();
}

function setupAppModal() {
  const modal = $('app-modal');
  if (!modal) return;
  modal.addEventListener('click', (event) => {
    if (event.target.closest('[data-modal-close]')) closeAppModal();
  });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') closeAppModal();
  });
}

function storageActivityTone(activity) {
  if (activity === 'sleeping') return 'sleep';
  if (activity === 'idle') return 'idle';
  return 'muted';
}

function storageHealthTone(slot = {}) {
  if (slot.state === 'warning' || slot.warning) return 'danger';
  if (slot.health && slot.health !== '—' && slot.health !== '未读取') return 'ok';
  return 'muted';
}

function storageTempTone(slot = {}) {
  return isStorageHot(slot) ? 'danger' : 'temp';
}

function storageSlotStatusLabel(slot = {}, tone = '') {
  if (tone === 'empty') return '空置';
  if (slot.state === 'warning') return STORAGE_STATE_LABELS.warning;
  if (isStorageHot(slot)) return '高温';
  if (slot.state === 'present') return '未使用';
  if (slot.state === 'unknown') return STORAGE_STATE_LABELS.unknown;
  // used 只代表"已使用"，不等于 SMART 健康；无活动读数时回退状态标签。
  return STORAGE_ACTIVITY_LABELS[slot.activity]
    || STORAGE_STATE_LABELS[slot.state]
    || STORAGE_STATE_LABELS.unknown;
}

function connectedFans(fanStatus = {}, limit = 0) {
  // 有转速，或已勾选（瞬时 0 转停转/故障保护仍可见）；未接线且持续 0 RPM 的通道不列出。
  const fans = (fanStatus.fans || []).filter((fan) => Number(fan.rpm) > 0 || fan.selected === true);
  return limit > 0 ? fans.slice(0, limit) : fans;
}

// 选择器签名只由可见风扇集合决定、与转速无关：已勾选风扇瞬时 0 转
// 不会触发列表重建，未保存的勾选因此不会丢。
function fanListSignature(fans = []) {
  return fans.map((fan) => fan.id).join('|');
}

function fanLabel(fan, fans = []) {
  return fans.length === 1 ? 'FAN' : `FAN${fan.channel}`;
}

function renderHealthBadge(label, tone, detail) {
  const badgeClass = `badge ${tone}`;
  ['health', 'about-health'].forEach((id) => {
    const badge = $(id);
    if (!badge) return;
    badge.textContent = label;
    badge.className = badgeClass;
  });
  if ($('health-tooltip')) $('health-tooltip').textContent = detail;
  if ($('about-health-detail')) $('about-health-detail').textContent = detail;
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

function sortedStorageSlots(storage = {}) {
  return [...(storage.slots || [])].sort((left, right) => {
    if (left.kind !== right.kind) return left.kind === 'front' ? -1 : 1;
    return left.slot - right.slot;
  });
}

function renderStorageTable(storage = {}) {
  const slots = sortedStorageSlots(storage);
  const body = $('storage-body');
  body.replaceChildren();
  slots.forEach((slot) => {
    const row = document.createElement('tr');
    const empty = slot.state === 'empty';
    row.className = `storage-${slot.state || 'unknown'}${isStorageHot(slot) ? ' storage-hot' : ''}`;
    const label = slot.kind === 'front' ? `前置 ${slot.slot}` : `M.2 ${slot.slot}`;
    const stateLabel = STORAGE_STATE_LABELS[slot.state] || slot.state || '未知';
    const activityLabel = empty ? '—' : (STORAGE_ACTIVITY_LABELS[slot.activity] || slot.activity || '未知');
    const purposeLabel = slot.purpose || (empty ? '空仓位' : '—');
    const healthLabel = storageHealthLabel(slot);
    const healthDetail = storageHealthDetail(slot);
    const temperature = formatTemperature(slot.temperature_c, Number(slot.temperature_c) > 0);
    const model = slot.model || '—';
    const meta = [slot.device, formatSize(slot.size_bytes), slot.serial].filter(Boolean).join(' · ') || '—';
    const extra = [activityLabel, purposeLabel].filter((value) => value && value !== '—').join(' · ') || '—';
    const utilization = formatUtilization(slot.utilization_percent);
    const cells = [
      ['th', '仓位', 'storage-col-slot', label],
      ['td', '状态', 'storage-col-state', stateLabel],
      ['td', '活动', 'storage-col-activity', activityLabel],
      ['td', '繁忙度', 'storage-col-util', utilization || '—'],
      ['td', '设备', 'storage-col-device', null],
      ['td', '用途', 'storage-col-purpose', purposeLabel],
      ['td', '健康', 'storage-col-health', healthLabel],
      ['td', '温度', 'storage-col-temp', temperature],
    ];
    cells.forEach(([tag, name, className, value]) => {
      const cell = document.createElement(tag);
      cell.dataset.label = name;
      cell.className = className;
      if (className === 'storage-col-device') {
        if (empty) {
          cell.textContent = '—';
        } else {
          const wrap = document.createElement('div');
          wrap.className = 'storage-device';
          const title = document.createElement('span');
          title.className = 'storage-model';
          title.textContent = model;
          const detail = document.createElement('small');
          detail.className = 'storage-device-meta';
          detail.textContent = meta;
          const more = document.createElement('small');
          more.className = 'storage-device-extra';
          more.textContent = extra;
          wrap.append(title, detail, more);
          cell.append(wrap);
        }
      } else if (!empty && className === 'storage-col-activity') {
        cell.append(storageChip(activityLabel, storageActivityTone(slot.activity)));
      } else if (!empty && className === 'storage-col-util') {
        cell.append(storageChip(utilization || '—', 'muted'));
      } else if (!empty && className === 'storage-col-health') {
        cell.append(storageChip(healthLabel, storageHealthTone(slot), healthDetail ? {
          title: `${label} 健康详情`,
          detail: healthDetail,
        } : {}));
      } else if (!empty && className === 'storage-col-temp') {
        cell.append(storageChip(temperature, storageTempTone(slot)));
      } else {
        cell.textContent = value;
      }
      row.append(cell);
    });
    const cardHead = document.createElement('td');
    cardHead.className = 'storage-card-head';
    cardHead.dataset.label = '仓位';
    cardHead.textContent = `${label} · ${stateLabel}`;
    row.append(cardHead);
    body.append(row);
  });
  if (!slots.length) {
    const row = document.createElement('tr');
    const cell = document.createElement('td');
    cell.colSpan = 8;
    cell.dataset.label = '状态';
    cell.textContent = '尚未获得仓位信息';
    row.append(cell);
    body.append(row);
  }
  $('storage-updated').textContent = storage.updated_at
    ? `更新于 ${new Date(storage.updated_at).toLocaleTimeString()}` : '等待刷新';
  $('storage-error').textContent = storage.last_error || '';
  $('storage-error').className = `inline-status${storage.last_error ? ' error' : ''}`;
}

function initializeStorageVisual() {
  if (storageVisualReady) return;
  const target = $('storage-visual');
  STORAGE_VISUAL_GROUPS.forEach((group) => {
    const figure = document.createElement('figure');
    figure.className = `storage-visual-group storage-visual-${group.kind}`;
    const frame = document.createElement('div');
    frame.className = 'storage-visual-frame';
    const base = document.createElement('img');
    base.className = 'storage-visual-base';
    base.src = baseUrl(group.base);
    base.alt = group.kind === 'front' ? '六盘位机箱示意图' : '四个 M.2 槽位示意图';
    frame.append(base);
    if (group.kind === 'm2') {
      const caption = document.createElement('div');
      caption.className = 'storage-visual-caption';
      caption.textContent = '内置 M.2 插槽';
      frame.append(caption);
    }
    group.ids.forEach((id) => {
      const marker = document.createElement('div');
      marker.className = 'storage-visual-slot is-empty';
      marker.dataset.slot = id;
      const overlay = document.createElement('img');
      overlay.className = `storage-slot-overlay${group.flip?.has(id) ? ' flipped' : ''}`;
      overlay.alt = '';
      overlay.hidden = true;
      const text = document.createElement('div');
      text.className = 'storage-slot-text';
      const name = document.createElement('b');
      const temperature = document.createElement('span');
      const status = document.createElement('small');
      text.append(name, temperature, status);
      marker.append(overlay, text);
      frame.append(marker);
    });
    figure.append(frame);
    target.append(figure);
    frame.addEventListener('click', (event) => {
      const marker = event.target.closest('.storage-visual-slot');
      if (!marker?.dataset.detail) return;
      openAppModal(marker.dataset.title || '健康详情', marker.dataset.detail);
    });
    Object.values(group.assets).forEach((path) => { const image = new Image(); image.src = baseUrl(path); });
  });
  storageVisualReady = true;
}

function storageVisualTone(slot = {}) {
  if (slot.state === 'empty') return 'empty';
  if (slot.state === 'warning' || isStorageHot(slot)) return 'red';
  if (slot.activity === 'sleeping') return 'blue';
  if (slot.state === 'present' || slot.state === 'unknown') return 'gray';
  return 'green';
}

function renderStorageVisual(storage = {}) {
  initializeStorageVisual();
  const slots = new Map((storage.slots || []).map((slot) => [slot.id || `${slot.kind}-${slot.slot}`, slot]));
  STORAGE_VISUAL_GROUPS.forEach((group) => {
    group.ids.forEach((id) => {
      const slot = slots.get(id) || { id, kind: group.kind, state: 'unknown', activity: 'unknown' };
      const marker = document.querySelector(`.storage-visual-slot[data-slot="${id}"]`);
      const tone = storageVisualTone(slot);
      const healthDetail = storageHealthDetail(slot);
      marker.className = `storage-visual-slot tone-${tone}${slot.state === 'empty' ? ' is-empty' : ''}${healthDetail ? ' is-action' : ''}`;
      if (healthDetail) {
        const slotLabel = slot.kind === 'front' ? `前置 ${slot.slot}` : `M.2 ${slot.slot}`;
        marker.dataset.detail = healthDetail;
        marker.dataset.title = `${slotLabel} 健康详情`;
      } else {
        delete marker.dataset.detail;
        delete marker.dataset.title;
      }
      const overlay = marker.querySelector('.storage-slot-overlay');
      // 空仓也铺"空置"托盘底图：机箱底图自带的空槽比托盘矮，不铺会让
      // 空仓与在位仓的可见高度不一致
      const source = baseUrl(group.assets[tone] || group.assets.gray);
      if (overlay.dataset.source !== source) {
        overlay.src = source;
        overlay.dataset.source = source;
      }
      overlay.hidden = false;
      const temperature = marker.querySelector('.storage-slot-text span');
      const status = marker.querySelector('.storage-slot-text small');
      marker.querySelector('.storage-slot-text b').textContent = id.split('-').pop();
      const hasTemperature = Number(slot.temperature_c) > 0;
      // 前置仓固定三行槽位（数字/状态/温度，与 M.2 顺序一致），温度为空
      // 也保留槽位：空仓的"空置"与在位盘的状态词落在同一高度，不随内容行数漂移。
      if (tone === 'empty' || !hasTemperature) {
        temperature.textContent = '';
      } else if (group.kind === 'front') {
        // 前置仓窄（12.5% 宽），单行"35.5 °C"必被省略号截断：数值（一位
        // 小数，与详情表格一致）与单位分两行完整显示
        temperature.textContent = `${Number(slot.temperature_c).toFixed(1)}\n°C`;
      } else {
        temperature.textContent = formatTemperature(slot.temperature_c);
      }
      if (status) {
        status.textContent = storageSlotStatusLabel(slot, tone);
      }
    });
  });
}

function renderDiagnosticChips(targetID, items, emptyText = '—') {
  const target = $(targetID);
  target.replaceChildren();
  if (!items.length) {
    target.textContent = emptyText;
    return;
  }
  items.forEach((item) => {
    const chip = document.createElement('span');
    chip.className = `diagnostic-chip${item.tone ? ` ${item.tone}` : ''}`;
    if (item.label) {
      const label = document.createElement('b');
      label.textContent = item.label;
      chip.append(label);
    }
    const value = document.createElement('span');
    value.textContent = item.value;
    chip.append(value);
    target.append(chip);
  });
}

function fanSourceLabel(source = '') {
  const labels = { cpu: 'CPU', hdd: 'HDD/SATA', nvme: 'NVMe' };
  const parts = String(source).split('+').filter(Boolean).map((part) => labels[part] || part);
  return parts.length ? parts.join(' + ') : '当前温度';
}

function renderDiagnostics(status, fanStatus) {
  const temperatures = Array.isArray(status.temperatures) ? status.temperatures : [];
  renderDiagnosticChips('temperature-sensors', temperatures.map((item) => ({
    label: item.label || 'coretemp',
    value: formatTemperature(item.celsius),
  })), '未读取到 coretemp');

  const gpuRuntime = Array.isArray(status.gpu_runtime) ? status.gpu_runtime : [];
  renderDiagnosticChips('gpu-runtime', gpuRuntime.map((item) => {
    const value = String(item);
    return {
      value,
      tone: /active|运行|启用/i.test(value) ? 'ok' : (/unsupported|未暴露|不可用/i.test(value) ? 'muted' : ''),
    };
  }), '未暴露');

  if (!fanStatus.active) {
    renderDiagnosticChips('fan-control', [{
      value: fanStatus.driver_detected === false
        ? '未检测到或未加载 IT87 驱动'
        : (fanStatus.available ? '可用，尚未启用' : '驱动已检测，但风扇无有效转速反馈'),
      tone: fanStatus.available ? 'muted' : 'warning',
    }]);
    return;
  }
  const cpuTemperature = Number(fanStatus.cpu_temperature_c ?? fanStatus.temperature_c);
  const hddTemperature = Number(fanStatus.hdd_temperature_c);
  const nvmeTemperature = Number(fanStatus.nvme_temperature_c);
  const chips = [{
    label: 'CPU',
    value: `${formatTemperature(cpuTemperature, cpuTemperature > 0)} → ${fanStatus.cpu_target_pwm_percent || 0}%`,
  }];
  chips.push(hddTemperature > 0
    ? { label: 'HDD/SATA', value: `${formatTemperature(hddTemperature)} → ${fanStatus.hdd_target_pwm_percent || 0}%` }
    : { label: 'HDD/SATA', value: '温度暂不可用', tone: 'muted' });
  chips.push(nvmeTemperature > 0
    ? { label: 'NVMe', value: `${formatTemperature(nvmeTemperature)} → ${fanStatus.nvme_target_pwm_percent || 0}%` }
    : { label: 'NVMe', value: '温度暂不可用', tone: 'muted' });
  chips.push({
    label: '最终',
    value: `${fanSourceLabel(fanStatus.control_source)} · ${fanStatus.target_pwm_percent || 0}%`,
    tone: 'ok',
  });
  renderDiagnosticChips('fan-control', chips);
}

function healthIssues(status, fanStatus, storageStatus, gpioStatus) {
  const issues = [];
  if (!status.supported) issues.push('当前处理器未通过 TAD6S4N模块兼容性检查');
  if (fanStatus.driver_detected === false) issues.push('未检测到或未加载 IT87 风扇驱动');
  const fans = Array.isArray(fanStatus.fans) ? fanStatus.fans : [];
  if (fanStatus.driver_detected === true && !fans.length) {
    issues.push('IT87 驱动已检测，但未发现可读取 PWM/RPM 的风扇通道');
  } else if (fanStatus.driver_detected === true && fans.length && !fans.some((fan) => Number(fan.rpm) > 0)) {
    issues.push('已发现风扇通道，但当前转速反馈均为 0 RPM；请检查风扇接线或驱动状态');
  }
  if (status.cpu_temperature?.available === false) {
    issues.push('未读取到 CPU coretemp 温度；请检查 coretemp 内核驱动是否已加载');
  }
  if (status.last_error) issues.push(`模块：${status.last_error}`);
  if (fanStatus.last_error) issues.push(`风扇：${fanStatus.last_error}`);
  const storagePending = !storageStatus.updated_at
    && String(storageStatus.last_error || '').includes('尚未完成首次刷新');
  if (storageStatus.last_error && !storagePending) issues.push(`硬盘检测：${storageStatus.last_error}`);
  const slots = Array.isArray(storageStatus.slots) ? storageStatus.slots : [];
  const detectedSlots = slots.filter((slot) => slot.device
    || ['present', 'used', 'warning'].includes(slot.state));
  if (storageStatus.updated_at && !detectedSlots.length) {
    issues.push('未检测到任何已插入硬盘；若机器实际装有硬盘，请检查 AHCI/NVMe 驱动和槽位映射');
  }
  // 休眠盘不读取温度是设计行为（避免唤醒），不构成温度接口故障
  const missingTemperatureSlots = detectedSlots.filter((slot) => slot.activity !== 'sleeping' && !(Number(slot.temperature_c) > 0));
  if (missingTemperatureSlots.length) {
    const labels = missingTemperatureSlots.map((slot) => (slot.kind === 'front' ? `SATA ${slot.slot}` : `NVMe ${slot.slot}`));
    issues.push(`${labels.join('、')} 未读取到温度；请检查 SMART/NVMe 温度接口与相关驱动`);
  }
  slots.filter((slot) => slot.state === 'warning').forEach((slot) => {
    const label = slot.kind === 'front' ? `SATA ${slot.slot}` : `NVMe ${slot.slot}`;
    issues.push(`${label}：${slot.warning || slot.health || '硬盘健康状态告警'}`);
  });
  slots.filter((slot) => isStorageHot(slot)).forEach((slot) => {
    const label = slot.kind === 'front' ? `SATA ${slot.slot}` : `NVMe ${slot.slot}`;
    const threshold = STORAGE_HOT_C[slot.kind] || STORAGE_HOT_C.front;
    issues.push(`${label}：温度 ${formatTemperature(slot.temperature_c)}，达到高温阈值 ${threshold}°C`);
  });
  if (gpioStatus.enabled && (!gpioStatus.available || gpioStatus.last_error)) {
    issues.push(`按钮控制：${gpioStatus.last_error || 'GPIO 硬件接口不可用'}`);
  }
  return [...new Set(issues)];
}

function renderFanHardwareWarning(fanStatus = {}) {
  const warning = $('fan-driver-warning');
  const fans = Array.isArray(fanStatus.fans) ? fanStatus.fans : [];
  const missingDriver = fanStatus.driver_detected === false;
  const noChannel = fanStatus.driver_detected === true && !fans.length;
  const noRPM = fanStatus.driver_detected === true && fans.length
    && !fans.some((fan) => Number(fan.rpm) > 0);
  warning.hidden = !(missingDriver || noChannel || noRPM);
  $('fan-warning-driver-link').hidden = !missingDriver;
  if (missingDriver) {
    $('fan-warning-title').textContent = '未检测到或未加载 IT87 风扇驱动';
    $('fan-warning-description').textContent = '风扇转速和 PWM 控制暂不可用。本插件安装时应已尝试内置 DKMS；请确认 dkms/gcc/make/linux-headers 后重启模块。';
  } else if (noChannel) {
    $('fan-warning-title').textContent = '未发现可控风扇通道';
    $('fan-warning-description').textContent = 'IT87 驱动已检测，但没有同时暴露 fan、pwm 与 pwm_enable 的通道，请检查驱动适配。';
  } else if (noRPM) {
    $('fan-warning-title').textContent = '风扇转速反馈为 0 RPM';
    $('fan-warning-description').textContent = '已发现风扇通道，请检查单风扇接线、BIOS 模式或驱动状态。';
  }
}

function setupFanSlotSelectors() {
  Object.values(FAN_SLOT_GROUPS).forEach((group) => {
    const container = $(group.containerID);
    group.slots.forEach((slot) => {
      const label = document.createElement('label');
      label.className = 'curve-slot-option';
      label.dataset.slotId = slot.id;
      const input = document.createElement('input');
      input.type = 'checkbox';
      input.value = slot.id;
      input.checked = true;
      const name = document.createElement('b');
      name.textContent = slot.label;
      const temperature = document.createElement('small');
      temperature.textContent = '等待温度';
      label.append(input, name, temperature);
      container.append(label);
    });
  });
}

function fillFanSlotSelections(fan = {}) {
  Object.values(FAN_SLOT_GROUPS).forEach((group) => {
    const configured = Array.isArray(fan[group.configField])
      ? fan[group.configField] : group.slots.map((slot) => slot.id);
    const selected = new Set(configured);
    $(group.containerID).querySelectorAll('input[type="checkbox"]').forEach((input) => {
      input.checked = selected.has(input.value);
    });
  });
}

function selectedFanSlotIDs(kind) {
  const group = FAN_SLOT_GROUPS[kind];
  return [...$(group.containerID).querySelectorAll('input[type="checkbox"]:checked')]
    .map((input) => input.value);
}

function renderFanSlotTemperatures(storage = {}) {
  const slots = new Map((storage.slots || []).map((slot) => [slot.id, slot]));
  Object.values(FAN_SLOT_GROUPS).forEach((group) => {
    group.slots.forEach((definition) => {
      const label = $(group.containerID).querySelector(`[data-slot-id="${definition.id}"]`);
      const slot = slots.get(definition.id);
      const state = slot?.state || 'unknown';
      label.className = `curve-slot-option storage-${state}`;
      const temperature = Number(slot?.temperature_c);
      const stateLabel = STORAGE_STATE_LABELS[state] || state;
      label.querySelector('small').textContent = temperature > 0
        ? `${formatTemperature(temperature)} · ${stateLabel}`
        : stateLabel;
    });
  });
}

function gpioScriptAction(scriptID) {
  return `script:${scriptID}`;
}

function setupGPIOActions(scripts = gpioScripts, preserveValues = true) {
  document.querySelectorAll('.gpio-action').forEach((select) => {
    const prior = preserveValues ? select.value : 'none';
    select.replaceChildren();
    const builtInGroup = document.createElement('optgroup');
    builtInGroup.label = '内置动作';
    GPIO_ACTIONS.forEach(([value, label]) => {
      const option = document.createElement('option');
      option.value = value;
      option.textContent = label;
      builtInGroup.append(option);
    });
    select.append(builtInGroup);
    if (scripts.length) {
      const scriptGroup = document.createElement('optgroup');
      scriptGroup.label = 'Shell 脚本';
      scripts.forEach((script) => {
        const option = document.createElement('option');
        option.value = gpioScriptAction(script.id);
        option.textContent = `脚本：${script.name}`;
        scriptGroup.append(option);
      });
      select.append(scriptGroup);
    }
    select.value = [...select.options].some((option) => option.value === prior) ? prior : 'none';
  });
}

function showGPIOScriptMessage(message, error = false) {
  $('gpio-script-message').textContent = message;
  $('gpio-script-message').className = `inline-status${error ? ' error' : ''}`;
}

function generateGPIOScriptID() {
  if (window.crypto?.randomUUID) return window.crypto.randomUUID();
  return `script-${Date.now()}-${Math.random().toString(16).slice(2, 10)}`;
}

function renderGPIOScriptList() {
  const list = $('gpio-script-list');
  list.replaceChildren();
  if (!gpioScripts.length) {
    const empty = document.createElement('p');
    empty.className = 'gpio-script-empty';
    empty.textContent = '尚未创建脚本。点击“新增脚本”开始编写。';
    list.append(empty);
    return;
  }
  gpioScripts.forEach((script) => {
    const action = gpioScriptAction(script.id);
    const bindings = [...document.querySelectorAll('.gpio-action')]
      .filter((select) => select.value === action).length;
    const card = document.createElement('article');
    card.className = 'gpio-script-card';
    const name = document.createElement('strong');
    name.textContent = script.name;
    const detail = document.createElement('small');
    detail.textContent = `${new TextEncoder().encode(script.body).length} 字节 · ${bindings ? `已绑定 ${bindings} 项` : '尚未绑定'}`;
    const actions = document.createElement('div');
    actions.className = 'actions';
    const edit = document.createElement('button');
    edit.type = 'button';
    edit.textContent = '编辑';
    edit.addEventListener('click', () => openGPIOScriptEditor(script.id));
    const remove = document.createElement('button');
    remove.type = 'button';
    remove.className = 'delete-script';
    remove.textContent = '删除';
    remove.addEventListener('click', () => deleteGPIOScript(script.id));
    actions.append(edit, remove);
    card.append(name, detail, actions);
    list.append(card);
  });
}

function bashTokenClass(token) {
  if (token.startsWith('#')) return 'bash-token-comment';
  if (token.startsWith("'") || token.startsWith('"')) return 'bash-token-string';
  if (token.startsWith('$')) return 'bash-token-variable';
  if (/^\d+$/.test(token)) return 'bash-token-number';
  return 'bash-token-keyword';
}

function renderBashHighlight() {
  const textarea = $('gpio-script-body');
  const code = $('gpio-script-highlight').querySelector('code');
  const source = textarea.value;
  const pattern = /'(?:[^']*)'|"(?:\\.|[^"\\])*"|\$\{[^}\n]*\}|\$[A-Za-z_][A-Za-z0-9_]*|\$[0-9#?*@!_-]|#[^\n]*|\b(?:if|then|else|elif|fi|for|while|until|do|done|case|esac|in|function|select|time|coproc|break|continue|return|exit|local|readonly|declare|typeset|export|unset|shift|source|alias|unalias|set|trap|true|false)\b|\b\d+\b/g;
  code.replaceChildren();
  let offset = 0;
  for (const match of source.matchAll(pattern)) {
    if (match.index > offset) code.append(document.createTextNode(source.slice(offset, match.index)));
    const token = document.createElement('span');
    token.className = bashTokenClass(match[0]);
    token.textContent = match[0];
    code.append(token);
    offset = match.index + match[0].length;
  }
  code.append(document.createTextNode(`${source.slice(offset)}\n`));
  const bytes = new TextEncoder().encode(source).length;
  $('gpio-script-size').textContent = `${bytes} / ${GPIO_SCRIPT_MAX_BODY_BYTES} 字节`;
  $('gpio-script-size').className = bytes > GPIO_SCRIPT_MAX_BODY_BYTES ? 'error' : '';
  syncBashEditorScroll();
}

function syncBashEditorScroll() {
  const textarea = $('gpio-script-body');
  const highlight = $('gpio-script-highlight');
  highlight.scrollTop = textarea.scrollTop;
  highlight.scrollLeft = textarea.scrollLeft;
}

function openGPIOScriptEditor(scriptID = '') {
  const script = gpioScripts.find((item) => item.id === scriptID);
  gpioEditingScriptID = script?.id || generateGPIOScriptID();
  $('gpio-script-id').value = gpioEditingScriptID;
  $('gpio-script-name').value = script?.name || '';
  $('gpio-script-body').value = script?.body || '#!/usr/bin/env bash\nset -euo pipefail\n\n';
  $('gpio-script-editor').hidden = false;
  showGPIOScriptMessage(script ? `正在编辑“${script.name}”` : '正在创建新脚本');
  renderBashHighlight();
  $('gpio-script-name').focus();
}

function closeGPIOScriptEditor() {
  gpioEditingScriptID = '';
  $('gpio-script-editor').hidden = true;
  $('gpio-script-id').value = '';
  $('gpio-script-name').value = '';
  $('gpio-script-body').value = '';
  renderBashHighlight();
}

function commitGPIOScriptEditor() {
  if ($('gpio-script-editor').hidden) return true;
  const id = $('gpio-script-id').value;
  const name = $('gpio-script-name').value.trim();
  const body = $('gpio-script-body').value.replace(/\r\n?/g, '\n');
  const bodyBytes = new TextEncoder().encode(body).length;
  if (!name) {
    showGPIOScriptMessage('脚本名称不能为空。', true);
    $('gpio-script-name').focus();
    return false;
  }
  if (gpioScripts.some((script) => script.id !== id && script.name.trim().toLowerCase() === name.toLowerCase())) {
    showGPIOScriptMessage(`脚本名称“${name}”已经存在。`, true);
    $('gpio-script-name').focus();
    return false;
  }
  if (!body.trim()) {
    showGPIOScriptMessage('脚本内容不能为空。', true);
    $('gpio-script-body').focus();
    return false;
  }
  if (bodyBytes > GPIO_SCRIPT_MAX_BODY_BYTES) {
    showGPIOScriptMessage(`脚本内容不能超过 ${GPIO_SCRIPT_MAX_BODY_BYTES} 字节。`, true);
    return false;
  }
  const existingIndex = gpioScripts.findIndex((script) => script.id === id);
  if (existingIndex < 0 && gpioScripts.length >= GPIO_SCRIPT_MAX_COUNT) {
    showGPIOScriptMessage(`最多只能创建 ${GPIO_SCRIPT_MAX_COUNT} 个脚本。`, true);
    return false;
  }
  const script = { id, name, body };
  if (existingIndex >= 0) gpioScripts[existingIndex] = script;
  else gpioScripts.push(script);
  setupGPIOActions(gpioScripts, true);
  renderGPIOScriptList();
  closeGPIOScriptEditor();
  showGPIOScriptMessage(`脚本“${name}”已加入本页草稿；点击“保存按钮控制”后生效。`);
  return true;
}

function deleteGPIOScript(scriptID) {
  const script = gpioScripts.find((item) => item.id === scriptID);
  if (!script) return;
  const action = gpioScriptAction(scriptID);
  const binding = [...document.querySelectorAll('.gpio-action')].find((select) => select.value === action);
  if (binding) {
    showGPIOScriptMessage(`脚本“${script.name}”仍被按键动作引用，请先在上方下拉框改成其他动作。`, true);
    binding.focus();
    return;
  }
  if (!window.confirm(`确定删除脚本“${script.name}”吗？删除后仍需点击“保存按钮控制”才会持久化。`)) return;
  gpioScripts = gpioScripts.filter((item) => item.id !== scriptID);
  if (gpioEditingScriptID === scriptID) closeGPIOScriptEditor();
  setupGPIOActions(gpioScripts, true);
  renderGPIOScriptList();
  showGPIOScriptMessage(`脚本“${script.name}”已从本页草稿删除。`);
}

function activateTab(tabID, focus = false) {
  const tabs = [...document.querySelectorAll('[role="tab"]')];
  tabs.forEach((tab) => {
    const active = tab.id === tabID;
    tab.setAttribute('aria-selected', String(active));
    tab.tabIndex = active ? 0 : -1;
    const panel = $(tab.getAttribute('aria-controls'));
    if (panel) panel.hidden = !active;
    if (active && focus) tab.focus();
  });
  if (tabID === 'tab-fan') requestAnimationFrame(() => { CURVE_KINDS.forEach(renderFanChart); fetchHistory(); renderHistoryChart(); });
  if (tabID === 'tab-debug') requestAnimationFrame(() => { fetchHistory().then(renderSensorNamesList); });
}

function setupTabs() {
  const tabs = [...document.querySelectorAll('[role="tab"]')];
  tabs.forEach((tab, index) => {
    tab.addEventListener('click', () => activateTab(tab.id));
    tab.addEventListener('keydown', (event) => {
      let target = index;
      if (event.key === 'ArrowLeft') target = (index - 1 + tabs.length) % tabs.length;
      else if (event.key === 'ArrowRight') target = (index + 1) % tabs.length;
      else if (event.key === 'Home') target = 0;
      else if (event.key === 'End') target = tabs.length - 1;
      else return;
      event.preventDefault();
      activateTab(tabs[target].id, true);
    });
  });
}

function setupHealthTooltip() {
  const summary = document.querySelector('.health-summary');
  const badge = $('health');
  const tooltip = $('health-tooltip');
  const finePointer = () => window.matchMedia('(hover: hover) and (pointer: fine)').matches;
  const show = () => tooltip.classList.add('visible');
  const hide = () => tooltip.classList.remove('visible');
  const place = (clientX, clientY) => {
    const gap = 14;
    const margin = 8;
    const rect = tooltip.getBoundingClientRect();
    let left = clientX + gap;
    if (left + rect.width > window.innerWidth - margin) left = clientX - rect.width - gap;
    const top = Math.min(
      Math.max(margin, clientY - rect.height / 2),
      Math.max(margin, window.innerHeight - rect.height - margin),
    );
    tooltip.style.right = 'auto';
    tooltip.style.bottom = 'auto';
    tooltip.style.left = `${Math.max(margin, left)}px`;
    tooltip.style.top = `${top}px`;
  };
  const placeNearBadge = () => {
    const rect = badge.getBoundingClientRect();
    place(rect.right, rect.top + rect.height / 2);
  };
  summary.addEventListener('mouseenter', (event) => {
    show();
    place(event.clientX, event.clientY);
  });
  summary.addEventListener('mousemove', (event) => {
    show();
    place(event.clientX, event.clientY);
  });
  summary.addEventListener('mouseleave', hide);
  summary.addEventListener('focusin', () => {
    show();
    placeNearBadge();
  });
  summary.addEventListener('focusout', (event) => {
    if (!summary.contains(event.relatedTarget)) hide();
  });
  summary.addEventListener('click', (event) => {
    if (finePointer()) return;
    event.preventDefault();
    event.stopPropagation();
    if (tooltip.classList.contains('visible')) hide();
    else {
      show();
      placeNearBadge();
    }
  });
  document.addEventListener('click', (event) => {
    if (!summary.contains(event.target)) hide();
  });
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape') hide();
  });
}

function updateGPIOEnabledState() {
  const enabled = $('gpio-enabled').checked;
  document.querySelectorAll('.gpio-action').forEach((select) => { select.disabled = !enabled; });
}

function fillGPIOInputs(gpio = {}) {
  $('gpio-enabled').checked = Boolean(gpio.enabled);
  gpioScripts = Array.isArray(gpio.scripts)
    ? gpio.scripts.map((script) => ({ id: String(script.id), name: String(script.name), body: String(script.body) }))
    : [];
  setupGPIOActions(gpioScripts, false);
  const buttons = new Map((gpio.buttons || []).map((button) => [button.id, button]));
  document.querySelectorAll('.gpio-table tbody tr').forEach((row) => {
    const actions = buttons.get(row.dataset.button)?.actions || {};
    row.querySelectorAll('.gpio-action').forEach((select) => {
      select.value = actions[select.dataset.stage] || 'none';
    });
  });
  closeGPIOScriptEditor();
  renderGPIOScriptList();
  showGPIOScriptMessage(gpioScripts.length ? `已加载 ${gpioScripts.length} 个脚本。` : '尚未创建自定义脚本。');
  updateGPIOEnabledState();
}

function gpioConfigFromInputs() {
  return {
    version: 1,
    enabled: $('gpio-enabled').checked,
    scripts: gpioScripts.map((script) => ({ ...script })),
    buttons: [...document.querySelectorAll('.gpio-table tbody tr')].map((row) => {
      const actions = { short: 'none' };
      row.querySelectorAll('.gpio-action').forEach((select) => { actions[select.dataset.stage] = select.value; });
      return { id: row.dataset.button, actions };
    }),
  };
}

function curveFromInputs(kind = 'cpu') {
  return curveEditors[kind].curve.map((point) => ({ ...point }));
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

function curvePointLimits(kind, index) {
  const editor = curveEditors[kind];
  const previous = editor.curve[index - 1];
  const next = editor.curve[index + 1];
  return {
    minTemp: previous ? Math.floor(previous.temp_c) + 1 : 20,
    maxTemp: next ? Math.ceil(next.temp_c) - 1 : fanEmergencyTemperature(),
    minPWM: Math.max(fanMinimumPWM(), previous ? Math.round(previous.pwm_percent) : 0),
    maxPWM: next ? Math.round(next.pwm_percent) : 100,
  };
}

function setCurvePoint(kind, index, temperature, pwm) {
  const editor = curveEditors[kind];
  if (!editor.curve[index]) return;
  const limits = curvePointLimits(kind, index);
  const minTemp = Math.min(limits.minTemp, limits.maxTemp);
  const maxTemp = Math.max(limits.minTemp, limits.maxTemp);
  const minPWM = Math.min(limits.minPWM, limits.maxPWM);
  const maxPWM = Math.max(limits.minPWM, limits.maxPWM);
  editor.curve[index] = {
    temp_c: clamp(Math.round(temperature), minTemp, maxTemp),
    pwm_percent: clamp(Math.round(pwm), minPWM, maxPWM),
  };
}

function normalizeCurveToControls(kind) {
  const editor = curveEditors[kind];
  let priorPWM = fanMinimumPWM();
  editor.curve = editor.curve.map((point) => {
    const pwm = clamp(Math.max(Math.round(point.pwm_percent), priorPWM), priorPWM, 100);
    priorPWM = pwm;
    return { temp_c: Math.round(point.temp_c), pwm_percent: pwm };
  });
  const maximum = fanEmergencyTemperature();
  for (let index = editor.curve.length - 1; index >= 0; index -= 1) {
    const cap = maximum - (editor.curve.length - 1 - index);
    editor.curve[index].temp_c = Math.min(editor.curve[index].temp_c, cap);
  }
  for (let index = 0; index < editor.curve.length; index += 1) {
    const floor = index === 0 ? 20 : editor.curve[index - 1].temp_c + 1;
    editor.curve[index].temp_c = Math.max(editor.curve[index].temp_c, floor);
  }
  renderFanChart(kind);
}

function findCurveAddCandidate(kind) {
  const curve = curveEditors[kind].curve;
  if (curve.length >= CURVE_MAX_POINTS) return null;
  const maximum = fanEmergencyTemperature();
  let best = null;
  for (let index = 0; index <= curve.length; index += 1) {
    const lower = index === 0 ? 20 : Math.floor(curve[index - 1].temp_c) + 1;
    const upper = index === curve.length ? maximum : Math.ceil(curve[index].temp_c) - 1;
    if (lower > upper) continue;
    const width = upper - lower;
    if (best && width <= best.width) continue;
    const previous = curve[index - 1];
    const next = curve[index];
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

function updateCurveControls(kind) {
  const editor = curveEditors[kind];
  editor.selectedIndex = clamp(editor.selectedIndex, 0, Math.max(0, editor.curve.length - 1));
  const selected = editor.curve[editor.selectedIndex];
  $(editor.selectedID).textContent = selected
    ? `节点 ${editor.selectedIndex + 1}/${editor.curve.length} · ${selected.temp_c} °C · ${selected.pwm_percent}%`
    : '—';
  $(editor.addID).disabled = uiBusy || !findCurveAddCandidate(kind);
  $(editor.removeID).disabled = uiBusy || editor.curve.length <= CURVE_MIN_POINTS;
}

function addCurvePoint(kind) {
  const editor = curveEditors[kind];
  const candidate = findCurveAddCandidate(kind);
  if (!candidate) return;
  editor.curve.splice(candidate.index, 0, candidate.point);
  editor.selectedIndex = candidate.index;
  renderFanChart(kind);
}

function removeSelectedCurvePoint(kind) {
  const editor = curveEditors[kind];
  if (editor.curve.length <= CURVE_MIN_POINTS) return;
  editor.curve.splice(editor.selectedIndex, 1);
  editor.selectedIndex = clamp(editor.selectedIndex, 0, editor.curve.length - 1);
  renderFanChart(kind);
}

function svgElement(name, attributes = {}, text = '') {
  const element = document.createElementNS('http://www.w3.org/2000/svg', name);
  Object.entries(attributes).forEach(([key, value]) => element.setAttribute(key, String(value)));
  if (text) element.textContent = text;
  return element;
}

const CURVE_VIEWBOX = { width: 640, height: 260 };
const CURVE_TEXT_PX = 12;

function curveChartScale(svg) {
  const width = svg?.clientWidth || 0;
  const height = svg?.clientHeight || 0;
  if (!width || !height) return 1;
  return Math.max(0.25, Math.min(width / CURVE_VIEWBOX.width, height / CURVE_VIEWBOX.height));
}

// 轴刻度换算成 viewBox 单位后的实际宽度：窄屏时 SVG 内文字字号按 1/chartScale
// 放大以保持物理尺寸，留白必须按同一比例跟着放大。用 canvas 按页面字体栈测量。
let historyAxisMeasureCtx = null;
function historyAxisTextWidth(text, fontSize) {
  if (!historyAxisMeasureCtx) historyAxisMeasureCtx = document.createElement('canvas').getContext('2d');
  historyAxisMeasureCtx.font = `${fontSize}px "PingFang SC", "Microsoft YaHei UI", "Microsoft YaHei", system-ui, sans-serif`;
  return historyAxisMeasureCtx.measureText(text).width;
}

function updateCurveChartTextScale(svg) {
  if (!svg) return;
  const scale = curveChartScale(svg);
  const fontSize = CURVE_TEXT_PX / scale;
  svg.querySelectorAll('text').forEach((text) => text.setAttribute('font-size', fontSize));
  svg.querySelectorAll('.curve-node-label').forEach((label) => {
    // CSS class sets a presentation default; inline user-unit style wins and
    // keeps the halo at a steady visual width as the SVG is resized.
    label.style.strokeWidth = `${4 / scale}`;
  });
}

function renderFanChart(kind = 'cpu') {
  const editor = curveEditors[kind];
  const svg = $(editor.chartID);
  svg.classList.add(`curve-chart-${kind}`);
  const curve = curveFromInputs(kind);
  if (curve.some((point) => !Number.isFinite(point.temp_c) || !Number.isFinite(point.pwm_percent))) return;
  const { right, top, bottom } = CHART;
  const chartScale = curveChartScale(svg);
  const textSize = CURVE_TEXT_PX / chartScale;
  // 左轴最宽刻度固定是 100%；窄屏下 SVG 内字号放大，左留白同步放大，
  // 否则转速刻度被 viewBox 裁掉。三个曲线编辑器几何一致，fanPlotBox 共用。
  const axisPad = 8 / chartScale;
  const left = Math.max(CHART.left, axisPad + historyAxisTextWidth('100%', textSize) + 2 / chartScale);
  fanPlotBox = { left, right, top, bottom };
  const x = (temp) => left + ((clamp(temp, 20, 100) - 20) / 80) * (right - left);
  const y = (speed) => bottom - ((clamp(speed, 30, 100) - 30) / 70) * (bottom - top);
  svg.replaceChildren();

  // 横轴标签行高随字号放大，低于绘图区底线时下移，避免顶到网格线；
  // 标签行与纵轴刻度有垂直重叠时，端部标签压到刻度列就向图内锚定
  const xLabelBase = Math.max(CURVE_VIEWBOX.height - 14 / chartScale, bottom + textSize * 0.75 + 1 / chartScale);
  const xRowNearGutter = xLabelBase < bottom + textSize * 1.07;
  [20, 40, 60, 80, 100].forEach((temp) => {
    svg.append(svgElement('line', { x1: x(temp), y1: top, x2: x(temp), y2: bottom, class: 'chart-grid-line' }));
    const label = `${temp}°`;
    const lx = x(temp);
    const halfLabel = historyAxisTextWidth(label, textSize) / 2;
    let anchor = 'middle';
    if (xRowNearGutter && lx - halfLabel < left - axisPad) anchor = 'start';
    else if (xRowNearGutter && lx + halfLabel > right + axisPad) anchor = 'end';
    svg.append(svgElement('text', { x: lx, y: xLabelBase, class: 'chart-axis-text', 'font-size': textSize, 'text-anchor': anchor }, label));
  });
  [30, 50, 70, 100].forEach((speed) => {
    svg.append(svgElement('line', { x1: left, y1: y(speed), x2: right, y2: y(speed), class: 'chart-grid-line' }));
    svg.append(svgElement('text', { x: left - axisPad, y: y(speed) + textSize * 0.35, class: 'chart-axis-text', 'font-size': textSize, 'text-anchor': 'end' }, `${speed}%`));
  });
  // 线宽与节点尺寸除以 chartScale：viewBox 会被容器拉伸，除以缩放后才是目标真实像素（同文字的处理）
  svg.append(svgElement('polyline', {
    points: curve.map((point) => `${x(point.temp_c)},${y(point.pwm_percent)}`).join(' '),
    fill: 'none', stroke: editor.color, 'stroke-width': 2.5 / chartScale, 'stroke-linecap': 'round', 'stroke-linejoin': 'round',
  }));
  const actualTemperatures = {
    cpu: currentStatus?.fan_control?.cpu_temperature_c ?? currentStatus?.fan_control?.temperature_c,
    hdd: currentStatus?.fan_control?.hdd_temperature_c ?? currentStatus?.fan_control?.disk_temperature_c,
    nvme: currentStatus?.fan_control?.nvme_temperature_c,
  };
  const actualTemp = Number(actualTemperatures[kind]);
  if (Number.isFinite(actualTemp) && actualTemp > 0) {
    const currentLabel = `当前 ${actualTemp.toFixed(1)}°C`;
    const currentLabelWidth = Math.max(72 / chartScale, currentLabel.length * textSize * 0.6);
    const currentX = clamp(x(actualTemp), currentLabelWidth / 2 + 4 / chartScale, CURVE_VIEWBOX.width - currentLabelWidth / 2 - 4 / chartScale);
    svg.append(svgElement('line', { x1: x(actualTemp), y1: top, x2: x(actualTemp), y2: bottom, class: 'chart-now-line', 'stroke-width': 1.5 / chartScale, 'stroke-dasharray': '6 5' }));
    svg.append(svgElement('text', { x: currentX, y: textSize + 4 / chartScale, class: 'chart-now-label', 'font-size': textSize, 'text-anchor': 'middle' }, currentLabel));
  }
  curve.forEach((point, index) => {
    const selected = index === editor.selectedIndex;
    const nodeX = x(point.temp_c);
    const nodeY = y(point.pwm_percent);
    const hitTarget = svgElement('circle', {
      cx: nodeX, cy: nodeY, r: 20,
      fill: 'transparent', stroke: 'transparent', 'pointer-events': 'all',
      class: 'curve-node curve-node-hit', 'data-index': index,
      'aria-hidden': true, focusable: false,
    });
    svg.append(hitTarget);
    const node = svgElement('circle', {
      // 真实半径约 4px（选中 5px）；命中热区 r=20 刻意不缩放，保证触控
      cx: nodeX, cy: nodeY, r: (selected ? 5 : 4) / chartScale,
      stroke: editor.color, 'stroke-width': 2 / chartScale,
      class: `curve-node curve-node-control${selected ? ' selected' : ''}`, 'data-index': index,
      tabindex: 0, role: 'button', 'aria-label': `节点 ${index + 1}，${point.temp_c} 摄氏度，转速 ${point.pwm_percent}%`,
    });
    svg.append(node);
    const label = `${point.temp_c}° · ${point.pwm_percent}%`;
    const labelWidth = Math.max(72 / chartScale, label.length * textSize * 0.6);
    const labelX = clamp(nodeX, 8 / chartScale + labelWidth / 2, CURVE_VIEWBOX.width - 8 / chartScale - labelWidth / 2);
    const labelY = nodeY < 50 / chartScale ? nodeY + 26 / chartScale : nodeY - 16 / chartScale;
    svg.append(svgElement('text', {
      x: labelX, y: labelY,
      'font-size': 12, 'font-weight': selected ? 800 : 600, 'text-anchor': 'middle', class: `curve-node-label${selected ? ' selected' : ''}`,
    }, label));
  });
  updateCurveChartTextScale(svg);
  updateCurveControls(kind);
}

/* ---- 历史温度：后台每分钟采样（GET api/history?range=小时数），服务端按峰值聚合 ---- */

// 父类分组：勾选父类 = 整组曲线显隐；▾ 弹窗勾选组内子类（CPU/SATA/NVMe 弹窗
// 内含“取最高”聚合项）。
const HISTORY_GROUPS = [
  { key: 'cpu', label: 'CPU', color: '#3f6ff5', aggregateLabel: '取最高（核心最高）' },
  { key: 'sata', label: 'SATA', color: '#18a779', aggregateLabel: 'SATA 取最高' },
  { key: 'nvme', label: 'NVMe', color: '#a25bd7', aggregateLabel: 'NVMe 取最高' },
  { key: 'gpu', label: 'GPU', color: '#e05252' },
  { key: 'nic', label: '网卡', color: '#e6a53c' },
  { key: 'other', label: '其它', color: '#2aa8a8' },
  { key: 'fan', label: '风扇', color: '#6e7780' }, // 中性灰：与温度传感器色系（蓝/绿/紫/红/琥珀/青）明显区分
];
const HISTORY_DEFAULT_HOURS = 0.5; // 默认 30 分钟档；用户选过则优先用记住的档位
const HISTORY_FETCH_TTL = 45000;
const HISTORY_RANGE_STORAGE_KEY = 'tad-history-range';
// 子类色从父类色派生：同色相、明度阶梯 ±8%、小幅交替色相偏移，
// 保证同组曲线同色系且可区分。
function historyHexToHsl(hex) {
  const value = hex.replace('#', '');
  const r = parseInt(value.slice(0, 2), 16) / 255;
  const g = parseInt(value.slice(2, 4), 16) / 255;
  const b = parseInt(value.slice(4, 6), 16) / 255;
  const max = Math.max(r, g, b);
  const min = Math.min(r, g, b);
  const l = (max + min) / 2;
  if (max === min) return { h: 0, s: 0, l };
  const d = max - min;
  const s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
  let h;
  if (max === r) h = ((g - b) / d + (g < b ? 6 : 0)) / 6;
  else if (max === g) h = ((b - r) / d + 2) / 6;
  else h = ((r - g) / d + 4) / 6;
  return { h: h * 360, s, l };
}

function historyChildShade(baseHex, index) {
  const { h, s, l } = historyHexToHsl(baseHex);
  const step = Math.ceil((index + 1) / 2) * 0.08; // 0.08, 0.16, 0.24 …
  const lighten = index % 2 === 0;
  const lightness = Math.min(0.85, Math.max(0.15, l + (lighten ? step : -step)));
  const hue = (h + (lighten ? 1 : -1) * Math.min(Math.ceil((index + 1) / 2), 3) * 5 + 360) % 360;
  return `hsl(${Math.round(hue)}, ${Math.round(Math.max(0.25, s) * 100)}%, ${Math.round(lightness * 100)}%)`;
}

// 风扇组子类不走 HSL 派生：灰色基色会被饱和度下限（0.25）强行染色，
// 改用独立灰色阶梯按子类序号取色。前四档（TAD6S4N 四风扇）保证深浅主题
// 都清晰：深色背景 rgb(20,21,25) 下过暗的灰（如 #4a5058）会与网格线混在一起。
const HISTORY_FAN_SHADES = ['#6e7780', '#97a0ab', '#b6bec8', '#8a919c', '#aab2bc', '#5b626b', '#cfd4da', '#454c54'];
const HISTORY_FAN_MAX_PERCENT = 100;

let historyCache = null;
let historyFetchedAt = 0;
let historyRangeHours = loadHistoryRangeHours(); // 上次使用的范围（localStorage 记忆）
let historyRangedSamples = [];   // 当前范围采样（与图上折线一致）
let historyYBoundsState = { lo: 20, hi: 90 }; // 渲染时缓存，供十字线取点
let historyPlotBox = { ...CHART }; // 渲染时缓存绘图区（窄屏下左右边距会随字号放大），供十字线换算
let fanPlotBox = { ...CHART }; // 曲线编辑器渲染时缓存绘图区（窄屏下左边距随字号放大），供拖拽换算
let historyLastCursorEvent = null; // 最后悬停位置，自动刷新重绘后恢复十字线
let historyOpenPopoverKey = null; // 当前展开的父类弹窗
let historyLegendIdentity = '';   // 图例内容标识，未变化时不重建（保护展开弹窗）

let historySeriesEnabled = loadHistorySeriesEnabled(); // 父类开关，缺省全开
// 只读访问器（渲染与测试用）：直接写 historySeriesEnabled 的路径集中在 setHistoryGroupEnabled。
function historySeriesEnabledFor(groupKey) {
  return historySeriesEnabled[groupKey] !== false;
}
let historyChildSelection = loadHistoryChildSelection(); // 组内勾选，null=全部显示

function loadHistorySeriesEnabled() {
  try {
    const raw = JSON.parse(window.localStorage.getItem('tad-history-series') || '{}');
    return raw && typeof raw === 'object' ? raw : {};
  } catch (error) { return {}; }
}

function saveHistorySeriesEnabled() {
  try { window.localStorage.setItem('tad-history-series', JSON.stringify(historySeriesEnabled)); }
  catch (error) { /* 忽略写入失败 */ }
}

function loadHistoryChildSelection() {
  try {
    const raw = JSON.parse(window.localStorage.getItem('tad-history-children') || '{}');
    const state = raw && typeof raw === 'object' ? raw : {};
    // 迁移旧的单盘勾选（sata/nvme 组）
    if (!state.sata && !state.nvme) {
      const legacy = JSON.parse(window.localStorage.getItem('tad-history-disks') || 'null');
      if (Array.isArray(legacy)) {
        state.sata = legacy.filter((id) => id.startsWith('front-'));
        state.nvme = legacy.filter((id) => id.startsWith('m2-'));
      }
    }
    return state;
  } catch (error) { return {}; }
}

function saveHistoryChildSelection() {
  try {
    const plain = {};
    for (const [key, value] of Object.entries(historyChildSelection)) {
      plain[key] = value === null ? null : [...value];
    }
    window.localStorage.setItem('tad-history-children', JSON.stringify(plain));
  } catch (error) { /* 忽略写入失败 */ }
}

// ---- 时间范围：拖动条（30分–2时无极 + 固定挡位，7天/30天并入挡位）----

// 滑杆双段设计（用户确认）：前 30% 是 30 分钟–2 小时的无极区（按 1 分钟
// 粒度吸附，0.3 分钟/位），后 70% 每 140 位一个固定挡位（6时/12时/24时 +
// 按保存天数显隐的 7天/30天），拇指落入挡位段即吸附到段中心的停靠位，
// 刻度画在停靠位上。挡位总数由 updateHistoryRangeAvailability 维护。
const HISTORY_CONT_POSITIONS = 400; // 无极区占用的滑杆位置数（总行程的 40%）
const HISTORY_STOP_SPAN = 120;      // 每个固定挡位占用的位置数
const HISTORY_CONT_MIN_HOURS = 0.5;
const HISTORY_CONT_MAX_HOURS = 2;
const HISTORY_BASE_STOPS = [6, 12, 24];
const HISTORY_ALL_STOPS = [6, 12, 24, 168, 720];

// 当前滑杆上的固定挡位数量（3=仅基础挡；保存天数 ≥7 加 7 天，≥30 加 30 天）
let historySliderStopCount = HISTORY_ALL_STOPS.length;

function historyStopsFor(stopCount) {
  return HISTORY_ALL_STOPS.slice(0, clamp(Math.round(stopCount) || HISTORY_ALL_STOPS.length, 3, HISTORY_ALL_STOPS.length));
}

// 挡位序号 → 拇指停靠位（挡位段中心）
function historyStopCenterPos(stopIndex) {
  return HISTORY_CONT_POSITIONS + stopIndex * HISTORY_STOP_SPAN + HISTORY_STOP_SPAN / 2;
}

// 滑杆位置 → 小时。无极区按 1 分钟粒度吸附；挡位区整段吸附到对应挡位。
function historyPosToHours(pos) {
  const max = historySliderStopCount > 0 ? historyStopCenterPos(historySliderStopCount - 1) : HISTORY_CONT_POSITIONS;
  const position = Math.round(clamp(Number(pos) || 0, 0, max));
  if (position <= HISTORY_CONT_POSITIONS) {
    const minutes = Math.round(HISTORY_CONT_MIN_HOURS * 60 + (position / HISTORY_CONT_POSITIONS) * (HISTORY_CONT_MAX_HOURS - HISTORY_CONT_MIN_HOURS) * 60);
    return minutes / 60;
  }
  const stops = historyStopsFor(historySliderStopCount);
  const index = Math.min(stops.length - 1, Math.floor((position - HISTORY_CONT_POSITIONS - 1) / HISTORY_STOP_SPAN));
  return stops[index];
}

// 小时 → 滑杆位置。无极区反解到吸附位（1 分钟粒度往返一致）；挡位取停靠位。
function historyHoursToPos(hours) {
  const value = Number(hours);
  if (!Number.isFinite(value)) return 0;
  if (value <= HISTORY_CONT_MAX_HOURS) {
    const minutes = clamp(value, HISTORY_CONT_MIN_HOURS, HISTORY_CONT_MAX_HOURS) * 60;
    return Math.round((minutes - HISTORY_CONT_MIN_HOURS * 60) / ((HISTORY_CONT_MAX_HOURS - HISTORY_CONT_MIN_HOURS) * 60 / HISTORY_CONT_POSITIONS));
  }
  const stops = historyStopsFor(historySliderStopCount);
  let best = 0;
  let bestDelta = Infinity;
  stops.forEach((stop, index) => {
    const delta = Math.abs(stop - value);
    if (delta < bestDelta) { best = index; bestDelta = delta; }
  });
  return historyStopCenterPos(best);
}

// 范围值钳制到合法值：7 天/30 天原样保留；≤2 小时按 1 分钟粒度钳进无极区；
// 2 小时以上的旧值（含旧版半小时值）吸附到最近固定挡位。这里用全套挡位，
// 与保存天数显隐无关——超出保留期的值由 updateHistoryRangeAvailability 回落。
function normalizeHistoryRangeHours(hours) {
  const value = Number(hours);
  if (Number.isFinite(value) && (value === 168 || value === 720)) return value;
  if (!Number.isFinite(value)) return HISTORY_DEFAULT_HOURS;
  if (value <= HISTORY_CONT_MAX_HOURS) {
    const minutes = clamp(value, HISTORY_CONT_MIN_HOURS, HISTORY_CONT_MAX_HOURS) * 60;
    return Math.round(minutes) / 60;
  }
  let best = HISTORY_BASE_STOPS[0];
  let bestDelta = Infinity;
  HISTORY_ALL_STOPS.forEach((stop) => {
    const delta = Math.abs(stop - value);
    if (delta < bestDelta) { best = stop; bestDelta = delta; }
  });
  return best;
}

function loadHistoryRangeHours() {
  try {
    const raw = window.localStorage.getItem(HISTORY_RANGE_STORAGE_KEY);
    if (raw === null || raw === '') return HISTORY_DEFAULT_HOURS;
    return normalizeHistoryRangeHours(parseFloat(raw));
  } catch (error) { return HISTORY_DEFAULT_HOURS; }
}

function saveHistoryRangeHours(hours) {
  try { window.localStorage.setItem(HISTORY_RANGE_STORAGE_KEY, String(hours)); }
  catch (error) { /* 隐私模式等场景下仅本次生效 */ }
  // 档位同时记到后端配置（随 /api/status 的 config.ui_prefs 下发，跨设备一致）；
  // 保存失败静默——localStorage 兜底足够，不打扰用户
  request('api/config/ui-prefs', { method: 'POST', body: JSON.stringify({ history_range_hours: hours }) }).catch(() => {});
}

// 范围标签：不足 1 小时显示分钟；无极区非整小时显示"N 小时 M 分"；
// 固定挡位显示 N 小时 / N 天。
function historyRangeLabel(hours) {
  if (hours === 168) return '7 天';
  if (hours === 720) return '30 天';
  if (hours < 1) return `${Math.round(hours * 60)} 分钟`;
  if (Number.isInteger(hours)) return `${hours} 小时`;
  const whole = Math.floor(hours);
  const minutes = Math.round((hours - whole) * 60);
  return minutes > 0 ? `${whole} 小时 ${minutes} 分` : `${whole} 小时`;
}

function historyChildSelectionFor(groupKey) {
  const selection = historyChildSelection[groupKey];
  if (selection === null || selection === undefined) return null; // null = 全部显示（跟随数据）
  return selection instanceof Set ? selection : new Set(selection); // 统一为 Set，调用方用 has()
}

function setHistoryChildSelection(groupKey, selection) {
  historyChildSelection[groupKey] = selection;
  saveHistoryChildSelection();
}

// 风扇 ID 缩写：it8613:it87.2608:fan3 → fan3
function historyFanLabel(fanID) {
  return String(fanID || '').slice(String(fanID).lastIndexOf(':') + 1);
}

// 保留最近 rangeHours 小时内的采样（ts 为 unix 秒）。
function historyFilterRange(samples, rangeHours, nowTs) {
  const cutoff = nowTs - rangeHours * 3600;
  return samples.filter((sample) => sample.ts >= cutoff);
}

// 兜底抽稀（正常情况下服务端已聚合到 ≤480 点）：超出上限按固定步长取样
// 并始终保留最后一个点。
function historyThinOut(samples, maxPoints = 480) {
  if (samples.length <= maxPoints) return samples.slice();
  const stride = Math.ceil(samples.length / maxPoints);
  const out = [];
  for (let i = 0; i < samples.length; i += stride) out.push(samples[i]);
  const last = samples[samples.length - 1];
  if (out[out.length - 1] !== last) out.push(last);
  return out;
}

// 渲染抽稀：2 小时以上的范围按 5 抽 1、6 小时以上按 15 抽 1 收敛折线点数
// （每条曲线少一个数量级的 DOM 节点）；2 小时以内（无极区）保持全精度。
// 必须在断口切分之后、按段执行——先抽稀会把点距拉大过断口阈值，停机断口
// 会被误并回连续线。
function historyDecimationStride(rangeHours) {
  if (rangeHours > 6) return 15;
  if (rangeHours > 2) return 5;
  return 1;
}

// 按步长抽稀一段折线：保留第 0/stride/2×stride… 个点，且始终保留最后一个
// 点（曲线末端反映最新状态）。
function historyThinByStride(samples, stride) {
  if (stride <= 1 || samples.length <= stride) return samples;
  const out = samples.filter((_, index) => index % stride === 0);
  const last = samples[samples.length - 1];
  if (out[out.length - 1] !== last) out.push(last);
  return out;
}

// 数据断口（插件停运/重启）：相邻点间隔超过 2.5 倍采样周期时断开折线。
// 必须在抽稀前按原始间隔判断，抽稀会人为拉大点距。
function historySplitSegments(samples, intervalSeconds) {
  const segments = [];
  let current = [];
  for (let i = 0; i < samples.length; i++) {
    if (current.length && samples[i].ts - current[current.length - 1].ts > intervalSeconds * 2.5) {
      segments.push(current);
      current = [];
    }
    current.push(samples[i]);
  }
  if (current.length) segments.push(current);
  return segments;
}

// 生成覆盖 [lo, hi] 的好看刻度（步长取 1/2/5 × 10^n）。
function historyNiceTicks(lo, hi, targetCount = 5) {
  if (!(hi > lo)) return [];
  const rawStep = (hi - lo) / Math.max(1, targetCount);
  const magnitude = Math.pow(10, Math.floor(Math.log10(rawStep)));
  let step = magnitude;
  for (const multiplier of [1, 2, 5, 10]) {
    if (magnitude * multiplier >= rawStep) { step = magnitude * multiplier; break; }
  }
  const ticks = [];
  for (let value = Math.ceil(lo / step) * step; value <= hi + 1e-9; value += step) {
    ticks.push(Math.round(value * 1e6) / 1e6);
  }
  return ticks;
}

// 温度轴范围：无数据用兜底区间；有数据时下界再放 5 度并按 5 度取整
// （最低 35 度 → 从 30 开始），上界留 2 度余量。
function historyYBounds(values, fallbackLo, fallbackHi) {
  const defined = values.filter((value) => Number.isFinite(value) && value > 0);
  if (!defined.length) return { lo: fallbackLo, hi: fallbackHi };
  const min = Math.min(...defined);
  const max = Math.max(...defined);
  const lo = Math.max(0, Math.floor((min - 5) / 5) * 5);
  let hi = Math.ceil((max + 2) / 5) * 5;
  if (hi - lo < 10) hi = lo + 10;
  return { lo, hi };
}

// 时间轴刻度：各档位固定步长（30 分钟→1 分钟、1 小时→10 分钟、2 小时→30 分钟、
// 6 小时→1 小时、12 小时→3 小时、24 小时→2 小时、7 天→6 小时、30 天→1 天），
// 未知范围退回自动算法。2/12 小时步长按 4–5 条网格线取，与其它档位密度一致。
const HISTORY_TICK_STEP_SECONDS = { 0.5: 60, 1: 600, 2: 1800, 6: 3600, 12: 10800, 24: 7200, 168: 21600, 720: 86400 };
function historyTimeTicks(startSec, endSec, rangeHours) {
  if (!(endSec > startSec)) return [];
  let step = HISTORY_TICK_STEP_SECONDS[rangeHours];
  if (!step) {
    const steps = [60, 120, 300, 600, 900, 1800, 3600, 7200, 10800, 14400, 21600, 43200, 86400, 172800, 259200, 432000];
    step = steps[steps.length - 1];
    for (const candidate of steps) {
      if ((endSec - startSec) / candidate <= 7) { step = candidate; break; }
    }
  }
  const ticks = [];
  for (let ts = Math.ceil(startSec / step) * step; ts <= endSec; ts += step) ticks.push(ts);
  return ticks;
}

function historyFormatClock(ts) {
  const date = new Date(ts * 1000);
  return `${String(date.getHours()).padStart(2, '0')}:${String(date.getMinutes()).padStart(2, '0')}`;
}

// 刻度步长 ≥ 1 天时显示日期（如 9/15），否则显示时分。
function historyTickLabel(ts, stepSeconds) {
  if (stepSeconds >= 86400) {
    const date = new Date(ts * 1000);
    return `${date.getMonth() + 1}/${date.getDate()}`;
  }
  return historyFormatClock(ts);
}

// 单系列转点集：value 缺失（0/NaN，休眠或空置）的点直接剔除。
function historySeriesPoints(samples, pick) {
  return samples.filter((sample) => {
    const value = pick(sample);
    return Number.isFinite(value) && value > 0;
  }).map((sample) => ({ ts: sample.ts, value: pick(sample) }));
}

// ---- 分组子类 ----
// 返回每个父类当前数据下的子类 ID 列表（含特殊聚合项 __agg__）。

function historyDiskIDs(samples) {
  const ids = [];
  samples.forEach((sample) => (sample.disks || []).forEach((disk) => {
    if (disk.id && !ids.includes(disk.id)) ids.push(disk.id);
  }));
  return ids.sort();
}

// 有过 rpm>0 或 pwm>0 的才算有效风扇（没接风扇的空通道全 0，排除）。
function historyFanIDs(samples) {
  const fanIDs = [];
  const hasSignal = {};
  samples.forEach((sample) => (sample.fans || []).forEach((fan) => {
    if (!fan.id) return;
    if (!fanIDs.includes(fan.id)) fanIDs.push(fan.id);
    if (fan.rpm > 0 || fan.pwm_percent > 0) hasSignal[fan.id] = true;
  }));
  return fanIDs.filter((id) => hasSignal[id]);
}

function historySensorsOfGroup(samples, group) {
  const ids = [];
  samples.forEach((sample) => (sample.sensors || []).forEach((sensor) => {
    if (sensor.group === group && sensor.key && !ids.includes(sensor.key)) ids.push(sensor.key);
  }));
  return ids.sort();
}

function historyGroupChildIDs(groupKey, samples) {
  switch (groupKey) {
    case 'cpu': return ['__agg__', ...historySensorsOfGroup(samples, 'cpu')];
    case 'sata': return ['__agg__', ...historyDiskIDs(samples).filter((id) => id.startsWith('front-'))];
    case 'nvme': return ['__agg__', ...historyDiskIDs(samples).filter((id) => id.startsWith('m2-'))];
    case 'gpu': return historySensorsOfGroup(samples, 'gpu');
    case 'nic': return historySensorsOfGroup(samples, 'nic');
    case 'other': return historySensorsOfGroup(samples, 'other');
    case 'fan': return historyFanIDs(samples);
    default: return [];
  }
}

// 子类取值：__agg__ → 组聚合值；其余 → 按各自数据源取值（缺失剔除）。
function historyChildValue(groupKey, childID, sample) {
  if (childID === '__agg__') {
    if (groupKey === 'cpu') return sample.cpu_c;
    if (groupKey === 'sata') return sample.hdd_c;
    if (groupKey === 'nvme') return sample.nvme_c;
    return NaN;
  }
  if (groupKey === 'cpu' || groupKey === 'gpu' || groupKey === 'nic' || groupKey === 'other') {
    const sensor = (sample.sensors || []).find((item) => item.group === groupKey && item.key === childID);
    return sensor ? sensor.c : NaN;
  }
  if (groupKey === 'sata' || groupKey === 'nvme') {
    const disk = (sample.disks || []).find((item) => item.id === childID);
    return disk ? disk.c : NaN;
  }
  if (groupKey === 'fan') {
    const fan = (sample.fans || []).find((item) => item.id === childID);
    return fan ? fan.pwm_percent : NaN;
  }
  return NaN;
}

function historyChildLabel(groupKey, childID) {
  if (childID === '__agg__') {
    const group = HISTORY_GROUPS.find((item) => item.key === groupKey);
    return group?.aggregateLabel || '取最高';
  }
  if (groupKey === 'cpu' || groupKey === 'gpu' || groupKey === 'nic' || groupKey === 'other') {
    const custom = sensorDisplayName(childID);
    if (custom) return custom;
    return groupKey === 'cpu' ? childID : String(childID).split(':').pop() + `（${String(childID).split(':')[0] || groupKey}）`;
  }
  if (groupKey === 'sata' || groupKey === 'nvme') return historySlotLabel(childID);
  if (groupKey === 'fan') return historyFanLabel(childID);
  return childID;
}

// 传感器显示名：用户在调试页设置（存后端配置），未设置时退回默认短名。
function sensorDisplayName(sensorKey) {
  const custom = currentStatus?.config?.sensor_names?.[sensorKey];
  return custom && custom.trim() ? custom.trim() : null;
}

// 槽位短名：front-2 → 前置2，m2-1 → M.2 1
function historySlotLabel(slotID) {
  const match = /^(front|m2)-([1-9])$/.exec(slotID || '');
  if (!match) return slotID;
  return match[1] === 'front' ? `前置${match[2]}` : `M.2 ${match[2]}`;
}

function historyChildColor(groupKey, childID, allIDs) {
  const group = HISTORY_GROUPS.find((item) => item.key === groupKey);
  const base = group ? group.color : '#3f6ff5';
  if (childID === '__agg__') return base;
  const index = allIDs.indexOf(childID);
  // 风扇组走独立灰色阶梯（中性灰不参与 HSL 派生）
  if (groupKey === 'fan') return HISTORY_FAN_SHADES[Math.max(0, index) % HISTORY_FAN_SHADES.length];
  return historyChildShade(base, index);
}

// 组曲线：父类开关 + 子类勾选共同决定可见性；聚合项视为一个普通子类。
function historyGroupSeries(groupKey, samples, intervalSeconds) {
  const childIDs = historyGroupChildIDs(groupKey, samples);
  const selection = historyChildSelectionFor(groupKey);
  const visible = (childID) => selection === null || selection.has(childID);
  return childIDs
    .filter((childID) => visible(childID))
    .map((childID) => {
      const points = historySeriesPoints(samples, (sample) => historyChildValue(groupKey, childID, sample));
      return {
        id: `${groupKey}:${childID}`,
        color: historyChildColor(groupKey, childID, childIDs),
        segments: historySplitSegments(points, intervalSeconds)
          .map((segment) => historyThinByStride(segment, historyDecimationStride(historyRangeHours)))
          .map((segment) => historyThinOut(segment)),
      };
    });
}

// 单图双轴：左轴温度（动态范围），右轴风扇 0-100%。
function renderHistoryChart() {
  const svg = $('history-chart');
  if (!svg) return;
  const status = $('history-status');
  const data = historyCache;
  const samples = data?.samples || [];
  if (!samples.length) {
    svg.replaceChildren();
    renderHistoryLegend();
    if (status) status.textContent = '正在采样，曲线会随时间慢慢生成。';
    return;
  }
  const intervalSeconds = Math.max(1, Number(data.interval_seconds) || 60);
  const nowTs = Math.floor(Date.now() / 1000);
  const startSec = nowTs - historyRangeHours * 3600;
  const ranged = historyFilterRange(samples, historyRangeHours, nowTs);
  historyRangedSamples = ranged;

  // 逐父类构建曲线；温度类（CPU/SATA/NVMe/网卡/其它）走左轴，风扇走右轴
  const tempLines = [];
  const fanLines = [];
  HISTORY_GROUPS.forEach((group) => {
    if (historySeriesEnabled[group.key] === false) return;
    const lines = historyGroupSeries(group.key, ranged, intervalSeconds);
    lines.forEach((line) => {
      line.yScale = group.key === 'fan' ? 'fan' : 'temp';
      (group.key === 'fan' ? fanLines : tempLines).push(line);
    });
  });

  const tempValues = tempLines.flatMap((line) => line.segments.flat().map((point) => point.value));
  const bounds = historyYBounds(tempValues, 20, 90);
  historyYBoundsState = bounds;

  const chartScale = curveChartScale(svg);
  const textSize = CURVE_TEXT_PX / chartScale;
  const tempTicks = historyNiceTicks(bounds.lo, bounds.hi, 4);
  // 左右留白按最宽轴刻度的实际宽度放大（右轴最宽固定是 100%）；
  // 桌面 scale≥1 时不小于 CHART 原值，布局保持不变。
  const axisPad = 8 / chartScale;
  const leftLabelWidth = Math.max(0, ...tempTicks.map((tick) => historyAxisTextWidth(`${tick}°`, textSize)));
  const plot = {
    left: Math.max(CHART.left, axisPad + leftLabelWidth),
    right: Math.min(CHART.right, CURVE_VIEWBOX.width - axisPad - historyAxisTextWidth('100%', textSize)),
    top: CHART.top,
    bottom: CHART.bottom,
  };
  historyPlotBox = plot;
  const x = (ts) => plot.left + ((clamp(ts, startSec, nowTs) - startSec) / (historyRangeHours * 3600)) * (plot.right - plot.left);
  const yTemp = (value) => plot.bottom - ((clamp(value, bounds.lo, bounds.hi) - bounds.lo) / (bounds.hi - bounds.lo)) * (plot.bottom - plot.top);
  const yFan = (percent) => plot.bottom - (clamp(percent, 0, HISTORY_FAN_MAX_PERCENT) / HISTORY_FAN_MAX_PERCENT) * (plot.bottom - plot.top);
  svg.replaceChildren();

  tempTicks.forEach((tick) => {
    svg.append(svgElement('line', { x1: plot.left, y1: yTemp(tick), x2: plot.right, y2: yTemp(tick), class: 'chart-grid-line' }));
    svg.append(svgElement('text', { x: plot.left - axisPad, y: yTemp(tick) + textSize * 0.35, class: 'chart-axis-text', 'font-size': textSize, 'text-anchor': 'end' }, `${tick}°`));
  });
  [0, 25, 50, 75, 100].forEach((tick) => {
    svg.append(svgElement('text', { x: plot.right + axisPad, y: yFan(tick) + textSize * 0.35, class: 'chart-axis-text', 'font-size': textSize, 'text-anchor': 'start' }, `${tick}%`));
  });
  const timeTicks = historyTimeTicks(startSec, nowTs, historyRangeHours);
  const tickStep = timeTicks.length > 1 ? timeTicks[1] - timeTicks[0] : 0;
  // 网格线按档位步长全画；刻度文字过密时抽稀，可容纳的标签数随缩放比例减少
  const maxLabels = Math.min(10, Math.max(5, Math.round(10 * chartScale)));
  const labelEvery = Math.max(1, Math.ceil(timeTicks.length / maxLabels));
  // 窄屏下横轴标签行高随字号放大，低于绘图区底线时下移，避免顶到网格线；
  // 标签行与两侧轴刻度有垂直重叠时，端部标签压到刻度列就向图内锚定
  const xLabelBase = Math.max(CURVE_VIEWBOX.height - 14 / chartScale, plot.bottom + textSize * 0.75 + 1 / chartScale);
  const xRowNearGutter = xLabelBase < plot.bottom + textSize * 1.07;
  timeTicks.forEach((tick, index) => {
    svg.append(svgElement('line', { x1: x(tick), y1: plot.top, x2: x(tick), y2: plot.bottom, class: 'chart-grid-line' }));
    if (index % labelEvery !== 0) return;
    const label = historyTickLabel(tick, tickStep);
    const lx = x(tick);
    const halfLabel = historyAxisTextWidth(label, textSize) / 2;
    let anchor = 'middle';
    if (xRowNearGutter && lx - halfLabel < plot.left - axisPad) anchor = 'start';
    else if (xRowNearGutter && lx + halfLabel > plot.right + axisPad) anchor = 'end';
    svg.append(svgElement('text', { x: lx, y: xLabelBase, class: 'chart-axis-text', 'font-size': textSize, 'text-anchor': anchor }, label));
  });

  const drawSeries = (line, yScale) => {
    line.segments.forEach((segment) => {
      if (segment.length < 2) return;
      svg.append(svgElement('polyline', {
        points: segment.map((point) => `${x(point.ts)},${yScale(point.value)}`).join(' '),
        fill: 'none', stroke: line.color, 'stroke-width': 2 / chartScale, 'stroke-linecap': 'round', 'stroke-linejoin': 'round',
      }));
    });
  };
  tempLines.forEach((line) => drawSeries(line, yTemp));
  fanLines.forEach((line) => drawSeries(line, yFan));

  renderHistoryLegend();

  if (status) {
    status.textContent = `已记录 ${samples.length} 个采样点 · 更新于 ${new Date().toLocaleTimeString('zh-CN', { hour12: false })}`;
  }
  // 自动刷新重绘会清掉十字线；若鼠标仍悬停在图上，按原位置恢复
  if (historyLastCursorEvent && !$('history-tooltip')?.hidden) showHistoryCursor(historyLastCursorEvent);
}

// 图例：六个父类行 = 复选框（整组显隐）+ 色点 + ▾（展开子类勾选弹窗）。
// 父类勾选开关：取消时连带清空组内全部子类勾选（存为空 Set）——可见性是
// "父类开 && 子类勾"两层与运算，父类关时子类残留会违背直觉；重新勾上父类
// 后子类仍是空的，要恢复数据再手动勾或点聚合项。
function setHistoryGroupEnabled(groupKey, enabled) {
  historySeriesEnabled[groupKey] = enabled;
  saveHistorySeriesEnabled();
  if (!enabled) setHistoryChildSelection(groupKey, new Set());
  renderHistoryChart();
}

function renderHistoryLegend() {
  const legend = $('history-legend');
  if (!legend) return;
  const identity = JSON.stringify(HISTORY_GROUPS.map((group) => ({
    key: group.key,
    children: historyGroupChildIDs(group.key, historyRangedSamples),
    empty: (historyCache?.samples || []).length === 0,
  })));
  if (identity !== historyLegendIdentity || !legend.children.length) {
    historyLegendIdentity = identity;
    legend.replaceChildren();
    HISTORY_GROUPS.forEach((group) => {
      const item = document.createElement('span');
      item.className = 'history-legend-item';
      item.dataset.seriesKey = group.key;
      const box = document.createElement('input');
      box.type = 'checkbox';
      box.checked = historySeriesEnabled[group.key] !== false;
      box.addEventListener('change', () => {
        setHistoryGroupEnabled(group.key, box.checked);
      });
      const dot = document.createElement('i');
      dot.className = 'history-dot';
      dot.style.background = group.color;
      item.append(box, dot, document.createTextNode(group.label));
      const childIDs = historyGroupChildIDs(group.key, historyRangedSamples);
      // 所有父类都可展开：无数据的组弹窗里显示“暂无数据”
      const arrow = document.createElement('button');
      arrow.type = 'button';
      arrow.className = 'history-popover-arrow';
      arrow.textContent = '▾';
      arrow.setAttribute('aria-label', `选择${group.label}曲线`);
      arrow.addEventListener('click', (event) => {
        event.stopPropagation();
        toggleHistoryPopover(group.key, item);
      });
      item.append(arrow);
      // 父类名字整行也可点击展开；父类勾选框和弹窗内部的点击（子类勾选等）
      // 不触发展开/折叠——弹窗在行内，冒泡到这里的点击若不排除会把弹窗
      // 立即折叠，且子类复选框随 DOM 移除导致 change 丢失、勾选不生效
      item.addEventListener('click', (event) => {
        if (event.target === box) return;
        if (event.target.closest?.('.history-popover')) return;
        toggleHistoryPopover(group.key, item);
      });
      legend.append(item);
    });
    if (historyOpenPopoverKey) {
      const anchor = legend.querySelector(`.history-legend-item[data-series-key="${historyOpenPopoverKey}"]`);
      if (anchor) {
        const popover = document.createElement('div');
        popover.className = 'history-popover';
        popover.dataset.seriesKey = historyOpenPopoverKey;
        anchor.append(popover);
        fillHistoryPopover(historyOpenPopoverKey, popover);
      } else {
        historyOpenPopoverKey = null;
      }
    }
  }
}

// 父类弹窗：温度组首行为“取最高”，其余行为组内子类；勾选即改曲线显隐。
function toggleHistoryPopover(groupKey, anchorItem) {
  const existing = anchorItem.querySelector('.history-popover');
  closeHistoryPopovers();
  if (existing) return;
  historyOpenPopoverKey = groupKey;
  const popover = document.createElement('div');
  popover.className = 'history-popover';
  popover.dataset.seriesKey = groupKey;
  anchorItem.append(popover);
  fillHistoryPopover(groupKey, popover);
}

function closeHistoryPopovers() {
  historyOpenPopoverKey = null;
  document.querySelectorAll('.history-popover').forEach((node) => node.remove());
}

function fillHistoryPopover(groupKey, popover) {
  const childIDs = historyGroupChildIDs(groupKey, historyRangedSamples);
  const selection = historyChildSelectionFor(groupKey);
  const visible = (childID) => selection === null || selection.has(childID);
  popover.replaceChildren();
  if (!childIDs.length) {
    const empty = document.createElement('div');
    empty.className = 'history-popover-empty';
    empty.textContent = '该组暂无数据';
    popover.append(empty);
    return;
  }
  childIDs.forEach((childID) => {
    const row = document.createElement('label');
    row.className = 'history-popover-row';
    const box = document.createElement('input');
    box.type = 'checkbox';
    box.checked = visible(childID);
    box.addEventListener('change', () => {
      const current = historyChildSelectionFor(groupKey) === null
        ? new Set(historyGroupChildIDs(groupKey, historyRangedSamples))
        : new Set(historyChildSelectionFor(groupKey));
      if (box.checked) current.add(childID);
      else current.delete(childID);
      setHistoryChildSelection(groupKey, current);
      renderHistoryChart();
    });
    const dot = document.createElement('i');
    dot.className = 'history-dot';
    dot.style.background = historyChildColor(groupKey, childID, childIDs); // 与曲线同色，所见即所得
    row.append(box, dot, document.createTextNode(historyChildLabel(groupKey, childID)));
    popover.append(row);
  });
}

// 点击图外区域收起弹窗。
document.addEventListener('click', (event) => {
  if (!event.target.closest?.('.history-legend-item')) closeHistoryPopovers();
});

async function fetchHistory(force = false) {
  if (!force && historyCache && Date.now() - historyFetchedAt < HISTORY_FETCH_TTL) {
    renderHistoryChart();
    return historyCache;
  }
  try {
    const requestedRange = historyRangeHours;
    const data = await request(`api/history?range=${requestedRange}`);
    // 快速切换范围时丢弃过期响应：慢的旧请求不能覆盖当前范围的缓存
    if (requestedRange !== historyRangeHours) return historyCache;
    if (data && Array.isArray(data.samples)) {
      historyCache = data;
      historyFetchedAt = Date.now();
      renderHistoryChart();
    }
  } catch (error) {
    const status = $('history-status');
    if (status) status.textContent = `历史数据读取失败：${error.message}`;
  }
  return historyCache;
}

// 把当前 historyRangeHours 同步到滑杆与按钮：固定挡位停在吸附位中心，
// 动一下滑杆（input/change）就回到滑杆值。
// 值标签跟随拇指：把滑杆位置百分比写入 CSS 变量（夹在 6%–94%，标签居中
// 对准拇指且不出容器）。
function syncRunLogThumbPct(slider) {
  const max = Number(slider.max) || 1;
  const pct = clamp((Number(slider.value) / max) * 100, 6, 94);
  slider.parentElement.style.setProperty('--thumb-pct', `${pct}%`);
}

function syncHistoryRangeUI() {
  const slider = $('history-range-slider');
  if (slider) {
    slider.value = String(historyHoursToPos(historyRangeHours));
    slider.setAttribute('aria-valuetext', historyRangeLabel(historyRangeHours));
    syncRunLogThumbPct(slider);
  }
  const label = $('history-range-value');
  if (label) label.textContent = historyRangeLabel(historyRangeHours);
}

function setHistoryRange(hours) {
  historyRangeTouched = true; // 本会话用户已手动选择，此后不被后端档位覆盖
  historyRangeHours = hours;
  saveHistoryRangeHours(hours);
  syncHistoryRangeUI();
  fetchHistory(true);
}

// 后端配置的档位只在首次状态到达时采纳一次：localStorage 只是首屏快速起效
// 的缓存，跨设备的最终一致性以后端为准（换浏览器/换设备也生效）；用户本会话
// 已操作过档位则不打扰。
let historyRangeTouched = false;
let historyRangeAdopted = false;
function applyBackendHistoryRange(prefs) {
  if (historyRangeAdopted || historyRangeTouched) return;
  historyRangeAdopted = true;
  const hours = Number(prefs?.history_range_hours);
  if (!Number.isFinite(hours) || hours <= 0) return;
  const normalized = normalizeHistoryRangeHours(hours);
  if (normalized === historyRangeHours) return;
  historyRangeHours = normalized;
  try { window.localStorage.setItem(HISTORY_RANGE_STORAGE_KEY, String(normalized)); }
  catch (error) { /* 缓存镜像失败可忽略 */ }
  syncHistoryRangeUI();
  fetchHistory(true);
}

// 保留天数收窄可查窗口：7 天/30 天挡位按保存天数显隐（小于 7 天两个都不
// 显示，7–29 天只显示 7 天），滑杆 max 随挡位数变化；当前记忆的范围超出
// 保留期则回落到默认档（30 分钟）。
function updateHistoryRangeAvailability(retentionDays) {
  const days = Math.round(Number(retentionDays) || 30);
  historySliderStopCount = 3 + (days >= 7 ? 1 : 0) + (days >= 30 ? 1 : 0);
  const slider = $('history-range-slider');
  // 滑杆 max = 末挡停靠位：拖到最右恰好填满整条轨道（进度条 100%），
  // 吸附回拉不会让尾巴留一段灰色。
  if (slider) slider.max = String(historyStopCenterPos(historySliderStopCount - 1));
  const ticks = document.querySelector('.history-range-ticks');
  if (ticks) {
    ticks.classList.remove('layout-5', 'layout-6', 'layout-7');
    ticks.classList.add(`layout-${historySliderStopCount + 2}`); // 30分/2时 两个恒显刻度 + 挡位数
    const weekTick = ticks.querySelector('[data-tick="7d"]');
    const monthTick = ticks.querySelector('[data-tick="30d"]');
    if (weekTick) weekTick.hidden = days < 7;
    if (monthTick) monthTick.hidden = days < 30;
  }
  if (historyRangeHours > days * 24) setHistoryRange(HISTORY_DEFAULT_HOURS);
}

function setupHistoryPanel() {
  const slider = $('history-range-slider');
  if (slider) {
    // 拖动中只实时刷新标签，松手（change）才取数，避免半路连续请求；
    // 滑杆位置经双段映射换成小时（无极区 1 分钟粒度 / 挡位区整段吸附）。
    slider.addEventListener('input', () => {
      const hours = historyPosToHours(parseFloat(slider.value));
      const label = $('history-range-value');
      if (label) label.textContent = historyRangeLabel(hours);
      syncRunLogThumbPct(slider);
    });
    slider.addEventListener('change', () => setHistoryRange(historyPosToHours(parseFloat(slider.value))));
  }
  syncHistoryRangeUI(); // 应用 localStorage 记忆的范围
  setupHistoryCursor();
}

// ---- 悬浮十字线：竖虚线 + 各曲线在该时刻的数值提示框 ----

function historyCursorTimeAt(event) {
  const svg = $('history-chart');
  if (!svg) return null;
  const matrix = svg.getScreenCTM();
  if (!matrix) return null;
  const point = svg.createSVGPoint();
  point.x = event.clientX;
  point.y = event.clientY;
  const local = point.matrixTransform(matrix.inverse());
  const nowTs = Math.floor(Date.now() / 1000);
  const startSec = nowTs - historyRangeHours * 3600;
  const raw = startSec + ((local.x - historyPlotBox.left) / (historyPlotBox.right - historyPlotBox.left)) * historyRangeHours * 3600;
  return Math.round(Math.min(nowTs, Math.max(startSec, raw)));
}

function historyNearestSample(ts) {
  let best = null;
  let bestDelta = Infinity;
  for (const sample of historyRangedSamples) {
    const delta = Math.abs(sample.ts - ts);
    if (delta < bestDelta) { best = sample; bestDelta = delta; }
  }
  return best;
}

// 十字线时刻各系列的取值行：与图上可见曲线同色同序。
function historyCursorRows(sample) {
  const rows = [];
  HISTORY_GROUPS.forEach((group) => {
    if (historySeriesEnabled[group.key] === false) return;
    const selection = historyChildSelectionFor(group.key);
    const childIDs = historyGroupChildIDs(group.key, historyRangedSamples);
    childIDs.forEach((childID) => {
      if (!(selection === null || selection.has(childID))) return;
      const value = historyChildValue(group.key, childID, sample);
      if (!(Number.isFinite(value) && value > 0)) return;
      const color = historyChildColor(group.key, childID, childIDs);
      const label = historyChildLabel(group.key, childID);
      if (group.key === 'fan') {
        const rpm = (sample.fans || []).find((fan) => fan.id === childID)?.rpm ?? 0;
        rows.push({ color, label, text: `${rpm} RPM · ${Math.round(value)}%`, isFan: true, value });
      } else {
        rows.push({ color, label, text: `${value.toFixed(1)}°C`, isFan: false, value });
      }
    });
  });
  return rows;
}

function drawHistoryCrosshair(sample) {
  const svg = $('history-chart');
  if (!svg) return;
  svg.querySelectorAll('.history-crosshair').forEach((node) => node.remove());
  if (!sample) return;
  const nowTs = Math.floor(Date.now() / 1000);
  const startSec = nowTs - historyRangeHours * 3600;
  const plot = historyPlotBox;
  const cx = plot.left + ((clamp(sample.ts, startSec, nowTs) - startSec) / (historyRangeHours * 3600)) * (plot.right - plot.left);
  // 竖虚线用 non-scaling-stroke（SVG 属性，CSP 安全）：viewBox 拉伸时线宽保持
  // styles.css 里设定的 1.5 屏幕像素，不会随窗口放大变粗
  svg.append(svgElement('line', { x1: cx, y1: plot.top, x2: cx, y2: plot.bottom, class: 'history-crosshair history-crosshair-line', 'vector-effect': 'non-scaling-stroke' }));
  const { lo, hi } = historyYBoundsState;
  const yTemp = (value) => plot.bottom - ((clamp(value, lo, hi) - lo) / (hi - lo)) * (plot.bottom - plot.top);
  const yFan = (percent) => plot.bottom - (clamp(percent, 0, HISTORY_FAN_MAX_PERCENT) / HISTORY_FAN_MAX_PERCENT) * (plot.bottom - plot.top);
  // 圆点半径除以缩放（同折线 stroke-width 的处理）：viewBox 随容器宽度拉伸，
  // 固定 viewBox 单位会让悬停圆点等比变大；curveChartScale 自带 0.25 下限防 0
  const scale = curveChartScale(svg);
  historyCursorRows(sample).forEach((row) => {
    svg.append(svgElement('circle', { cx, cy: row.isFan ? yFan(row.value) : yTemp(row.value), r: 4 / scale, class: 'history-crosshair', fill: row.color }));
  });
}

function showHistoryCursor(event) {
  if (!historyRangedSamples.length) return;
  historyLastCursorEvent = { clientX: event.clientX, clientY: event.clientY };
  const ts = historyCursorTimeAt(event);
  if (ts == null) return;
  const sample = historyNearestSample(ts);
  if (!sample) return;
  drawHistoryCrosshair(sample);
  const tooltip = $('history-tooltip');
  if (!tooltip) return;
  tooltip.replaceChildren();
  const time = document.createElement('b');
  time.textContent = new Date(sample.ts * 1000).toLocaleString('zh-CN', { hour12: false, month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' });
  tooltip.append(time);
  historyCursorRows(sample).forEach((row) => {
    const item = document.createElement('div');
    item.className = 'history-tip-row';
    const dot = document.createElement('i');
    dot.className = 'history-dot';
    dot.style.background = row.color;
    item.append(dot, document.createTextNode(`${row.label} ${row.text}`));
    tooltip.append(item);
  });
  tooltip.hidden = false;
  tooltip.classList.add('visible');
  const gap = 14;
  const margin = 8;
  const rect = tooltip.getBoundingClientRect();
  let left = event.clientX + gap;
  if (left + rect.width > window.innerWidth - margin) left = event.clientX - rect.width - gap;
  const top = Math.min(Math.max(margin, event.clientY - rect.height / 2), Math.max(margin, window.innerHeight - rect.height - margin));
  tooltip.style.left = `${Math.max(margin, left)}px`;
  tooltip.style.top = `${top}px`;
}

function hideHistoryCursor() {
  historyLastCursorEvent = null;
  const svg = $('history-chart');
  if (svg) svg.querySelectorAll('.history-crosshair').forEach((node) => node.remove());
  const tooltip = $('history-tooltip');
  if (tooltip) {
    tooltip.classList.remove('visible');
    tooltip.hidden = true;
  }
}

function setupHistoryCursor() {
  const svg = $('history-chart');
  if (!svg) return;
  svg.addEventListener('mousemove', showHistoryCursor);
  svg.addEventListener('mouseleave', hideHistoryCursor);
}


function curvePositionFromPointer(kind, event) {
  const svg = $(curveEditors[kind].chartID);
  const matrix = svg.getScreenCTM();
  if (!matrix) return null;
  const point = svg.createSVGPoint();
  point.x = event.clientX;
  point.y = event.clientY;
  const local = point.matrixTransform(matrix.inverse());
  return {
    temperature: 20 + ((local.x - fanPlotBox.left) / (fanPlotBox.right - fanPlotBox.left)) * 80,
    pwm: 30 + ((fanPlotBox.bottom - local.y) / (fanPlotBox.bottom - fanPlotBox.top)) * 70,
  };
}

function updateCurveFromPointer(kind, event) {
  const editor = curveEditors[kind];
  if (editor.draggedIndex < 0) return;
  const position = curvePositionFromPointer(kind, event);
  if (!position) return;
  setCurvePoint(kind, editor.draggedIndex, position.temperature, position.pwm);
  renderFanChart(kind);
}

function populateFanDevices(status, keepInputs) {
  const select = $('fan-device');
  const fans = connectedFans(status.fan_control);
  const config = status.config?.fan || {};
  const signature = fanListSignature(fans);
  const rebuild = signature !== fanSelectorSignature;
  if (rebuild) {
    fanSelectorSignature = signature;
    Object.values(FAN_CURVE_GROUPS).forEach((group) => {
      const container = $(group.containerID);
      container.replaceChildren();
      fans.forEach((fan) => {
        const label = document.createElement('label');
        label.className = 'curve-fan-option';
        label.dataset.fanId = fan.id;
        const input = document.createElement('input');
        input.type = 'checkbox';
        input.value = fan.id;
        const name = document.createElement('b');
        name.textContent = fanLabel(fan, fans);
        const rpm = document.createElement('small');
        label.append(input, name, rpm);
        container.append(label);
      });
      if (!fans.length) container.textContent = '未检测到有转速反馈的风扇';
    });
    select.replaceChildren();
    fans.forEach((fan) => {
      const option = document.createElement('option');
      option.value = fan.id;
      option.textContent = fanLabel(fan, fans);
      select.append(option);
    });
  }
  Object.values(FAN_CURVE_GROUPS).forEach((group) => {
    fans.forEach((fan) => {
      const label = $(group.containerID).querySelector(`[data-fan-id="${fan.id}"]`);
      if (label) label.querySelector('small').textContent = `${fan.rpm} RPM`;
    });
  });
  if (!keepInputs || rebuild) {
    Object.values(FAN_CURVE_GROUPS).forEach((group) => {
      const configured = Array.isArray(config[group.configField])
        ? config[group.configField]
        : (config.device_id ? [config.device_id] : (fans.length === 1 ? [fans[0].id] : []));
      const selected = new Set(configured);
      $(group.containerID).querySelectorAll('input').forEach((input) => { input.checked = selected.has(input.value); });
    });
  }
  select.value = config.device_id || selectedCurveFanIDs('cpu')[0] || fans[0]?.id || '';
}

function selectedCurveFanIDs(kind) {
  const group = FAN_CURVE_GROUPS[kind];
  return [...$(group.containerID).querySelectorAll('input[type="checkbox"]:checked')].map((input) => input.value);
}

function fillFanInputs(fan = {}) {
  $('fan-enabled').checked = Boolean(fan.enabled);
  $('fan-min').value = fan.min_pwm_percent ?? 60;
  $('fan-emergency').value = fan.emergency_temp_c ?? 85;
  $('fan-poll').value = fan.poll_seconds ?? 2;
  const fillCurve = (kind, curve, fallback) => {
    const source = Array.isArray(curve) && curve.length >= CURVE_MIN_POINTS && curve.length <= CURVE_MAX_POINTS
      && curve.every((point) => Number.isFinite(Number(point.temp_c)) && Number.isFinite(Number(point.pwm_percent)))
      ? curve : fallback;
    const editor = curveEditors[kind];
    editor.curve = source.map((point) => ({ temp_c: Number(point.temp_c), pwm_percent: Number(point.pwm_percent) }));
    editor.selectedIndex = clamp(editor.selectedIndex, 0, editor.curve.length - 1);
  };
  fillCurve('cpu', fan.curve, DEFAULT_CPU_CURVE);
  fillCurve('hdd', fan.hdd_curve || fan.disk_curve, DEFAULT_STORAGE_CURVE);
  fillCurve('nvme', fan.nvme_curve, DEFAULT_STORAGE_CURVE);
  fillFanSlotSelections(fan);
}

function fillHistoryInputs(history = {}) {
  const enabled = history.enabled !== false; // 后端旧配置无 history 段，默认启用
  $('history-enabled').checked = enabled;
  const maxSize = Number(history.max_size_mb) || 64;
  $('history-max-size').value = maxSize;
  // 旧后端配置无 retention_days 字段时按 30 天兜底
  const retentionDays = Number(history.retention_days) || 30;
  $('history-retention-days').value = retentionDays;
  $('history-archive-enabled').checked = Boolean(history.archive_enabled);
  $('history-archive-dir').value = history.archive_dir || '';
  updateHistoryRangeAvailability(retentionDays);
  updateHistoryDisabledNotice(enabled);
  // 运行日志大小在 render() 的 status 上下文里单独回填（见 syncRunLogInputs）
}

function syncRunLogInputs(status = {}) {
  $('runlog-max-size').value = Number(status.config?.log?.max_size_mb) || 16;
}

function updateHistoryDisabledNotice(enabled) {
  $('history-disabled-notice').hidden = enabled;
}

$('history-enabled').addEventListener('change', () => {
  updateHistoryDisabledNotice($('history-enabled').checked);
});

// 历史设置保存前的取值校验：返回错误提示文案，通过时返回 null
function historySettingsError(maxSize, retentionDays) {
  if (!Number.isFinite(maxSize) || maxSize < 8 || maxSize > 1024) return '数据库大小上限需在 8–1024 MB 之间。';
  if (!Number.isFinite(retentionDays) || retentionDays < 1) return '保存天数需至少为 1 天。';
  return null;
}

$('save-history').addEventListener('click', async () => {
  const maxSizeInput = $('history-max-size');
  const retentionInput = $('history-retention-days');
  if (!maxSizeInput.reportValidity() || !retentionInput.reportValidity()) return;
  const maxSize = Number(maxSizeInput.value);
  const retentionDays = Number(retentionInput.value);
  const archiveEnabled = $('history-archive-enabled').checked;
  const archiveDir = $('history-archive-dir').value.trim();
  if (archiveEnabled && !archiveDir) {
    showMessage('开启长期记录需先填写保存位置。', true, 'message-history');
    return;
  }
  const error = historySettingsError(maxSize, retentionDays);
  if (error) {
    showMessage(error, true, 'message-history');
    return;
  }
  setBusy(true);
  try {
    render(await request('api/config/history', { method: 'POST', body: JSON.stringify({ enabled: $('history-enabled').checked, max_size_mb: maxSize, retention_days: retentionDays, archive_enabled: archiveEnabled, archive_dir: archiveEnabled ? archiveDir : '' }) }), true);
    showMessage($('history-enabled').checked ? '历史设置已保存；后台每分钟继续写入采样。' : '历史设置已保存；后台已停止写入新采样。', false, 'message-history');
  } catch (error) {
    showMessage(`保存失败：${error.message}`, true, 'message-history');
  } finally {
    setBusy(false);
  }
});

// 清空数据库属危险操作：按钮在调试页"历史温度设定"卡片的操作行（导出之后）。
// 点击瞬间先补一次长期记录冲刷（不论二次确认结果如何，缓冲里的旧数据先落到
// 归档盘），然后经 window.confirm 二次确认；确认后才真正清空。冲刷失败时在
// 弹窗里明示后果，由用户决定是否仍要清空。
$('history-clear').addEventListener('click', async () => {
  setBusy(true);
  let flushError = '';
  let flushed = false;
  try {
    const result = await request('api/history/archive', { method: 'POST', body: '{}' });
    flushed = Boolean(result.flushed);
  } catch (error) {
    flushError = error.message;
  }
  const message = flushError
    ? `长期记录冲刷失败（${flushError}），未冲刷的数据将随清空丢失。仍要清空？`
    : flushed
      ? '长期记录的缓冲已补归档。清空数据库将删除全部在线历史采样，且无法恢复。确定清空？'
      : '清空数据库将删除全部历史采样，且无法恢复。确定清空？';
  const confirmed = window.confirm(message);
  if (!confirmed) {
    setBusy(false);
    return; // 取消：补冲刷已生效（或已尝试），在线数据原样保留
  }
  try {
    await request('api/history/clear', { method: 'POST', body: '{}' });
    // 强制绕过 TTL 缓存重新拉取，曲线立即清空（与保存传感器名后的刷新一致）
    await fetchHistory(true);
    renderHistoryChart();
    showMessage('历史数据库已清空。', false, 'message-history');
  } catch (error) {
    showMessage(`清空失败：${error.message}`, true, 'message-history');
  } finally {
    setBusy(false);
  }
});

// ---- 运行日志：独立大小设置、导出与清空 ----

function runlogMessage(message, error = false) {
  showMessage(message, error, 'runlog-status');
}

$('save-runlog').addEventListener('click', async () => {
  const input = $('runlog-max-size');
  if (!input.reportValidity()) return;
  const maxSize = Number(input.value);
  if (!Number.isFinite(maxSize) || maxSize < 1 || maxSize > 256) {
    runlogMessage('日志大小上限需在 1–256 MB 之间。', true);
    return;
  }
  setBusy(true);
  try {
    render(await request('api/config/log', { method: 'POST', body: JSON.stringify({ max_size_mb: maxSize }) }), true);
    runlogMessage(`日志大小上限已保存为 ${maxSize} MB；超过后自动截断并保留一代备份。`);
  } catch (error) {
    runlogMessage(`保存失败：${error.message}`, true);
  } finally {
    setBusy(false);
  }
});

$('runlog-export').addEventListener('click', () => {
  const stamp = new Date().toISOString().slice(0, 10);
  const link = document.createElement('a');
  link.href = baseUrl('api/log/export');
  link.download = `tad-module-log-${stamp}.txt`;
  document.body.append(link);
  link.click();
  link.remove();
  runlogMessage('日志下载已开始。');
});

$('runlog-clear').addEventListener('click', async () => {
  if (!window.confirm('清空运行日志将删除当前日志与上一代备份，且无法恢复。确定清空？')) return;
  setBusy(true);
  try {
    await request('api/log/clear', { method: 'POST', body: '{}' });
    runlogMessage('运行日志已清空。');
  } catch (error) {
    runlogMessage(`清空失败：${error.message}`, true);
  } finally {
    setBusy(false);
  }
});

function renderFanRPMs(fanStatus = {}) {
  const target = $('fan-rpm-list');
  const fans = connectedFans(fanStatus, 4);
  const existing = [...target.querySelectorAll('[data-fan-id]')];
  if (existing.map((item) => item.dataset.fanId).join('|') !== fans.map((fan) => fan.id).join('|')) {
    target.replaceChildren();
    fans.forEach((fan) => {
      const item = document.createElement('p');
      item.dataset.fanId = fan.id;
      item.append(document.createElement('span'), document.createElement('strong'));
      target.append(item);
    });
    if (!fans.length) target.innerHTML = '<strong id="fan-rpm">不可用</strong>';
  }
  fans.forEach((fan) => {
    const item = target.querySelector(`[data-fan-id="${fan.id}"]`);
    item.querySelector('span').textContent = fanLabel(fan, fans);
    item.querySelector('strong').textContent = `${fan.rpm} RPM`;
  });
}

function resolvedPowerPreset(status = {}, mode) {
  const profile = status.profile || {};
  const preset = profile.power_presets?.[mode];
  if (!preset) return null;
  const minPL1 = Number(profile.min_pl1_w) || 1;
  const maxPL1 = Number(status.effective_max_pl1_w || profile.max_pl1_w) || Number.POSITIVE_INFINITY;
  const maxPL2 = Number(status.effective_max_pl2_w || profile.max_pl2_w) || Number.POSITIVE_INFINITY;
  const pl1W = Math.max(minPL1, Math.min(Number(preset.pl1_w), maxPL1));
  const pl2W = Math.max(pl1W, Math.min(Number(preset.pl2_w), maxPL2));
  return { pl1_w: pl1W, pl2_w: pl2W };
}

function detectedPowerMode(status = {}, config = {}) {
  for (const mode of ['saving', 'standard', 'performance']) {
    const preset = resolvedPowerPreset(status, mode);
    if (preset && Number(preset.pl1_w) === Number(config.pl1_w) && Number(preset.pl2_w) === Number(config.pl2_w)) return mode;
  }
  return 'custom';
}

function updatePowerMode(mode, applyPreset = true) {
  const selected = document.querySelector(`input[name="power-mode"][value="${mode}"]`)
    || document.querySelector('input[name="power-mode"][value="custom"]');
  selected.checked = true;
  const preset = resolvedPowerPreset(currentStatus || {}, selected.value);
  if (applyPreset && preset) {
    $('pl1').value = preset.pl1_w;
    $('pl2').value = preset.pl2_w;
  }
  const custom = selected.value === 'custom';
  $('pl1').readOnly = !custom;
  $('pl2').readOnly = !custom;
  $('power-custom-fields').classList.toggle('preset-active', !custom);
}

function render(status, keepInputs = false) {
  currentStatus = status;
  const pkg = status.packages?.[0] || {};
  const cpuTemperature = status.cpu_temperature || {};
  const fanStatus = status.fan_control || {};
  const storageStatus = status.storage || {};
  const gpioStatus = status.gpio || {};
  const versionText = status.version ? `v${status.version}` : 'v—';
  $('app-version').textContent = versionText;
  $('about-version').textContent = versionText;
  $('device-name').textContent = status.device_name || 'TAD6S4N';
  $('os-version').textContent = [status.os_name, status.os_version].filter(Boolean).join(' ') || 'fnOS';
  $('cpu-model').textContent = status.cpu_model || '未识别';
  $('cpu-display-label').textContent = cpuTemperature.display_source === 'package_fallback'
    ? 'CPU 温度（Package 回退）'
    : 'CPU 核心最高';
  $('cpu-display-temperature').textContent = formatTemperature(cpuTemperature.display_c, cpuTemperature.available);
  $('package-temperature').textContent = formatTemperature(cpuTemperature.package_max_c, Number(cpuTemperature.package_sensors) > 0);
  $('current-pl1').textContent = pkg.has_pl1 ? `${pkg.pl1_w} W` : '不可用';
  $('current-pl2').textContent = pkg.has_pl2 ? `${pkg.pl2_w} W` : '不可用';
  renderFanRPMs(fanStatus);
  $('profile').textContent = status.profile?.display || '不支持';
  $('temperature-source').textContent = cpuTemperature.display_source === 'core_max_rr'
    ? `RR 核心最大值（${cpuTemperature.core_sensors} 个 Core）`
    : (cpuTemperature.display_source === 'package_fallback' ? '未找到 Core 标签，回退到 Package' : '未识别');
  $('last-apply').textContent = status.last_apply ? new Date(status.last_apply).toLocaleString() : '尚未应用';
  const diagnosticError = status.last_error || fanStatus.last_error || '';
  $('last-error').textContent = diagnosticError || '正常';
  $('last-error').className = diagnosticError ? 'diagnostic-error' : 'diagnostic-ok';
  renderDiagnostics(status, fanStatus);
  renderStorageVisual(storageStatus);
  renderStorageTable(storageStatus);
  renderFanSlotTemperatures(storageStatus);
  renderFanHardwareWarning(fanStatus);
  $('gpio-status').textContent = gpioStatus.enabled
    ? (gpioStatus.available ? `监听中${gpioStatus.last_event ? ` · 最近：${gpioStatus.last_event}` : ''}` : `已启用但不可用：${gpioStatus.last_error || '无法读取 /dev/port'}`)
    : (gpioStatus.available ? '硬件接口可用，按键映射尚未启用。' : '按键映射默认关闭。');
  $('gpio-status').className = `inline-status${gpioStatus.enabled && (!gpioStatus.available || gpioStatus.last_error) ? ' error' : ''}`;
  const issues = healthIssues(status, fanStatus, storageStatus, gpioStatus);
  const healthy = issues.length === 0;
  const healthMessage = healthy
    ? '未发现需要处理的异常。'
    : `需要检查以下项目：\n${issues.map((issue) => `• ${issue}`).join('\n')}`;
  renderHealthBadge(healthy ? '运行正常' : '需要检查', healthy ? 'ok' : 'error', healthMessage);

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
    updatePowerMode(detectedPowerMode(status, status.config || {}), false);
    fillFanInputs(status.config?.fan);
    fillGPIOInputs(status.config?.gpio);
    applyBackendHistoryRange(status.config?.ui_prefs); // 后端档位先采纳，保留天数钳制随后生效
    fillHistoryInputs(status.config?.history);
    syncRunLogInputs(status);
  }
  CURVE_KINDS.forEach(renderFanChart);
}

function showMessage(message, error = false, targetID = 'message-global') {
  const target = $(targetID);
  target.textContent = message;
  target.className = `message${error ? ' error' : ''}`;
}

function setDebugStatus(message, error = false) {
  const target = $('debug-status');
  target.textContent = message;
  target.className = `message${error ? ' error' : ''}`;
}

// 宿主窗口探测（仅采集结构信息，脱敏）：fnOS 桌面标题栏由宿主绘制、无官方
// 配色接口，深色模式下是否变深取决于宿主实现。此处从插件 iframe 向上最多
// 12 层记录祖先链的标签/id/类名与计算背景色，用于在真机上定位窗口外壳的
// 真实 DOM 结构；不采集任何文本内容与 URL。
function collectHostWindowInfo() {
  const info = {
    embedded: window.parent !== window,
    parentReadable: false,
    parentThemeMode: null,
    iframe: null,
    ancestors: [],
  };
  try {
    if (window.parent === window) return info;
    const parent = window.parent;
    const doc = parent.document;
    if (!doc || !doc.body) return info;
    info.parentReadable = true;
    info.parentThemeMode = (doc.body.getAttribute('theme-mode') || '').trim().toLowerCase() || null;
    let frame = null;
    doc.querySelectorAll('iframe').forEach((candidate) => {
      if (frame) return;
      try { if (candidate.contentWindow === window) frame = candidate; } catch (error) { /* 跨域框架跳过 */ }
    });
    if (!frame) return info;
    info.iframe = {
      id: frame.id || null,
      class: (`${frame.className || ''}`).slice(0, 120) || null,
    };
    let node = frame.parentElement;
    for (let depth = 0; node && depth < 12; depth += 1, node = node.parentElement) {
      const style = parent.getComputedStyle(node);
      info.ancestors.push({
        depth,
        tag: node.tagName.toLowerCase(),
        id: node.id || null,
        class: (`${node.className || ''}`).slice(0, 160) || null,
        backgroundColor: style.backgroundColor,
        color: style.color,
      });
    }
  } catch (error) {
    info.error = String((error && error.message) || error);
  }
  return info;
}

// 页面诊断：报告生成时刻的环境快照 + 采集到的页面错误（localStorage 里
// 跨刷新保留）。白屏类问题真机无控制台，靠这份快照定位：哪块面板消失、
// 是否有运行期异常。取走即清空，报告之间不重复累计。
function collectPageDiagnostics() {
  const diagnostics = {
    viewport: `${window.innerWidth}x${window.innerHeight}`,
    theme: String(document.documentElement.dataset.theme || '') || null,
    activePanels: [...document.querySelectorAll('.tab-panel')].filter((panel) => !panel.hidden).map((panel) => panel.id),
    shellAttached: Boolean(document.querySelector('main.shell')),
    pageErrors: [],
  };
  try {
    diagnostics.pageErrors = JSON.parse(window.localStorage.getItem(PAGE_ERROR_STORE_KEY) || '[]');
    window.localStorage.setItem(PAGE_ERROR_STORE_KEY, '[]');
  } catch (error) { /* 无存储时保持空数组 */ }
  return diagnostics;
}

function formatDebugReport(payload) {
  const generatedAt = new Date().toISOString();
  let serialized;
  try {
    serialized = JSON.stringify(payload, null, 2);
  } catch (error) {
    throw new Error(`报告格式化失败：${error.message}`);
  }
  let host;
  try {
    host = JSON.stringify(collectHostWindowInfo(), null, 2);
  } catch (error) {
    host = `{"error": ${JSON.stringify(String(error.message))}}`;
  }
  let page;
  try {
    page = JSON.stringify(collectPageDiagnostics(), null, 2);
  } catch (error) {
    page = `{"error": ${JSON.stringify(String(error.message))}}`;
  }
  return `# TAD6S4N10G 调试报告\n\n生成时间：${generatedAt}\n\n以下内容由 GET api/debug/report 返回，用于协助定位模块问题。请在公开发布前检查是否包含敏感信息。\n\n## 接口报告\n\n\`\`\`json\n${serialized}\n\`\`\`\n\n## 宿主窗口环境（页面采集，仅结构信息）\n\n\`\`\`json\n${host}\n\`\`\`\n\n## 页面诊断（错误采集与环境快照）\n\n\`\`\`json\n${page}\n\`\`\``;
}

function updateDebugReportActions(enabled) {
  ['debug-copy-report', 'debug-download-report', 'debug-open-issue'].forEach((id) => { $(id).disabled = !enabled; });
}

async function generateDebugReport() {
  const button = $('debug-generate-report');
  button.disabled = true;
  setDebugStatus('正在生成调试报告…');
  try {
    const payload = await request('api/debug/report');
    const text = formatDebugReport(payload);
    debugReportText = text;
    $('debug-report').value = text;
    $('debug-report-meta').textContent = `已生成 · ${text.length.toLocaleString()} 字符 · ${new Date().toLocaleString()}`;
    updateDebugReportActions(true);
    setDebugStatus('调试报告已生成，可复制、下载或打开 GitHub Issue。');
  } catch (error) {
    setDebugStatus(`生成失败：${error.message}`, true);
  } finally {
    button.disabled = false;
  }
}

async function copyDebugReport() {
  if (!debugReportText) return;
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(debugReportText);
      setDebugStatus('报告已复制到剪贴板。');
      return;
    }
    throw new Error('Clipboard API 不可用');
  } catch (error) {
    const helper = document.createElement('textarea');
    helper.value = debugReportText;
    helper.setAttribute('readonly', '');
    helper.style.position = 'fixed';
    helper.style.opacity = '0';
    document.body.append(helper);
    helper.select();
    let copied = false;
    try { copied = document.execCommand('copy'); } catch (copyError) { copied = false; }
    helper.remove();
    if (copied) {
      setDebugStatus('报告已复制到剪贴板。');
      return;
    }
    $('debug-report').focus();
    $('debug-report').select();
    setDebugStatus('自动复制失败，报告已选中，请按 Ctrl+C 手动复制。', true);
  }
}

// ---- 调试页：传感器显示名设置 ----

function setupSensorNames() {
  const saveButton = $('sensor-names-save');
  if (!saveButton) return;
  saveButton.addEventListener('click', saveSensorNames);
}

// 父类下拉可选项：与后端 sensorGroupValues 保持一致（gpu|nic|other）。
const SENSOR_GROUP_OPTIONS = [
  { value: 'gpu', label: 'GPU' },
  { value: 'nic', label: '网卡' },
  { value: 'other', label: '其它' },
];

// 从历史数据里发现全部传感器键，渲染成行：默认名 + 显示名输入框 + 父类下拉。
// CPU 组传感器（核心温度）固定归 CPU 父类，不下拉。
function renderSensorNamesList() {
  const wrap = $('sensor-names-list');
  if (!wrap) return;
  const samples = historyCache?.samples || [];
  const groupOf = new Map();
  samples.forEach((sample) => (sample.sensors || []).forEach((sensor) => {
    if (sensor.group && sensor.key && !groupOf.has(sensor.key)) groupOf.set(sensor.key, sensor.group);
  }));
  const keys = [...groupOf.keys()].sort();
  wrap.replaceChildren();
  if (!keys.length) {
    const empty = document.createElement('p');
    empty.className = 'debug-report-meta';
    empty.textContent = '暂未发现传感器，请先打开历史温度页等待数据加载。';
    wrap.append(empty);
    return;
  }
  const saved = currentStatus?.config?.sensor_names || {};
  const savedGroups = currentStatus?.config?.sensor_groups || {};
  keys.forEach((key) => {
    const row = document.createElement('div');
    row.className = 'sensor-name-row';
    const keyLabel = document.createElement('span');
    keyLabel.className = 'sensor-name-key';
    keyLabel.textContent = key;
    const input = document.createElement('input');
    input.type = 'text';
    input.className = 'sensor-name-input';
    input.dataset.sensorKey = key;
    input.value = saved[key] || '';
    input.placeholder = historyChildLabel(key.startsWith('Core ') || key.startsWith('Package') ? 'cpu' : 'other', key);
    input.maxLength = 40;
    if (groupOf.get(key) === 'cpu') {
      const fixed = document.createElement('span');
      fixed.className = 'sensor-name-group-fixed';
      fixed.textContent = 'CPU';
      row.append(keyLabel, input, fixed);
    } else {
      const select = document.createElement('select');
      select.className = 'sensor-name-group';
      select.dataset.sensorKey = key;
      // 默认归属随 /api/history 下发（default_groups）；选回默认组时保存
      // 端会自动清除覆盖，跟随驱动表自动归类。
      select.dataset.defaultGroup = historyCache?.default_groups?.[key] || groupOf.get(key);
      select.title = '归属父类';
      SENSOR_GROUP_OPTIONS.forEach(({ value, label }) => {
        const option = document.createElement('option');
        option.value = value;
        option.textContent = label;
        select.append(option);
      });
      // 当前生效父类：用户覆盖优先，否则即默认组
      select.value = savedGroups[key] || select.dataset.defaultGroup;
      row.append(keyLabel, input, select);
    }
    wrap.append(row);
  });
}

async function saveSensorNames() {
  const status = $('sensor-names-status');
  const names = {};
  document.querySelectorAll('.sensor-name-input').forEach((input) => {
    const value = input.value.trim();
    if (value) names[input.dataset.sensorKey] = value;
  });
  const groups = {};
  document.querySelectorAll('.sensor-name-group').forEach((select) => {
    if (select.value && select.value !== select.dataset.defaultGroup) groups[select.dataset.sensorKey] = select.value;
  });
  try {
    const updated = await request('api/config/sensor-names', { method: 'POST', body: JSON.stringify({ names, groups }) });
    render(updated, true);
    if (status) status.textContent = `已保存 ${Object.keys(names).length} 个显示名、${Object.keys(groups).length} 个父类归属。`;
    // 父类归属在读端生效：强制刷新历史数据，曲线立即搬到新父类
    await fetchHistory(true);
    renderHistoryChart();
  } catch (error) {
    if (status) status.textContent = `保存失败：${error.message}`;
  }
}

// ---- 调试页：历史温度数据导出（sql 快照 / csv 宽表） ----

function setupHistoryExport() {
  const toggle = $('history-export-toggle');
  const menu = $('history-export-menu');
  if (!toggle || !menu) return;
  toggle.addEventListener('click', (event) => {
    event.stopPropagation();
    menu.hidden = !menu.hidden;
    toggle.setAttribute('aria-expanded', String(!menu.hidden));
  });
  document.addEventListener('click', (event) => {
    if (!event.target.closest?.('.history-export-wrap')) {
      menu.hidden = true;
      toggle.setAttribute('aria-expanded', 'false');
    }
  });
  $('history-export-sql').addEventListener('click', () => downloadHistoryExport('sql'));
  $('history-export-csv').addEventListener('click', () => downloadHistoryExport('csv'));
}

function downloadHistoryExport(kind) {
  const status = $('debug-status'); // 状态提示复用调试面板的 aria-live 区
  const stamp = new Date().toISOString().slice(0, 10);
  const link = document.createElement('a');
  link.href = baseUrl(`api/history/export/${kind}`);
  link.download = kind === 'sql' ? `tad-module-history-${stamp}.db` : `tad-module-history-${stamp}.csv`;
  document.body.append(link);
  link.click();
  link.remove();
  if (status) status.textContent = kind === 'sql' ? '数据库快照下载已开始。' : 'CSV 导出下载已开始。';
}

function downloadDebugReport() {
  if (!debugReportText) return;
  const stamp = new Date().toISOString().replace(/[:.]/g, '-');
  const blob = new Blob([debugReportText], { type: 'text/plain;charset=utf-8' });
  const link = document.createElement('a');
  link.href = URL.createObjectURL(blob);
  link.download = `tad-module-debug-report-${stamp}.txt`;
  document.body.append(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(link.href);
  setDebugStatus('报告下载已开始。');
}

function openDebugIssue() {
  if (!debugReportText) return;
  const base = 'https://github.com/luodaoyi/TAD6S4N10G-fnos/issues/new';
  const title = 'TAD6S4N10G 模块问题反馈';
  const body = `请描述问题现象、复现步骤和预期结果。\n\n调试报告：\n\n${debugReportText}`;
  const issueURL = `${base}?${new URLSearchParams({ title, body }).toString()}`;
  if (issueURL.length > 1900) {
    window.open(base, '_blank', 'noopener,noreferrer');
    setDebugStatus('报告内容过长，已打开基础 Issue 页面；请先复制报告再粘贴。', true);
    return;
  }
  window.open(issueURL, '_blank', 'noopener,noreferrer');
  setDebugStatus('已打开 GitHub Issue 草稿，请检查内容后手动提交。');
}

function setBusy(busy) {
  uiBusy = busy;
  $('config-form').classList.toggle('busy', busy);
  document.querySelectorAll('button').forEach((button) => {
    if (button.closest('.app-modal')) return;
    button.disabled = busy;
  });
  if (!busy) updateDebugReportActions(Boolean(debugReportText));
  CURVE_KINDS.forEach(updateCurveControls);
}

async function refresh(keepInputs = false) {
  try {
    const status = await request('api/status');
    render(status, keepInputs);
  } catch (error) {
    showMessage(`读取状态失败：${error.message}`, true);
    renderHealthBadge('连接失败', 'error', `无法读取模块状态：${error.message}`);
  }
  const historyPanel = $('panel-fan');
  if (historyPanel && !historyPanel.hidden) fetchHistory();
}

function reportPanelValidity(panelID) {
  const invalid = [...$(panelID).querySelectorAll('[required]')]
    .find((control) => !control.disabled && !control.checkValidity());
  if (!invalid) return true;
  invalid.reportValidity();
  return false;
}

function fanConfigFromInputs() {
  const cpuCurve = curveFromInputs('cpu');
  const hddCurve = curveFromInputs('hdd');
  const nvmeCurve = curveFromInputs('nvme');
  const cpuFanIDs = selectedCurveFanIDs('cpu');
  const hddFanIDs = selectedCurveFanIDs('hdd');
  const nvmeFanIDs = selectedCurveFanIDs('nvme');
  return {
    enabled: $('fan-enabled').checked,
    device_id: cpuFanIDs[0] || hddFanIDs[0] || nvmeFanIDs[0] || '',
    cpu_fan_ids: cpuFanIDs,
    hdd_fan_ids: hddFanIDs,
    nvme_fan_ids: nvmeFanIDs,
    min_pwm_percent: Number($('fan-min').value),
    emergency_temp_c: Number($('fan-emergency').value),
    poll_seconds: Number($('fan-poll').value),
    curve: cpuCurve,
    hdd_curve: hddCurve,
    nvme_curve: nvmeCurve,
    hdd_slot_ids: selectedFanSlotIDs('hdd'),
    nvme_slot_ids: selectedFanSlotIDs('nvme'),
  };
}

$('save-global').addEventListener('click', async () => {
  if (!reportPanelValidity('panel-power')) return;
  const config = {
    enabled: $('enabled').checked,
    pl1_w: Number($('pl1').value),
    pl2_w: Number($('pl2').value),
    reapply_seconds: Number($('interval').value),
  };
  if (config.pl2_w < config.pl1_w) {
    showMessage('PL2 不能低于 PL1。', true, 'message-global');
    return;
  }
  setBusy(true);
  try {
    render(await request('api/config/global', { method: 'POST', body: JSON.stringify(config) }), true);
    showMessage('全局配置已保存并应用；风扇和按钮配置未修改。', false, 'message-global');
  } catch (error) {
    showMessage(`保存失败：${error.message}`, true, 'message-global');
  } finally {
    setBusy(false);
  }
});

$('save-fan').addEventListener('click', async () => {
  if (!reportPanelValidity('panel-fan')) return;
  const config = fanConfigFromInputs();
  if (config.enabled && ![...config.cpu_fan_ids, ...config.hdd_fan_ids, ...config.nvme_fan_ids].length) {
    showMessage('启用风扇曲线前，至少需要为一条温度曲线选择风扇。', true, 'message-fan');
    return;
  }
  const curves = [
    ['CPU', config.curve],
    ['HDD/SATA', config.hdd_curve],
    ['NVMe', config.nvme_curve],
  ];
  for (const [label, curve] of curves) {
    if (curve.length < CURVE_MIN_POINTS || curve.length > CURVE_MAX_POINTS) {
      showMessage(`${label}曲线必须包含 ${CURVE_MIN_POINTS}–${CURVE_MAX_POINTS} 个节点。`, true, 'message-fan');
      return;
    }
    if (curve.some((point, index) => point.pwm_percent < config.min_pwm_percent
        || (index > 0 && (point.temp_c <= curve[index - 1].temp_c || point.pwm_percent < curve[index - 1].pwm_percent)))) {
      showMessage(`${label}曲线温度必须严格递增，转速不能随温度升高而下降，且不能低于最低转速。`, true, 'message-fan');
      return;
    }
    if (config.emergency_temp_c < curve[curve.length - 1].temp_c) {
      showMessage(`紧急满速温度不能低于最后一个${label}曲线节点。`, true, 'message-fan');
      return;
    }
  }
  setBusy(true);
  try {
    render(await request('api/config/fan', { method: 'POST', body: JSON.stringify(config) }), true);
    showMessage('风扇控制与三条温控曲线已保存并应用；其他配置未修改。', false, 'message-fan');
  } catch (error) {
    showMessage(`保存失败：${error.message}`, true, 'message-fan');
  } finally {
    setBusy(false);
  }
});

$('save-gpio').addEventListener('click', async () => {
  if (!commitGPIOScriptEditor()) return;
  const config = gpioConfigFromInputs();
  setBusy(true);
  try {
    render(await request('api/config/gpio', { method: 'POST', body: JSON.stringify(config) }), true);
    showMessage('按钮控制配置已保存；全局和风扇配置未修改。', false, 'message-gpio');
  } catch (error) {
    showMessage(`保存失败：${error.message}`, true, 'message-gpio');
  } finally {
    setBusy(false);
  }
});

$('apply-now').addEventListener('click', async () => {
  setBusy(true);
  try {
    render(await request('api/apply', { method: 'POST', body: '{}' }), true);
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

function setupCurveEditor(kind) {
  const editor = curveEditors[kind];
  const curveChart = $(editor.chartID);
  curveChart.addEventListener('pointerdown', (event) => {
    if (uiBusy) return;
    const node = event.target.closest?.('.curve-node');
    if (!node) return;
    editor.selectedIndex = Number(node.dataset.index);
    editor.draggedIndex = editor.selectedIndex;
    curveChart.classList.add('dragging');
    curveChart.setPointerCapture(event.pointerId);
    event.preventDefault();
    updateCurveFromPointer(kind, event);
  });
  curveChart.addEventListener('pointermove', (event) => {
    if (editor.draggedIndex < 0) return;
    event.preventDefault();
    updateCurveFromPointer(kind, event);
  });
  const finishCurveDrag = (event, update = true) => {
    if (editor.draggedIndex < 0) return;
    if (update) updateCurveFromPointer(kind, event);
    editor.draggedIndex = -1;
    curveChart.classList.remove('dragging');
    if (curveChart.hasPointerCapture(event.pointerId)) curveChart.releasePointerCapture(event.pointerId);
  };
  curveChart.addEventListener('pointerup', finishCurveDrag);
  curveChart.addEventListener('pointercancel', (event) => finishCurveDrag(event, false));
  curveChart.addEventListener('lostpointercapture', () => {
    editor.draggedIndex = -1;
    curveChart.classList.remove('dragging');
  });
  curveChart.addEventListener('keydown', (event) => {
    const node = event.target.closest?.('.curve-node');
    if (node) editor.selectedIndex = Number(node.dataset.index);
    const point = editor.curve[editor.selectedIndex];
    if (!point) return;
    let temperature = point.temp_c;
    let pwm = point.pwm_percent;
    if (event.key === 'ArrowLeft') temperature -= 1;
    else if (event.key === 'ArrowRight') temperature += 1;
    else if (event.key === 'ArrowDown') pwm -= 1;
    else if (event.key === 'ArrowUp') pwm += 1;
    else if (event.key === 'Delete' || event.key === 'Backspace') {
      event.preventDefault();
      removeSelectedCurvePoint(kind);
      return;
    } else return;
    event.preventDefault();
    setCurvePoint(kind, editor.selectedIndex, temperature, pwm);
    renderFanChart(kind);
    requestAnimationFrame(() => curveChart.querySelector(`.curve-node-control[data-index="${editor.selectedIndex}"]`)?.focus());
  });
  $(editor.addID).addEventListener('click', () => addCurvePoint(kind));
  $(editor.removeID).addEventListener('click', () => removeSelectedCurvePoint(kind));
}
CURVE_KINDS.forEach(setupCurveEditor);
function setupCurveChartScaling() {
  const charts = [
    ...CURVE_KINDS.map((kind) => $(curveEditors[kind].chartID)),
    $('history-chart'),
  ].filter(Boolean);
  const update = () => {
    CURVE_KINDS.forEach((kind) => renderFanChart(kind));
    renderHistoryChart();
  };
  if (typeof ResizeObserver === 'function') {
    const observer = new ResizeObserver(update);
    charts.forEach((chart) => observer.observe(chart));
  } else {
    window.addEventListener('resize', update, { passive: true });
  }
  update();
}
setupCurveChartScaling();
$('fan-min').addEventListener('input', () => {
  if ($('fan-min').value !== '') CURVE_KINDS.forEach(normalizeCurveToControls);
});
$('fan-emergency').addEventListener('input', () => {
  if ($('fan-emergency').value !== '') CURVE_KINDS.forEach(normalizeCurveToControls);
});
$('config-form').addEventListener('invalid', (event) => {
  const panel = event.target.closest?.('.tab-panel');
  const tabID = panel?.getAttribute('aria-labelledby');
  if (tabID) activateTab(tabID);
}, true);
setupTabs();
setupHistoryPanel();
setupHealthTooltip();
setupAppModal();
setupFanSlotSelectors();
setupGPIOActions();
document.querySelectorAll('input[name="power-mode"]').forEach((input) => {
  input.addEventListener('change', () => updatePowerMode(input.value, true));
});
$('check-updates').addEventListener('click', checkForUpdates);
$('debug-generate-report').addEventListener('click', generateDebugReport);
$('debug-copy-report').addEventListener('click', copyDebugReport);
$('debug-download-report').addEventListener('click', downloadDebugReport);
$('debug-open-issue').addEventListener('click', openDebugIssue);
setupHistoryExport();
setupSensorNames();
$('gpio-enabled').addEventListener('change', updateGPIOEnabledState);
$('gpio-script-add').addEventListener('click', () => openGPIOScriptEditor());
$('gpio-script-cancel').addEventListener('click', closeGPIOScriptEditor);
$('gpio-script-commit').addEventListener('click', commitGPIOScriptEditor);
$('gpio-script-body').addEventListener('input', renderBashHighlight);
$('gpio-script-body').addEventListener('scroll', syncBashEditorScroll);
$('gpio-script-body').addEventListener('keydown', (event) => {
  if (event.key !== 'Tab') return;
  event.preventDefault();
  const textarea = event.currentTarget;
  const start = textarea.selectionStart;
  textarea.setRangeText('  ', start, textarea.selectionEnd, 'end');
  renderBashHighlight();
});
document.querySelectorAll('.gpio-action').forEach((select) => {
  select.addEventListener('change', renderGPIOScriptList);
});

const THEME_STORAGE_KEY = 'tad-theme';
const THEME_ORDER = ['auto', 'light', 'dark'];
const THEME_META = {
  auto: { label: '主题：跟随 fnOS/系统', icon: '🌗' },
  light: { label: '主题：浅色', icon: '☀️' },
  dark: { label: '主题：深色', icon: '🌙' },
};

function currentThemeMode() {
  try {
    const saved = window.localStorage.getItem(THEME_STORAGE_KEY);
    return THEME_ORDER.includes(saved) ? saved : 'auto';
  } catch (error) {
    return 'auto';
  }
}

// fnOS 没有公开的插件主题接口；插件页与 fnOS 桌面同源内嵌时，读取父窗口
// 明暗实现自动跟随。fnOS 实测（1.2.0505）：桌面主题挂在 <body theme-mode=
// "dark|light"> 属性上（Semi Design 的暗色选择器即 body[theme-mode=dark]），
// <html> 的 class="light" 是引导脚本默认值、从不随主题变；body 无该属性时
// 按根元素背景亮度兜底。跨域或无父窗口时返回 null，退回 prefers-color-scheme。
function detectParentTheme() {
  try {
    if (window.parent === window || !window.parent.document) return null;
    const parent = window.parent;
    const root = parent.document.documentElement;
    const body = parent.document.body;
    const bodyMode = (body.getAttribute('theme-mode') || '').trim().toLowerCase();
    if (bodyMode === 'dark') return 'dark';
    if (bodyMode === 'light') return 'light';
    const themeAttr = `${root.getAttribute('data-theme') || ''} ${root.getAttribute('data-bs-theme') || ''}`;
    const className = `${root.className || ''} ${body.className || ''}`;
    const tokens = `${themeAttr} ${className}`;
    if (/(^|\s)(dark|night)(\s|$)/i.test(tokens)) return 'dark';
    if (/(^|\s)light(\s|$)/i.test(tokens)) return 'light';
    const background = parent.getComputedStyle(root).backgroundColor;
    const rgb = background.match(/rgba?\((\d+),\s*(\d+),\s*(\d+)(?:,\s*([\d.]+))?\)/);
    if (rgb && (rgb[4] === undefined || Number(rgb[4]) > 0.5)) {
      const luminance = 0.2126 * Number(rgb[1]) + 0.7152 * Number(rgb[2]) + 0.0722 * Number(rgb[3]);
      if (luminance < 90) return 'dark';
      if (luminance > 150) return 'light';
    }
  } catch (error) {
    return null; // 父窗口跨域不可读
  }
  return null;
}

function resolveAutoTheme() {
  return detectParentTheme() || (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
}

// theme-color 与 styles.css 两套 --bg 同值（#f3f4f6 / #141519）。fnOS 桌面
// 的窗口标题栏由宿主绘制，插件无法直接改色；meta 供手机浏览器地址栏等
// 外壳取色，切主题时同步，避免深色页面顶着浅色外壳条。
function syncThemeColorMeta(theme) {
  const meta = document.querySelector('meta[name="theme-color"]');
  if (meta) meta.setAttribute('content', theme === 'dark' ? '#141519' : '#f3f4f6');
}

function applyTheme(mode) {
  if (typeof window === 'undefined' || !window.matchMedia) return;
  const theme = mode === 'auto' ? resolveAutoTheme() : mode;
  document.documentElement.dataset.theme = theme;
  syncThemeColorMeta(theme);
  const meta = THEME_META[mode];
  const button = $('theme-toggle');
  if (button) {
    button.textContent = meta.icon;
    button.title = meta.label;
    button.setAttribute('aria-label', meta.label);
  }
}

// 仅跟随档需要监听 fnOS 桌面主题变化；观察器注册在父窗口节点上，不随
// 本 iframe 销毁回收，固定浅/深档或页面卸载时必须显式断开，否则会在
// fnOS 桌面上累积观察器并在父窗口属性变动时对已销毁文档反复取主题。
let parentThemeObserver = null;

function syncParentThemeObserver() {
  if (parentThemeObserver) {
    parentThemeObserver.disconnect();
    parentThemeObserver = null;
  }
  if (currentThemeMode() !== 'auto') return;
  try {
    if (window.parent === window || !window.parent.MutationObserver) return;
    const observer = new window.parent.MutationObserver(() => applyTheme(currentThemeMode()));
    observer.observe(window.parent.document.documentElement, {
      attributes: true, attributeFilter: ['class', 'data-theme', 'data-bs-theme', 'style'],
    });
    observer.observe(window.parent.document.body, {
      attributes: true, attributeFilter: ['class', 'theme-mode', 'style'],
    });
    parentThemeObserver = observer;
  } catch (error) { /* 父窗口不可读时仅跟随浏览器偏好 */ }
}

applyTheme(currentThemeMode());
syncParentThemeObserver();
window.addEventListener('pagehide', () => {
  if (parentThemeObserver) {
    parentThemeObserver.disconnect();
    parentThemeObserver = null;
  }
});
// bfcache 恢复不会重新执行脚本，而 pagehide 已断开观察器：pageshow 时重挂，
// 并重取一次主题（恢复期间 fnOS 桌面可能已切换明暗）。
window.addEventListener('pageshow', () => {
  applyTheme(currentThemeMode());
  syncParentThemeObserver();
});
const themeToggle = $('theme-toggle');
if (themeToggle) {
  themeToggle.addEventListener('click', () => {
    const next = THEME_ORDER[(THEME_ORDER.indexOf(currentThemeMode()) + 1) % THEME_ORDER.length];
    try { window.localStorage.setItem(THEME_STORAGE_KEY, next); } catch (error) { /* 隐私模式等场景下仅本次生效 */ }
    applyTheme(next);
    syncParentThemeObserver();
  });
  window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => applyTheme(currentThemeMode()));
}

CURVE_KINDS.forEach(renderFanChart);
refresh();
setInterval(() => refresh(true), 5000);
