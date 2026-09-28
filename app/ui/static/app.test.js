'use strict';

// 回归测试（node:test，无第三方依赖）：通过 vm 在最小 DOM/定时器桩中加载
// 真实的 app.js，对纯逻辑函数做断言。运行：node --test app/ui/static/

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function stubElement() {
  const store = { checked: false, value: '', textContent: '' };
  return new Proxy(function stub() {}, {
    get(_target, prop) {
      if (prop === Symbol.toPrimitive || prop === 'toString') return () => '';
      if (prop in store) return store[prop];
      return stubElement();
    },
    set(_target, prop, value) {
      store[prop] = value;
      return true;
    },
    apply() {
      return stubElement();
    },
  });
}

function loadAppContext() {
  const source = fs.readFileSync(path.join(__dirname, 'app.js'), 'utf8');
  const matchMedia = () => ({ matches: false, addEventListener() {} });
  const sandbox = {
    console,
    URLSearchParams,
    setInterval: () => 0,
    clearInterval: () => {},
    setTimeout: () => 0,
    clearTimeout: () => {},
    requestAnimationFrame: () => 0,
    fetch: () => new Promise(() => {}),
    Image: function Image() {},
    ResizeObserver: class ResizeObserver {
      observe() {}
      disconnect() {}
      unobserve() {}
    },
    navigator: { userAgent: 'node-test' },
    location: { reload() {}, href: '', pathname: '/' },
    document: {
      readyState: 'complete',
      getElementById: () => stubElement(),
      createElement: () => stubElement(),
      createElementNS: () => stubElement(),
      createTextNode: () => stubElement(),
      querySelector: () => stubElement(),
      querySelectorAll: () => [],
      addEventListener() {},
      body: stubElement(),
      documentElement: stubElement(),
    },
  };
  sandbox.window = {
    matchMedia,
    addEventListener() {},
    innerWidth: 1280,
    innerHeight: 800,
    location: sandbox.location,
  };
  sandbox.globalThis = sandbox;
  const context = vm.createContext(sandbox);
  vm.runInContext(source, context, { filename: 'app.js' });
  return (name) => vm.runInContext(name, context);
}

const resolve = loadAppContext();

test('盘位覆盖层标签：empty/present/used/warning/unknown 各状态文案统一', () => {
  const storageSlotStatusLabel = resolve('storageSlotStatusLabel');
  assert.equal(storageSlotStatusLabel({ state: 'empty' }, 'empty'), '空置');
  assert.equal(storageSlotStatusLabel({ state: 'present' }), '未使用');
  assert.equal(storageSlotStatusLabel({ state: 'used', activity: 'idle' }), '空闲');
  assert.equal(storageSlotStatusLabel({ state: 'used', activity: 'light' }), '轻载');
  assert.equal(storageSlotStatusLabel({ state: 'used', activity: 'medium' }), '中载');
  assert.equal(storageSlotStatusLabel({ state: 'used', activity: 'heavy' }), '高载');
  assert.equal(storageSlotStatusLabel({ state: 'used', activity: 'busy' }), '繁忙');
  assert.equal(storageSlotStatusLabel({ state: 'used', activity: 'full' }), '满载');
  assert.equal(storageSlotStatusLabel({ state: 'used', activity: 'sleeping' }), '休眠');
  assert.equal(storageSlotStatusLabel({ state: 'warning' }), '告警');
  assert.equal(storageSlotStatusLabel({ state: 'unknown' }), '未知');
});

test('盘位覆盖层标签：used 无活动读数显示"已使用"，不得伪装成 SMART 健康', () => {
  const storageSlotStatusLabel = resolve('storageSlotStatusLabel');
  assert.equal(storageSlotStatusLabel({ state: 'used', activity: 'unknown' }), '未知');
  assert.equal(storageSlotStatusLabel({ state: 'used' }), '已使用');
});

