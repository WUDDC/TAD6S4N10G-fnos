package powerguard

import (
	"bytes"
	"context"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func historyTestStatus() Status {
	return Status{
		CPUTemperature: CPUTemperatureStatus{Available: true, DisplayC: 55.5},
		Storage: StorageStatus{Slots: []StorageSlot{
			{ID: "front-1", Kind: "front", TemperatureC: 41.5},
			{ID: "front-2", Kind: "front", TemperatureC: 39.0},
			{ID: "m2-1", Kind: "m2", TemperatureC: 48.0},
			{ID: "m2-2", Kind: "m2"},
			{ID: "front-3", Kind: "front", State: StorageEmpty},
		}},
		FanControl: FanControlStatus{Fans: []FanStatus{
			{ID: "fan", RPM: 1320, PWMPercent: 60},
			{ID: "dead", RPM: 0, PWMPercent: 0},
		}},
	}
}

func TestSampleFromStatusTakesHottestSlotPerKind(t *testing.T) {
	now := time.Unix(1700000000, 0)
	sample := SampleFromStatus(statusPtr(historyTestStatus()), now)
	if sample.TS != now.Unix() {
		t.Fatalf("ts: got %d, want %d", sample.TS, now.Unix())
	}
	if sample.CPUC != 55.5 {
		t.Fatalf("cpu: got %v, want 55.5", sample.CPUC)
	}
	if sample.HDDC != 41.5 {
		t.Fatalf("hdd: got %v, want 41.5 (hottest front slot)", sample.HDDC)
	}
	if sample.NVMeC != 48.0 {
		t.Fatalf("nvme: got %v, want 48", sample.NVMeC)
	}
	if len(sample.Fans) != 1 || sample.Fans[0].ID != "fan" || sample.Fans[0].RPM != 1320 || sample.Fans[0].PWMPercent != 60 {
		t.Fatalf("fans: dead channel should be excluded, got %+v", sample.Fans)
	}
}

func TestSampleFromStatusNilStatusKeepsTimestamp(t *testing.T) {
	now := time.Unix(1700000001, 0)
	sample := SampleFromStatus(nil, now)
	if sample.TS != now.Unix() || sample.CPUC != 0 || sample.HDDC != 0 || sample.NVMeC != 0 || len(sample.Fans) != 0 {
		t.Fatalf("empty sample: got %+v", sample)
	}
}

func TestSampleFromStatusWithoutCPUAvailability(t *testing.T) {
	st := historyTestStatus()
	st.CPUTemperature.Available = false
	sample := SampleFromStatus(&st, time.Now())
	if sample.CPUC != 0 {
		t.Fatalf("cpu should stay zero when unavailable, got %v", sample.CPUC)
	}
}

func TestSampleFromStatusClassifiesExtraSensors(t *testing.T) {
	st := historyTestStatus()
	st.ExtraTemperatures = []Temperature{
		{Label: "igc:PHY", Celsius: 62},
		{Label: "i915:temp1", Celsius: 36},
		{Label: "acpitz:temp1", Celsius: 27.8},
		{Label: "spd5118:temp1", Celsius: 35.5},
	}
	sample := SampleFromStatus(statusPtr(st), time.Unix(1700000002, 0))
	groups := map[string]string{}
	for _, sensor := range sample.Sensors {
		if sensor.Group == "cpu" {
			continue
		}
		groups[sensor.Key] = sensor.Group
	}
	if groups["igc:PHY"] != "nic" {
		t.Fatalf("igc should be nic, got %q", groups["igc:PHY"])
	}
	if groups["i915:temp1"] != "gpu" {
		t.Fatalf("i915 should be gpu, got %q", groups["i915:temp1"])
	}
	if groups["acpitz:temp1"] != "other" {
		t.Fatalf("acpitz should be other, got %q", groups["acpitz:temp1"])
	}
	if groups["spd5118:temp1"] != "other" {
		t.Fatalf("spd5118 should be other, got %q", groups["spd5118:temp1"])
	}
}

func statusPtr(st Status) *Status {
	return &st
}

func newTestStore(t *testing.T) *HistoryStore {
	t.Helper()
	store, err := NewHistoryStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestHistoryStoreAppendPersistsAndReloads(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history.db")
	store, err := NewHistoryStore(path)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-2 * time.Minute)
	want := []HistorySample{
		{TS: base.Unix(), CPUC: 50, HDDC: 40, NVMeC: 45, Fans: []HistoryFanSample{{ID: "fan", RPM: 1200, PWMPercent: 50}}},
		{TS: base.Add(time.Minute).Unix(), CPUC: 51},
	}
	for _, sample := range want {
		if err := store.Append(sample); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	store.Close()

	reloaded, err := NewHistoryStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	samples, interval, err := reloaded.Aggregated(context.Background(), historyMaxRangeHours, 2000, base.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if interval != 60 {
		t.Fatalf("interval: got %d, want 60", interval)
	}
	if len(samples) != len(want) {
		t.Fatalf("reloaded len: got %d, want %d", len(samples), len(want))
	}
	for i := range want {
		if samples[i].TS != want[i].TS || samples[i].CPUC != want[i].CPUC || samples[i].HDDC != want[i].HDDC || samples[i].NVMeC != want[i].NVMeC {
			t.Fatalf("sample %d: got %+v, want %+v", i, samples[i], want[i])
		}
	}
	if len(samples[0].Fans) != 1 || samples[0].Fans[0].RPM != 1200 || samples[0].Fans[0].PWMPercent != 50 {
		t.Fatalf("fans: got %+v", samples[0].Fans)
	}
}

func TestHistoryStoreUpsertOverwritesSameTimestamp(t *testing.T) {
	store := newTestStore(t)
	ts := time.Unix(1700000000, 0)
	if err := store.Append(HistorySample{TS: ts.Unix(), CPUC: 50, Fans: []HistoryFanSample{{ID: "fan", RPM: 1000, PWMPercent: 40}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(HistorySample{TS: ts.Unix(), CPUC: 52, Fans: []HistoryFanSample{{ID: "fan", RPM: 1400, PWMPercent: 62}}}); err != nil {
		t.Fatal(err)
	}
	samples, _, err := store.Aggregated(context.Background(), historyMaxRangeHours, 2000, ts.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || samples[0].CPUC != 52 {
		t.Fatalf("upsert should keep one row with new values: %+v", samples)
	}
	if len(samples[0].Fans) != 1 || samples[0].Fans[0].RPM != 1400 || samples[0].Fans[0].PWMPercent != 62 {
		t.Fatalf("fan rows should be replaced: %+v", samples[0].Fans)
	}
}

func TestHistoryAggregatedPeaksAndStride(t *testing.T) {
	store := newTestStore(t)
	now := time.Unix(1700000000, 0)
	// 600 个点、每分钟一个，其中插一个 99 度尖峰 + 风扇峰值
	for i := 600; i >= 1; i-- {
		sample := HistorySample{TS: now.Unix() - int64(i*60), CPUC: 50}
		if i == 300 {
			sample.CPUC = 99
			sample.Fans = []HistoryFanSample{{ID: "fan", RPM: 2000, PWMPercent: 90}}
		}
		if err := store.Append(sample); err != nil {
			t.Fatal(err)
		}
	}
	samples, interval, err := store.Aggregated(context.Background(), 10, 100, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) > 100 {
		t.Fatalf("aggregated len %d exceeds maxPoints 100", len(samples))
	}
	if interval <= 60 {
		t.Fatalf("aggregated interval should exceed base interval, got %d", interval)
	}
	peak := 0.0
	fanPWM := 0
	for _, sample := range samples {
		if sample.CPUC > peak {
			peak = sample.CPUC
		}
		for _, fan := range sample.Fans {
			if fan.PWMPercent > fanPWM {
				fanPWM = fan.PWMPercent
			}
		}
	}
	if peak != 99 {
		t.Fatalf("peak must survive aggregation, got %v", peak)
	}
	if fanPWM != 90 {
		t.Fatalf("fan peak must survive aggregation, got %d", fanPWM)
	}
}

func TestHistoryAggregatedRangeFilter(t *testing.T) {
	store := newTestStore(t)
	now := time.Unix(1700000000, 0)
	// 一条 2 小时前的旧数据 + 一条 5 分钟前的新数据
	if err := store.Append(HistorySample{TS: now.Unix() - 7200, CPUC: 30}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(HistorySample{TS: now.Unix() - 300, CPUC: 55}); err != nil {
		t.Fatal(err)
	}
	samples, _, err := store.Aggregated(context.Background(), 1, 2000, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || samples[0].CPUC != 55 {
		t.Fatalf("range query should only return recent sample: %+v", samples)
	}
}

func TestHistoryPruneRemovesExpiredRows(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	if err := store.Append(HistorySample{TS: now.Add(-40 * 24 * time.Hour).Unix(), CPUC: 30}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(HistorySample{TS: now.Unix(), CPUC: 55}); err != nil {
		t.Fatal(err)
	}
	if err := store.Prune(now); err != nil {
		t.Fatal(err)
	}
	samples, _, err := store.Aggregated(context.Background(), historyMaxRangeHours, 2000, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || samples[0].CPUC != 55 {
		t.Fatalf("expired rows should be pruned: %+v", samples)
	}
}

func TestHistoryPruneKeepsTwoDayBufferBeyondDisplayWindow(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	// 展示窗口 30 天之外、存储 32 天之内：31 天前的数据必须保留
	if err := store.Append(HistorySample{TS: now.Add(-31 * 24 * time.Hour).Unix(), CPUC: 31}); err != nil {
		t.Fatal(err)
	}
	// 超过存储 32 天：轮转删除
	if err := store.Append(HistorySample{TS: now.Add(-33 * 24 * time.Hour).Unix(), CPUC: 33}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(HistorySample{TS: now.Add(-29 * 24 * time.Hour).Unix(), CPUC: 29}); err != nil {
		t.Fatal(err)
	}
	if err := store.Prune(now); err != nil {
		t.Fatal(err)
	}
	// 展示窗口 30 天：Aggregated 只能看到 29d，31d 落在窗口外但仍在库中
	samples, _, err := store.Aggregated(context.Background(), historyMaxRangeHours, 2000, now)
	if err != nil {
		t.Fatal(err)
	}
	visible := make([]float64, 0, len(samples))
	for _, sample := range samples {
		if sample.CPUC > 0 {
			visible = append(visible, sample.CPUC)
		}
	}
	if len(visible) != 1 || visible[0] != 29 {
		t.Fatalf("display window should only see the 29d sample, got: %v", visible)
	}
	// 直接查库：31d 必须保留（2 天缓冲），33d 必须已轮转删除
	var surviving int64
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM history WHERE cpu_c = 31`).Scan(&surviving); err != nil {
		t.Fatal(err)
	}
	if surviving != 1 {
		t.Fatalf("31d sample must survive within 32-day storage buffer, got %d rows", surviving)
	}
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM history WHERE cpu_c = 33`).Scan(&surviving); err != nil {
		t.Fatal(err)
	}
	if surviving != 0 {
		t.Fatalf("33d sample must rotate beyond 32-day retention, got %d rows", surviving)
	}
}

func TestHistoryAggregatedClampsBadParams(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	// 30 分钟前（0.5h 下限内）与 2 小时前（下限外）各一个点
	if err := store.Append(HistorySample{TS: now.Unix() - 7200, CPUC: 20}); err != nil {
		t.Fatal(err)
	}
	if err := store.Append(HistorySample{TS: now.Unix() - 600, CPUC: 50}); err != nil {
		t.Fatal(err)
	}
	samples, _, err := store.Aggregated(context.Background(), 0, 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if samples == nil {
		t.Fatal("aggregated should never return nil")
	}
	if len(samples) != 1 || samples[0].CPUC != 50 {
		t.Fatalf("range=0 should clamp to 0.5h and exclude 2h-old sample: %+v", samples)
	}
}

func TestHistoryAggregatedMultipleFansPerIDPeaks(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	// 12 个点、两个常驻风扇 + 一个只在偶数点出现的风扇，聚合后各自保留峰值
	for i := 12; i >= 1; i-- {
		sample := HistorySample{TS: now.Unix() - int64(i*60)}
		sample.Fans = []HistoryFanSample{
			{ID: "fan0", RPM: int64(1000 + i), PWMPercent: 40 + i},
			{ID: "fan1", RPM: int64(2000 - i), PWMPercent: 60 - i},
		}
		if i%2 == 0 {
			sample.Fans = append(sample.Fans, HistoryFanSample{ID: "fan2", RPM: 3000 + int64(i), PWMPercent: 80})
		}
		if err := store.Append(sample); err != nil {
			t.Fatal(err)
		}
	}
	samples, _, err := store.Aggregated(context.Background(), 1, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) == 0 {
		t.Fatal("no samples returned")
	}
	fans := map[string]HistoryFanSample{}
	for _, fan := range samples[len(samples)-1].Fans {
		fans[fan.ID] = fan
	}
	if len(fans) != 3 {
		t.Fatalf("expect 3 fan ids, got %d: %+v", len(fans), samples[len(samples)-1].Fans)
	}
	// 最后一个桶由 i=2（有 fan2）与 i=1 两个采样聚合而成
	if fans["fan0"].RPM != 1002 || fans["fan0"].PWMPercent != 42 {
		t.Fatalf("fan0 peak wrong: %+v", fans["fan0"])
	}
	if fans["fan1"].RPM != 1999 || fans["fan1"].PWMPercent != 59 {
		t.Fatalf("fan1 peak wrong: %+v", fans["fan1"])
	}
	if fans["fan2"].RPM != 3002 || fans["fan2"].PWMPercent != 80 {
		t.Fatalf("fan2 peak wrong: %+v", fans["fan2"])
	}
}

func TestHistoryStorePerDiskTemperaturesPersistAndAggregate(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	// 12 个点：front-2 温度爬升，front-1 恒定；聚合后保留各盘峰值
	for i := 12; i >= 1; i-- {
		sample := HistorySample{TS: now.Unix() - int64(i*60), CPUC: 50}
		sample.Disks = []HistoryDiskSample{
			{ID: "front-2", TemperatureC: float64(40 + i)},
			{ID: "front-1", TemperatureC: 39},
		}
		if err := store.Append(sample); err != nil {
			t.Fatal(err)
		}
	}
	samples, _, err := store.Aggregated(context.Background(), 1, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	last := samples[len(samples)-1]
	if len(last.Disks) != 2 {
		t.Fatalf("expect 2 disks, got %+v", last.Disks)
	}
	byID := map[string]float64{}
	for _, disk := range last.Disks {
		byID[disk.ID] = disk.TemperatureC
	}
	if byID["front-2"] != 42 {
		t.Fatalf("front-2 peak should be 42, got %v", byID["front-2"])
	}
	if byID["front-1"] != 39 {
		t.Fatalf("front-1 should stay 39, got %v", byID["front-1"])
	}

	// 换个库重开验证落盘
	path := filepath.Join(t.TempDir(), "history.db")
	persist, err := NewHistoryStore(path)
	if err != nil {
		t.Fatal(err)
	}
	sample := HistorySample{TS: now.Unix(), CPUC: 50, Disks: []HistoryDiskSample{{ID: "m2-1", TemperatureC: 47.5}}}
	if err := persist.Append(sample); err != nil {
		t.Fatal(err)
	}
	persist.Close()
	reloaded, err := NewHistoryStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	reloadedSamples, _, err := reloaded.Aggregated(context.Background(), 1, 2000, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(reloadedSamples) != 1 || len(reloadedSamples[0].Disks) != 1 || reloadedSamples[0].Disks[0].ID != "m2-1" || reloadedSamples[0].Disks[0].TemperatureC != 47.5 {
		t.Fatalf("per-disk reload mismatch: %+v", reloadedSamples)
	}
}

func TestHistoryAggregatedHalfHourRange(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	// 31 个点覆盖 30 分钟；更早放一个 1 小时前的点
	if err := store.Append(HistorySample{TS: now.Unix() - 3600, CPUC: 20}); err != nil {
		t.Fatal(err)
	}
	for i := 30; i >= 0; i-- {
		if err := store.Append(HistorySample{TS: now.Unix() - int64(i*60), CPUC: 50}); err != nil {
			t.Fatal(err)
		}
	}
	samples, interval, err := store.Aggregated(context.Background(), 0.5, 2000, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 31 {
		t.Fatalf("half-hour range should return 31 samples, got %d", len(samples))
	}
	for _, sample := range samples {
		if sample.TS < now.Unix()-1800-60 {
			t.Fatalf("sample older than 30min returned: %d", sample.TS)
		}
	}
	if interval != 60 {
		t.Fatalf("interval: got %d, want 60", interval)
	}
}

func TestHistoryExportSQLiteSnapshot(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	if err := store.Append(HistorySample{TS: now.Unix(), CPUC: 52, Fans: []HistoryFanSample{{ID: "fan3", RPM: 1200, PWMPercent: 45}}, Disks: []HistoryDiskSample{{ID: "front-2", TemperatureC: 41}}, Sensors: []HistorySensorSample{{Group: "other", Key: "acpitz:temp1", C: 28}}}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := store.ExportSQLite(context.Background(), &buf); err != nil {
		t.Fatalf("export: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("export produced empty output")
	}
	// 快照应能作为独立数据库重新打开并读出数据
	snapPath := filepath.Join(t.TempDir(), "snap.db")
	if err := os.WriteFile(snapPath, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	snap, err := NewHistoryStore(snapPath)
	if err != nil {
		t.Fatalf("reopen snapshot: %v", err)
	}
	defer snap.Close()
	samples, _, err := snap.Aggregated(context.Background(), historyMaxRangeHours, 2000, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || samples[0].CPUC != 52 || len(samples[0].Fans) != 1 || len(samples[0].Disks) != 1 || len(samples[0].Sensors) != 1 {
		t.Fatalf("snapshot data mismatch: %+v", samples)
	}
}

func TestHistoryExportCSVWideTable(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	if err := store.Append(HistorySample{
		TS: now.Unix(), CPUC: 52.5, HDDC: 41, NVMeC: 48,
		Fans:    []HistoryFanSample{{ID: "it87:fan3", RPM: 1200, PWMPercent: 45}},
		Disks:   []HistoryDiskSample{{ID: "front-2", TemperatureC: 41.2}},
		Sensors: []HistorySensorSample{{Group: "cpu", Key: "Core 0", C: 52.5}, {Group: "other", Key: "acpitz:temp1", C: 28}},
	}); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := store.WriteCSV(context.Background(), &buf); err != nil {
		t.Fatalf("csv: %v", err)
	}
	content := buf.String()
	if !strings.HasPrefix(content, "\xef\xbb\xbfts,time,cpu_c,hdd_c,nvme_c") {
		t.Fatalf("csv should start with BOM and fixed header, got: %q", content[:60])
	}
	for _, want := range []string{"fan_it87:fan3_rpm", "fan_it87:fan3_pwm", "disk_front-2_c", "sensor_cpu_Core 0_c", "sensor_other_acpitz:temp1_c", "1200", "45", "41.2", "52.5"} {
		if !strings.Contains(content, want) {
			t.Fatalf("csv missing %q:\n%s", want, content)
		}
	}
	// 第二个采样缺风扇数据时对应列应为空
	if err := store.Append(HistorySample{TS: now.Add(time.Minute).Unix(), CPUC: 53}); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := store.WriteCSV(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	headerFields := strings.Split(strings.Split(buf.String(), "\n")[0], ",")
	fanRpmIdx := -1
	for i, name := range headerFields {
		if strings.HasSuffix(name, "_rpm") {
			fanRpmIdx = i
			break
		}
	}
	if fanRpmIdx < 0 {
		t.Fatalf("header missing fan rpm column: %v", headerFields)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expect header + 2 rows, got %d", len(lines))
	}
	lastFields := strings.Split(lines[2], ",")
	if lastFields[fanRpmIdx] != "" || lastFields[fanRpmIdx+1] != "" {
		t.Fatalf("second sample has no fan data, fan columns should be empty: %v", lastFields)
	}
}

func TestSaveSensorSettingsPersistsAndCleans(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "proc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "proc", "cpuinfo"), []byte("model name : Intel(R) Processor N100\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{Root: dir, ConfigPath: filepath.Join(dir, "config.json"), StatePath: filepath.Join(dir, "state.json"), Version: "test"}
	if _, err := manager.LoadOrCreateConfig(); err != nil {
		t.Fatal(err)
	}
	if err := manager.SaveSensorSettings(
		map[string]string{
			"it8613:temp1":          "主板温度",
			"  acpitz:temp1  ":      "  ACPI 温区 ",
			"igc:PHY":               "",
			strings.Repeat("x", 81): "过长键应被丢弃",
		},
		map[string]string{
			"  mlx5:temp1 ":         " nic ",
			"i915:temp1":            "other",
			"acpitz:temp1":          "cpu",   // 非法父类：丢弃
			"it8613:temp1":          "bogus", // 非法父类：丢弃
			"":                      "nic",   // 空键：丢弃
			strings.Repeat("y", 81): "nic",   // 过长键：丢弃
		}); err != nil {
		t.Fatal(err)
	}
	cfg, err := manager.LoadOrCreateConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.SensorNames["it8613:temp1"] != "主板温度" {
		t.Fatalf("normal name missing: %+v", cfg.SensorNames)
	}
	if cfg.SensorNames["acpitz:temp1"] != "ACPI 温区" {
		t.Fatalf("key/value should be trimmed: %+v", cfg.SensorNames)
	}
	if _, exists := cfg.SensorNames["igc:PHY"]; exists {
		t.Fatalf("empty value should be dropped: %+v", cfg.SensorNames)
	}
	if _, exists := cfg.SensorNames[strings.Repeat("x", 81)]; exists {
		t.Fatalf("oversized key should be dropped: %+v", cfg.SensorNames)
	}
	if cfg.SensorGroups["mlx5:temp1"] != "nic" || cfg.SensorGroups["i915:temp1"] != "other" {
		t.Fatalf("valid groups missing: %+v", cfg.SensorGroups)
	}
	if len(cfg.SensorGroups) != 2 {
		t.Fatalf("invalid/empty/oversized groups should be dropped: %+v", cfg.SensorGroups)
	}
	// 清空场景：传空 map 应清掉已有名称与归属
	if err := manager.SaveSensorSettings(map[string]string{}, map[string]string{}); err != nil {
		t.Fatal(err)
	}
	cfg, err = manager.LoadOrCreateConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.SensorNames) != 0 || len(cfg.SensorGroups) != 0 {
		t.Fatalf("empty save should clear names and groups: %+v %+v", cfg.SensorNames, cfg.SensorGroups)
	}
}

func TestHistorySizeLimitPrunesOldestDays(t *testing.T) {
	store := newTestStore(t)
	now := time.Now()
	base := now.Add(-6 * 24 * time.Hour)
	// 6 天逐小时采样，每个点带风扇/盘位/传感器行，撑起可观测的库体积
	for i := 0; i <= 144; i++ {
		sample := HistorySample{
			TS:      base.Add(time.Duration(i) * time.Hour).Unix(),
			CPUC:    40 + float64(i%10),
			Fans:    []HistoryFanSample{{ID: "it8613:it87.2608:fan2", RPM: 900, PWMPercent: 40}},
			Disks:   []HistoryDiskSample{{ID: "front-2", TemperatureC: 41.5}},
			Sensors: []HistorySensorSample{{Group: "cpu", Key: "Core 0", C: 45}},
		}
		if err := store.Append(sample); err != nil {
			t.Fatal(err)
		}
	}
	newest := base.Add(144 * time.Hour).Unix()
	before := store.dbSizeBytes()
	if before <= 1<<20 {
		t.Fatalf("test dataset should exceed the 1MB limit, got %d bytes", before)
	}
	if err := store.PruneIfNeeded(now, 1); err != nil { // 1MB 上限：MB→字节换算 + 按天删最旧
		t.Fatal(err)
	}
	if store.dbSizeBytes() > 1<<20 {
		t.Fatalf("size limit not enforced: %d bytes", store.dbSizeBytes())
	}
	samples, _, err := store.Aggregated(context.Background(), historyMaxRangeHours, 2000, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) == 0 || samples[len(samples)-1].TS != newest {
		t.Fatalf("newest sample must survive size pruning: %+v", samples)
	}
	if samples[0].TS <= base.Unix() {
		t.Fatalf("pruning should remove from the oldest side: kept ts=%d", samples[0].TS)
	}
	// 0 表示不启用大小限制，此时不得报错也不得继续删除
	kept := len(samples)
	if err := store.PruneIfNeeded(now, 0); err != nil {
		t.Fatal(err)
	}
	samples, _, _ = store.Aggregated(context.Background(), historyMaxRangeHours, 2000, now)
	if len(samples) != kept {
		t.Fatalf("disabled size limit must not prune: before=%d after=%d", kept, len(samples))
	}
}

func TestHistoryLoopSkipsAppendWhenDisabled(t *testing.T) {
	dir := t.TempDir()
	procDir := filepath.Join(dir, "proc")
	if err := os.MkdirAll(procDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procDir, "cpuinfo"), []byte("model name : Intel(R) Processor N100\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(dir, "config.json")
	statePath := filepath.Join(dir, "state.json")
	manager := &Manager{Root: dir, ConfigPath: configPath, StatePath: statePath}
	if _, err := manager.LoadOrCreateConfig(); err != nil {
		t.Fatalf("config setup: %v", err)
	}
	if !manager.HistorySettings().Enabled {
		t.Fatal("history should default to enabled")
	}
	if err := manager.SaveHistoryConfig(HistoryConfig{Enabled: false, MaxSizeMB: 64}); err != nil {
		t.Fatal(err)
	}
	if manager.HistorySettings().Enabled {
		t.Fatal("history should be disabled after save")
	}
	store := newTestStore(t)
	logger := log.New(os.Stderr, "", 0)
	for i := 0; i < 3; i++ {
		store.appendAndLog(context.Background(), manager, logger)
	}
	samples, _, err := store.Aggregated(context.Background(), historyMaxRangeHours, 2000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 0 {
		t.Fatalf("disabled history must not write samples: %+v", samples)
	}
	// 重新启用后恢复写入
	if err := manager.SaveHistoryConfig(HistoryConfig{Enabled: true, MaxSizeMB: 64}); err != nil {
		t.Fatal(err)
	}
	store.appendAndLog(context.Background(), manager, logger)
	samples, _, err = store.Aggregated(context.Background(), historyMaxRangeHours, 2000, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 {
		t.Fatalf("re-enabled history should append again: %+v", samples)
	}
}

// 同名芯片（多 NVMe/多 mlx5）在 extraTemperatures 里带 #N 后缀，
// 分组识别必须剥掉后缀再查驱动表（mlx5 归「网卡」组）。
func TestSampleFromStatusGroupsNumberedDuplicateChips(t *testing.T) {
	status := &Status{ExtraTemperatures: []Temperature{
		{Label: "i915:temp1", Celsius: 42},
		{Label: "i915#2:temp1", Celsius: 41},
		{Label: "mlx5:temp1", Celsius: 64},
		{Label: "mlx5#2:temp1", Celsius: 63},
	}}
	sample := SampleFromStatus(status, time.Unix(1000, 0))
	groups := map[string]string{}
	for _, sensor := range sample.Sensors {
		groups[sensor.Key] = sensor.Group
	}
	want := map[string]string{
		"i915:temp1": "gpu", "i915#2:temp1": "gpu",
		"mlx5:temp1": "nic", "mlx5#2:temp1": "nic",
	}
	if !reflect.DeepEqual(groups, want) {
		t.Fatalf("groups=%v, want %v", groups, want)
	}
}

// ApplySensorGroupOverrides 在查询结果上整体改写父类归属：
// 数据库保持默认分组，改回覆盖配置即恢复。
func TestApplySensorGroupOverrides(t *testing.T) {
	samples := []HistorySample{{
		Sensors: []HistorySensorSample{
			{Group: "other", Key: "mlx5:temp1", C: 64},
			{Group: "cpu", Key: "Core 0", C: 45},
			{Group: "gpu", Key: "i915:temp1", C: 42},
		},
	}}
	ApplySensorGroupOverrides(samples, map[string]string{"mlx5:temp1": "nic", "i915:temp1": "other", "Core 0": "nic"})
	want := []HistorySensorSample{
		{Group: "nic", Key: "mlx5:temp1", C: 64},
		{Group: "nic", Key: "Core 0", C: 45},
		{Group: "other", Key: "i915:temp1", C: 42},
	}
	if !reflect.DeepEqual(samples[0].Sensors, want) {
		t.Fatalf("sensors=%+v, want %+v", samples[0].Sensors, want)
	}
	ApplySensorGroupOverrides(samples, nil)
	if samples[0].Sensors[0].Group != "nic" {
		t.Fatalf("nil overrides must not touch samples: %+v", samples[0].Sensors)
	}
}

// history_sensors 主键是 (ts, grp, key)：即使上游仍产出重复键
// （旧数据回放、同名芯片），Append 也不得让整个采样点回滚丢失。
func TestAppendToleratesDuplicateSensorKeys(t *testing.T) {
	store, err := NewHistoryStore(filepath.Join(t.TempDir(), "history.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	sample := HistorySample{
		TS: time.Now().Unix(), CPUC: 50,
		Sensors: []HistorySensorSample{
			{Group: "other", Key: "nvme:temp1", C: 39.85},
			{Group: "other", Key: "nvme:temp1", C: 43.85},
		},
	}
	if err := store.Append(sample); err != nil {
		t.Fatalf("append duplicate sensor keys: %v", err)
	}
	samples, _, err := store.Aggregated(context.Background(), 1, 480, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 1 || samples[0].CPUC != 50 {
		t.Fatalf("sample lost after duplicate-key append: %+v", samples)
	}
}
