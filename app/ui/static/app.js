const $ = (id) => document.getElementById(id);
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

function maxTemperature(items) {
  if (!Array.isArray(items) || !items.length) return null;
  return Math.max(...items.map((item) => Number(item.celsius)).filter(Number.isFinite));
}

function render(status, keepInputs = false) {
  currentStatus = status;
  const pkg = status.packages?.[0] || {};
  const temperature = maxTemperature(status.temperatures);
  $('cpu-model').textContent = status.cpu_model || '未识别';
  $('temperature').textContent = temperature == null ? '不可用' : `${temperature.toFixed(1)} °C`;
  $('current-pl1').textContent = pkg.has_pl1 ? `${pkg.pl1_w} W` : '不可用';
  $('current-pl2').textContent = pkg.has_pl2 ? `${pkg.pl2_w} W` : '不可用';
  $('profile').textContent = status.profile?.display || '不支持';
  $('gpu-runtime').textContent = status.gpu_runtime?.join('，') || '未暴露';
  $('last-apply').textContent = status.last_apply ? new Date(status.last_apply).toLocaleString() : '尚未应用';
  $('last-error').textContent = status.last_error || '正常';

  const healthy = status.supported && !status.last_error;
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
  if (!keepInputs) {
    $('enabled').checked = Boolean(status.config?.enabled);
    $('pl1').value = status.config?.pl1_w ?? profile.default_pl1_w ?? '';
    $('pl2').value = status.config?.pl2_w ?? profile.default_pl2_w ?? '';
    $('interval').value = status.config?.reapply_seconds ?? 30;
  }
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
  const config = {
    enabled: $('enabled').checked,
    pl1_w: Number($('pl1').value),
    pl2_w: Number($('pl2').value),
    reapply_seconds: Number($('interval').value),
  };
  if (config.pl2_w < config.pl1_w) {
    showMessage('PL2 不能低于 PL1。', true);
    return;
  }
  setBusy(true);
  try {
    render(await request('api/config', { method: 'POST', body: JSON.stringify(config) }));
    showMessage('配置已保存并应用。');
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
    showMessage('已重新应用当前限制。');
  } catch (error) {
    showMessage(`重应用失败：${error.message}`, true);
  } finally {
    setBusy(false);
  }
});

$('restore').addEventListener('click', async () => {
  if (!window.confirm('确定恢复插件首次安装前捕获的功耗限制吗？自动校验仍会在下一周期按已保存配置重应用。')) return;
  setBusy(true);
  try {
    render(await request('api/restore', { method: 'POST', body: '{}' }));
    showMessage('已关闭自动限制，并恢复首次安装前的功耗配置。');
  } catch (error) {
    showMessage(`恢复失败：${error.message}`, true);
  } finally {
    setBusy(false);
  }
});

refresh();
setInterval(() => refresh(true), 10000);