test('盘位覆盖层标签：高温优先于活动状态，前置与 M.2 阈值各自生效', () => {
  const storageSlotStatusLabel = resolve('storageSlotStatusLabel');
  const isStorageHot = resolve('isStorageHot');
  assert.equal(storageSlotStatusLabel({ state: 'used', kind: 'front', temperature_c: 56, activity: 'idle' }), '高温');
  assert.equal(storageSlotStatusLabel({ state: 'used', kind: 'm2', temperature_c: 71, activity: 'idle' }), '高温');
  assert.equal(isStorageHot({ kind: 'front', temperature_c: 55 }), true);
  assert.equal(isStorageHot({ kind: 'm2', temperature_c: 70 }), true);
  assert.equal(isStorageHot({ kind: 'front', temperature_c: 54.9 }), false);
  assert.equal(isStorageHot({ kind: 'm2', temperature_c: 69.9 }), false);
});

test('风扇 1200→0→1200：选择器签名不变，未保存的勾选不触发重建', () => {
  const fanListSignature = resolve('fanListSignature');
  const fans = (rpm) => [
    { id: 'fan0', channel: 0, rpm },
    { id: 'fan1', channel: 1, rpm },
  ];
  const baseline = fanListSignature(fans(1200));
  assert.equal(fanListSignature(fans(0)), baseline);
  assert.equal(fanListSignature(fans(1200)), baseline);
  assert.equal(baseline, 'fan0|fan1');
  assert.notEqual(fanListSignature([{ id: 'fan0' }, { id: 'fan2' }]), baseline);
});

test('已勾选风扇 1200→0→1200 仍可见；未勾选的 0 RPM / 负值通道隐藏', () => {
  const connectedFans = resolve('connectedFans');
  const fans = [
    { id: 'fan0', channel: 0, rpm: 1200 },
    { id: 'fan1', channel: 1, rpm: 1200, selected: true },
    { id: 'fan2', channel: 2, rpm: 0 },
    { id: 'fan3', channel: 3, rpm: -1 },
  ];
  assert.deepEqual(connectedFans({ fans }).map((fan) => fan.id), ['fan0', 'fan1']);
  fans[1].rpm = 0;
  assert.deepEqual(connectedFans({ fans }).map((fan) => fan.id), ['fan0', 'fan1']);
  fans[1].rpm = 1200;
  assert.deepEqual(connectedFans({ fans }).map((fan) => fan.id), ['fan0', 'fan1']);
  assert.equal(connectedFans({ fans: [{ id: 'fan1', rpm: 0 }] }).length, 0);
  assert.equal(connectedFans({ fans: [{ id: 'fan1', rpm: 0, selected: true }] }).length, 1);
});

// ---- 历史图表纯函数 ----

test('历史范围过滤：只保留 now-range 之后的采样', () => {
  const historyFilterRange = resolve('historyFilterRange');
  const nowTs = 1700003600;
  const samples = [
    { ts: nowTs - 90000 },
    { ts: nowTs - 3600 },
    { ts: nowTs - 60 },
    { ts: nowTs },
  ];
  const ranged = historyFilterRange(samples, 1, nowTs); // 1 小时
  assert.deepEqual([...ranged.map((s) => s.ts)], [nowTs - 3600, nowTs - 60, nowTs]);
});

test('历史抽稀：超出上限按步长取样且保留最后一个点', () => {
  const historyThinOut = resolve('historyThinOut');
  const samples = Array.from({ length: 1000 }, (_, i) => ({ ts: i }));
  const thinned = historyThinOut(samples, 100);
  assert.ok(thinned.length <= 101);
  assert.equal(thinned[0].ts, 0);
  assert.equal(thinned[thinned.length - 1].ts, 999);
  assert.ok(historyThinOut(samples, 2000).length === 1000, '不超上限时原样返回');
});

test('历史断口：相邻点间隔超过 2.5 倍采样周期时分段', () => {
  const historySplitSegments = resolve('historySplitSegments');
  const samples = [
    { ts: 0 }, { ts: 60 }, { ts: 120 },
    { ts: 1200 }, // 缺口 1080s > 60*2.5
    { ts: 1260 },
  ];
  const segments = historySplitSegments(samples, 60);
  assert.deepEqual([...segments.map((segment) => segment.length)], [3, 2]);
  assert.equal(historySplitSegments([{ ts: 0 }, { ts: 150 }], 60).length, 1, '2.5 倍以内不断开');
});

test('历史数值刻度：步长取 1/2/5 档，覆盖数据范围', () => {
  const historyNiceTicks = resolve('historyNiceTicks');
  assert.deepEqual([...historyNiceTicks(20, 90, 5)], [20, 40, 60, 80]);
  assert.deepEqual([...historyNiceTicks(0, 2000, 4)], [0, 500, 1000, 1500, 2000]);
  assert.equal(historyNiceTicks(5, 5, 4).length, 0, '范围为空时无刻度');
});

test('历史温度轴范围：下界放 5 度并按 5 度取整（最低 35 → 从 30 起）', () => {
  const historyYBounds = resolve('historyYBounds');
  const fallback = historyYBounds([], 20, 90);
  assert.equal(fallback.lo, 20);
  assert.equal(fallback.hi, 90);
  const bounds = historyYBounds([35, 48, 62], 20, 90);
  assert.equal(bounds.lo, 30, '最低 35 度时下界应为 30');
  assert.ok(bounds.hi >= 64, `上界应留余量: ${bounds.hi}`);
  const zeroIgnored = historyYBounds([0, 0, 55], 20, 90);
  assert.ok(zeroIgnored.lo <= 50, '0 值（不可用）不参与范围计算');
  const cold = historyYBounds([38], 20, 90);
  assert.equal(cold.lo, 30);
});

test('历史时间刻度：各档位固定步长（30 分钟→1 分钟 … 30 天→1 天）', () => {
  const historyTimeTicks = resolve('historyTimeTicks');
  const start = 1700000000;
  const halfHour = historyTimeTicks(start, start + 1800, 0.5);
  assert.ok(halfHour.length >= 29 && halfHour.length <= 31, `30min ticks ${halfHour.length}`);
  assert.ok(halfHour.every((ts) => ts % 60 === 0), '30 分钟档刻度应对齐分钟');
  const dayTicks = historyTimeTicks(start, start + 86400, 24);
  assert.ok(dayTicks.length >= 11 && dayTicks.length <= 13, `24h ticks ${dayTicks.length}`);
  assert.ok(dayTicks.every((ts) => ts % 7200 === 0), '24h 范围刻度应为 2 小时步长');
  const weekEnd = start + 7 * 86400;
  const weekTicks = historyTimeTicks(start, weekEnd, 168);
  assert.ok(weekTicks.length >= 27 && weekTicks.length <= 29, `7d ticks ${weekTicks.length}`);
  assert.ok(weekTicks.every((ts) => ts % 21600 === 0), '7 天范围刻度应为 6 小时步长');
  const monthEnd = start + 30 * 86400;
  const monthTicks = historyTimeTicks(start, monthEnd, 720);
  assert.ok(monthTicks.length >= 29 && monthTicks.length <= 31, `month ticks ${monthTicks.length}`);
  assert.ok(monthTicks.every((ts) => ts % 86400 === 0), '30 天范围刻度应为 1 天步长');
});

test('历史范围过滤与断口分段配合：先按原始间隔切段，再抽稀', () => {
  const historySplitSegments = resolve('historySplitSegments');
  const historyThinOut = resolve('historyThinOut');
  // 30 个连续点 + 10 分钟停机断口 + 30 个连续点
  const samples = [];
  for (let i = 0; i < 30; i++) samples.push({ ts: i * 60, cpu_c: 50 });
  for (let i = 0; i < 30; i++) samples.push({ ts: 3000 + i * 60, cpu_c: 55 });
  const segments = historySplitSegments(samples, 60);
  assert.deepEqual([...segments.map((segment) => segment.length)], [30, 30]);
  const thinned = segments.map((segment) => historyThinOut(segment, 10));
  assert.ok(thinned.every((segment) => segment.length <= 11), '每段抽稀后不超上限且保留末点');
});

test('历史时间格式：本地 HH:MM 两位补零', () => {
  const historyFormatClock = resolve('historyFormatClock');
  const ts = new Date(2026, 8, 15, 9, 5).getTime() / 1000;
  assert.equal(historyFormatClock(ts), '09:05');
});
test('历史分组子类：空通道过滤、组别归类、聚合项与勾选可见性', () => {
  const historyGroupChildIDs = resolve('historyGroupChildIDs');
  const historyGroupSeries = resolve('historyGroupSeries');
  const historyChildValue = resolve('historyChildValue');
  const historyFanLabel = resolve('historyFanLabel');
  const historySlotLabel = resolve('historySlotLabel');
  const samples = [
    { ts: 0, cpu_c: 55, hdd_c: 41, nvme_c: 48,
      sensors: [{ group: 'cpu', key: 'Core 0', c: 55 }, { group: 'cpu', key: 'Package id 0', c: 52 }, { group: 'nic', key: 'igc:PHY', c: 62 }, { group: 'other', key: 'acpi:temp1', c: 30 }],
      fans: [{ id: 'it8613:fan2', rpm: 0, pwm_percent: 0 }, { id: 'it8613:fan3', rpm: 1200, pwm_percent: 45 }],
      disks: [{ id: 'front-2', c: 41.2 }, { id: 'm2-1', c: 48 }] },
    { ts: 60, cpu_c: 57, hdd_c: 42, nvme_c: 49,
      sensors: [{ group: 'cpu', key: 'Core 0', c: 57 }, { group: 'cpu', key: 'Package id 0', c: 53 }, { group: 'nic', key: 'igc:PHY', c: 63 }, { group: 'other', key: 'acpi:temp1', c: 31 }],
      fans: [{ id: 'it8613:fan2', rpm: 0, pwm_percent: 0 }, { id: 'it8613:fan3', rpm: 1300, pwm_percent: 55 }],
      disks: [{ id: 'front-2', c: 42 }, { id: 'm2-1', c: 49 }] },
  ];
  // CPU 组：聚合项 + 两个传感器；SATA/NVMe 组：聚合项 + 各自盘位（不串组）
  assert.deepEqual([...historyGroupChildIDs('cpu', samples)], ['__agg__', 'Core 0', 'Package id 0']);
  assert.deepEqual([...historyGroupChildIDs('sata', samples)], ['__agg__', 'front-2']);
  assert.deepEqual([...historyGroupChildIDs('nvme', samples)], ['__agg__', 'm2-1']);
  // 网卡组：只有网卡传感器；其它组：非网卡非 CPU 传感器
  assert.deepEqual([...historyGroupChildIDs('nic', samples)], ['igc:PHY']);
  assert.deepEqual([...historyGroupChildIDs('other', samples)], ['acpi:temp1']);
  // 风扇组：全 0 空通道过滤
  assert.deepEqual([...historyGroupChildIDs('fan', samples)], ['it8613:fan3']);
  assert.equal(historyFanLabel('it8613:fan3'), 'fan3');
  assert.equal(historySlotLabel('front-2'), '前置2');

  // 子类取值：聚合与单盘
  assert.equal(historyChildValue('sata', '__agg__', samples[0]), 41);
  assert.equal(historyChildValue('sata', 'front-2', samples[1]), 42);
  assert.equal(historyChildValue('cpu', 'Core 0', samples[1]), 57);
  assert.equal(historyChildValue('nic', 'igc:PHY', samples[0]), 62);

  // 组曲线可见性：默认全选（null）→ 全部子类；只勾聚合项 → 一条线
  const allSeries = historyGroupSeries('sata', samples, 60);
  assert.equal(allSeries.length, 2);
  const setHistoryChildSelection = resolve('setHistoryChildSelection');
  setHistoryChildSelection('sata', new Set(['__agg__']));
  const aggOnly = historyGroupSeries('sata', samples, 60);
  setHistoryChildSelection('sata', null);
  assert.equal(aggOnly.length, 1, '只勾聚合项时应只有一条线');
  assert.equal(aggOnly[0].id, 'sata:__agg__');
  assert.equal(aggOnly[0].color, '#18a779');
});

test('index.html 面板结构：相邻 tab-panel 之间 section 开闭配对，防止面板被嵌套', () => {
  const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
  const markers = [...html.matchAll(/id="panel-[a-z]+"/g)].map((match) => match.index);
  assert.ok(markers.length >= 7, '应存在至少 7 个面板');
  for (let i = 0; i < markers.length - 1; i++) {
    const segment = html.slice(markers[i], markers[i + 1]);
    const opens = (segment.match(/<section\b/g) || []).length;
    const closes = (segment.match(/<\/section>/g) || []).length;
    assert.equal(opens, closes, `panel 区间 ${i}（${html.slice(markers[i], markers[i] + 60)}…）section 未配对，会把后续面板嵌套进去`);
  }
});
