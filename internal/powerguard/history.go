package powerguard

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	// 纯 Go 的 SQLite 驱动：无 CGO，交叉编译不受影响；WAL 模式保证断电后
	// 数据文件要么是旧状态要么是新状态。
	_ "modernc.org/sqlite"
)

// 历史采样参数：60 秒一个点、实际存储 32 天（约 4.6 万行），单文件 history.db。
// 满配设备每分钟约 0.7KB（主表 45B + 风扇 165B + 盘位 180B + 传感器 315B），
// 32 天约 32–48MB，默认大小上限 64MB 是两倍余量；超限后按天删最旧数据。
// 存储比展示窗口（historyMaxRangeHours = 30 天）多 2 天缓冲：日期/大小清理
// 以天粒度从最旧侧删除时，用户可见的 30 天窗口前缘永远不会缺数据。
const (
	historyInterval        = time.Minute
	historyRetentionDays   = 32 // 实际保留天数；展示窗口另由 historyMaxRangeHours 钳制在 30 天
	historyPruneEvery      = time.Hour
	historyMaxPoints       = 480
	historyMaxRangeHours   = 30 * 24 // 用户可查询的最大范围：30 天（< 存储的 32 天）
	historyFileVersion     = 1       // /api/history 响应结构版本
	historyMinMaxSizeMB    = 8
	historyMaxMaxSizeMB    = 1024
	historySizePruneBytes  = 24 * 60 * 60 // 大小超限时每次删除的最旧数据跨度（秒）
	historySizePruneRounds = 64           // 单次清理最多删除的天数，防止死循环
)

type HistoryConfig struct {
	Enabled   bool  `json:"enabled"`
	MaxSizeMB int64 `json:"max_size_mb"`
}

func DefaultHistoryConfig() HistoryConfig {
	return HistoryConfig{Enabled: true, MaxSizeMB: 64}
}

// ClampHistoryMaxSize 把大小上限限制在合理区间，配置文件里的非法值静默归位。
func ClampHistoryMaxSize(maxSizeMB int64) int64 {
	if maxSizeMB < historyMinMaxSizeMB {
		return historyMinMaxSizeMB
	}
	if maxSizeMB > historyMaxMaxSizeMB {
		return historyMaxMaxSizeMB
	}
	return maxSizeMB
}

type HistoryFanSample struct {
	ID         string `json:"id"`
	RPM        int64  `json:"rpm"`
	PWMPercent int    `json:"pwm_percent"`
}

type HistoryDiskSample struct {
	ID           string  `json:"id"`
	TemperatureC float64 `json:"c"`
}

type HistorySensorSample struct {
	Group string  `json:"group"` // cpu | gpu | nic | other
	Key   string  `json:"key"`
	C     float64 `json:"c"`
}

type HistorySample struct {
	TS      int64                 `json:"ts"` // unix 秒
	CPUC    float64               `json:"cpu_c,omitempty"`
	HDDC    float64               `json:"hdd_c,omitempty"`
	NVMeC   float64               `json:"nvme_c,omitempty"`
	Fans    []HistoryFanSample    `json:"fans,omitempty"`
	Disks   []HistoryDiskSample   `json:"disks,omitempty"`   // 单盘温度（仅在线且有读数的槽位）
	Sensors []HistorySensorSample `json:"sensors,omitempty"` // CPU 逐传感器 / 网卡 / 其它
}

type historyFile struct {
	Version         int             `json:"version"`
	IntervalSeconds int             `json:"interval_seconds"`
	Samples         []HistorySample `json:"samples"`
}

// HistoryStore 把采样写入 SQLite（WAL 模式），读写并发安全。
// 查询按需走索引取范围切片，不用常驻整段历史在内存里。
type HistoryStore struct {
	mu        sync.Mutex
	db        *sql.DB
	path      string
	lastPrune time.Time
}

func NewHistoryStore(path string) (*HistoryStore, error) {
	if path == "" {
		return nil, errors.New("history path is required")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open history db: %w", err)
	}
	// WAL：写只追加日志页，读不阻塞写；synchronous=NORMAL 在 WAL 下够安全。
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA synchronous=NORMAL",
		"PRAGMA busy_timeout=3000",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("history pragma: %w", err)
		}
	}
	schema := `
CREATE TABLE IF NOT EXISTS history (
	ts     INTEGER PRIMARY KEY,
	cpu_c  REAL NOT NULL DEFAULT 0,
	hdd_c  REAL NOT NULL DEFAULT 0,
	nvme_c REAL NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS history_fans (
	ts          INTEGER NOT NULL REFERENCES history(ts) ON DELETE CASCADE ON UPDATE CASCADE,
	fan_id      TEXT NOT NULL,
	rpm         INTEGER NOT NULL DEFAULT 0,
	pwm_percent INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (ts, fan_id)
);
CREATE TABLE IF NOT EXISTS history_slots (
	ts           INTEGER NOT NULL REFERENCES history(ts) ON DELETE CASCADE ON UPDATE CASCADE,
	slot_id      TEXT NOT NULL,
	temperature_c REAL NOT NULL DEFAULT 0,
	PRIMARY KEY (ts, slot_id)
);
CREATE TABLE IF NOT EXISTS history_sensors (
	ts      INTEGER NOT NULL REFERENCES history(ts) ON DELETE CASCADE ON UPDATE CASCADE,
	grp     TEXT NOT NULL,
	key     TEXT NOT NULL,
	c       REAL NOT NULL DEFAULT 0,
	PRIMARY KEY (ts, grp, key)
);`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("history schema: %w", err)
	}
	store := &HistoryStore{db: db, path: path, lastPrune: time.Time{}}
	if err := store.Prune(time.Now()); err != nil {
		db.Close()
		return nil, fmt.Errorf("history prune: %w", err)
	}
	return store, nil
}

func (s *HistoryStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// ExportSQLite 把数据库的一致性快照写入 w：VACUUM INTO 临时文件再读回，
// 避开 WAL 未合并导致直接复制丢数据的问题。
func (s *HistoryStore) ExportSQLite(ctx context.Context, w io.Writer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tmp, err := os.CreateTemp("", "tad-module-history-*.db")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	tmp.Close()
	os.Remove(tmpName) // VACUUM INTO 要求目标文件不存在
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, tmpName); err != nil {
		return fmt.Errorf("vacuum into: %w", err)
	}
	defer os.Remove(tmpName)
	file, err := os.Open(tmpName)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(w, file)
	return err
}

// WriteCSV 把全部历史导出为宽表 CSV：时间与聚合温度为固定列，风扇/单盘/
// 传感器按出现的 ID 动态成列（缺失留空）。首行带 BOM，方便 Excel 识别 UTF-8。
func (s *HistoryStore) WriteCSV(ctx context.Context, w io.Writer) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	samples, err := s.queryRange(ctx, 0)
	if err != nil {
		return err
	}

	fanCols := map[string]bool{}
	diskCols := map[string]bool{}
	sensorCols := map[string]bool{}
	for _, sample := range samples {
		for _, fan := range sample.Fans {
			fanCols[fan.ID] = true
		}
		for _, disk := range sample.Disks {
			diskCols[disk.ID] = true
		}
		for _, sensor := range sample.Sensors {
			sensorCols[sensor.Group+"\x00"+sensor.Key] = true
		}
	}
	sortedKeys := func(set map[string]bool) []string {
		keys := make([]string, 0, len(set))
		for key := range set {
			keys = append(keys, key)
		}
		for i := 1; i < len(keys); i++ {
			for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
				keys[j], keys[j-1] = keys[j-1], keys[j]
			}
		}
		return keys
	}
	fans := sortedKeys(fanCols)
	disks := sortedKeys(diskCols)
	sensors := sortedKeys(sensorCols)

	if _, err := w.Write([]byte("\xef\xbb\xbf")); err != nil {
		return err
	}
	csvWriter := csv.NewWriter(w)
	header := []string{"ts", "time", "cpu_c", "hdd_c", "nvme_c"}
	for _, id := range fans {
		header = append(header, "fan_"+id+"_rpm", "fan_"+id+"_pwm")
	}
	for _, id := range disks {
		header = append(header, "disk_"+id+"_c")
	}
	for _, key := range sensors {
		parts := strings.SplitN(key, "\x00", 2)
		header = append(header, "sensor_"+parts[0]+"_"+parts[1]+"_c")
	}
	if err := csvWriter.Write(header); err != nil {
		return err
	}
	formatC := func(value float64) string {
		if value <= 0 {
			return ""
		}
		return strconv.FormatFloat(value, 'f', 1, 64)
	}
	for _, sample := range samples {
		record := []string{
			strconv.FormatInt(sample.TS, 10),
			time.Unix(sample.TS, 0).Format("2006-01-02 15:04:05"),
			formatC(sample.CPUC), formatC(sample.HDDC), formatC(sample.NVMeC),
		}
		for _, id := range fans {
			rpm, pwm := "", ""
			for _, fan := range sample.Fans {
				if fan.ID == id {
					rpm = strconv.FormatInt(fan.RPM, 10)
					pwm = strconv.Itoa(fan.PWMPercent)
				}
			}
			record = append(record, rpm, pwm)
		}
		for _, id := range disks {
			value := ""
			for _, disk := range sample.Disks {
				if disk.ID == id {
					value = formatC(disk.TemperatureC)
				}
			}
			record = append(record, value)
		}
		for _, key := range sensors {
			value := ""
			for _, sensor := range sample.Sensors {
				if sensor.Group+"\x00"+sensor.Key == key {
					value = formatC(sensor.C)
				}
			}
			record = append(record, value)
		}
		if err := csvWriter.Write(record); err != nil {
			return err
		}
	}
	csvWriter.Flush()
	return csvWriter.Error()
}

// Append 追加一个采样点（ts 为主键，重复写入即覆盖）。
func (s *HistoryStore) Append(sample HistorySample) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO history (ts, cpu_c, hdd_c, nvme_c) VALUES (?, ?, ?, ?)
ON CONFLICT(ts) DO UPDATE SET cpu_c=excluded.cpu_c, hdd_c=excluded.hdd_c, nvme_c=excluded.nvme_c`,
		sample.TS, sample.CPUC, sample.HDDC, sample.NVMeC); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM history_fans WHERE ts = ?`, sample.TS); err != nil {
		return err
	}
	for _, fan := range sample.Fans {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO history_fans (ts, fan_id, rpm, pwm_percent) VALUES (?, ?, ?, ?)`,
			sample.TS, fan.ID, fan.RPM, fan.PWMPercent); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM history_slots WHERE ts = ?`, sample.TS); err != nil {
		return err
	}
	for _, disk := range sample.Disks {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO history_slots (ts, slot_id, temperature_c) VALUES (?, ?, ?)`,
			sample.TS, disk.ID, disk.TemperatureC); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM history_sensors WHERE ts = ?`, sample.TS); err != nil {
		return err
	}
	for _, sensor := range sample.Sensors {
		if _, err := tx.Exec(`INSERT OR REPLACE INTO history_sensors (ts, grp, key, c) VALUES (?, ?, ?, ?)`,
			sample.TS, sensor.Group, sensor.Key, sensor.C); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return nil
}

// PruneIfNeeded 挂在采样循环上的低频清理：日期轮转距上次超过 1 小时才真正
// 执行 DELETE；大小上限每次都检查（仅两次 stat），超限才进入删除与 VACUUM。
// 历史记录停用后清理照跑，让旧数据按期收敛。
func (s *HistoryStore) PruneIfNeeded(now time.Time, maxSizeMB int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastPrune.IsZero() || time.Since(s.lastPrune) >= historyPruneEvery {
		if err := s.pruneLocked(now); err != nil {
			return err
		}
	}
	if maxSizeMB <= 0 {
		return nil
	}
	return s.enforceSizeLocked(maxSizeMB << 20)
}

// Prune 删除保留期之外的采样（history_fans 级联删除）。
func (s *HistoryStore) Prune(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pruneLocked(now)
}

func (s *HistoryStore) pruneLocked(now time.Time) error {
	cutoff := now.Add(-historyRetentionDays * 24 * time.Hour).Unix()
	if _, err := s.db.Exec(`DELETE FROM history WHERE ts < ?`, cutoff); err != nil {
		return err
	}
	s.lastPrune = now
	return nil
}

// dbSizeBytes 统计主库与 WAL 文件大小；文件不存在按 0 处理。
func (s *HistoryStore) dbSizeBytes() int64 {
	total := int64(0)
	for _, name := range []string{s.path, s.path + "-wal"} {
		if info, err := os.Stat(name); err == nil {
			total += info.Size()
		}
	}
	return total
}

// enforceSizeLocked 数据库（含 WAL）超过上限时，从最旧的一天开始逐段删除，
// 至少保留最新一个采样点。删除发生在采样间隙且只在超限时发生，VACUUM 的
// I/O 开销可以接受。
func (s *HistoryStore) enforceSizeLocked(limitBytes int64) error {
	deleted := false
	for round := 0; round < historySizePruneRounds; round++ {
		if s.dbSizeBytes() <= limitBytes {
			break
		}
		var oldest, newest sql.NullInt64
		if err := s.db.QueryRow(`SELECT MIN(ts), MAX(ts) FROM history`).Scan(&oldest, &newest); err != nil {
			return err
		}
		if !oldest.Valid || !newest.Valid || newest.Int64-oldest.Int64 <= historySizePruneBytes {
			break // 不足一天可删时停止，至少保留最新一天的采样
		}
		cutoff := oldest.Int64 + historySizePruneBytes
		if cutoff > newest.Int64 {
			cutoff = newest.Int64
		}
		if _, err := s.db.Exec(`DELETE FROM history WHERE ts < ?`, cutoff); err != nil {
			return err
		}
		deleted = true
	}
	if !deleted {
		return nil
	}
	if _, err := s.db.Exec(`VACUUM`); err != nil {
		return err
	}
	// VACUUM 只压缩主库；WAL 文件停在高位水位，必须显式截断才真正归还磁盘
	_, err := s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	return err
}

// Aggregated 返回最近 rangeHours 小时的采样（支持 0.5 这样的半小时范围）；
// 行数超过 maxPoints 时按桶聚合（数值字段取峰值），第二个返回值是聚合后的
// 采样间隔秒数，前端据此识别停机断口。
func (s *HistoryStore) Aggregated(ctx context.Context, rangeHours, maxPoints float64, now time.Time) ([]HistorySample, int, error) {
	if rangeHours < 0.5 {
		rangeHours = 0.5
	}
	if rangeHours > historyMaxRangeHours {
		rangeHours = historyMaxRangeHours
	}
	if maxPoints < 10 {
		maxPoints = 10
	}
	if maxPoints > 2000 {
		maxPoints = 2000
	}
	cutoff := now.Add(-time.Duration(rangeHours * float64(time.Hour))).Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	samples, err := s.queryRange(ctx, cutoff)
	if err != nil {
		return nil, 0, err
	}
	if len(samples) == 0 {
		return []HistorySample{}, int(historyInterval.Seconds()), nil
	}
	stride := int(math.Ceil(float64(len(samples)) / maxPoints))
	if stride <= 1 {
		return samples, int(historyInterval.Seconds()), nil
	}
	out := make([]HistorySample, 0, len(samples)/stride+1)
	for i := 0; i < len(samples); i += stride {
		out = append(out, aggregateBucket(samples[i:min(i+stride, len(samples))]))
	}
	return out, int(historyInterval.Seconds()) * stride, nil
}

// queryRange 读取 cutoff 之后的采样：主表与三张子表分别查询再按 ts 归并。
// 不做多表 JOIN——JOIN 会产生 风扇数×槽位数×传感器数 的笛卡尔积，30 天
// 数据量下行数爆炸且拖长互斥锁持有时间。
func (s *HistoryStore) queryRange(ctx context.Context, cutoff int64) ([]HistorySample, error) {
	mainRows, err := s.db.QueryContext(ctx, `SELECT ts, cpu_c, hdd_c, nvme_c FROM history WHERE ts >= ? ORDER BY ts`, cutoff)
	if err != nil {
		return nil, err
	}
	defer mainRows.Close()
	samples := make([]HistorySample, 0, 64)
	for mainRows.Next() {
		var sample HistorySample
		if err := mainRows.Scan(&sample.TS, &sample.CPUC, &sample.HDDC, &sample.NVMeC); err != nil {
			return nil, err
		}
		samples = append(samples, sample)
	}
	if err := mainRows.Err(); err != nil {
		return nil, err
	}
	// 索引在主表行收齐后建：append 扩容会使指向切片元素的指针失效
	index := make(map[int64]*HistorySample, len(samples))
	for i := range samples {
		index[samples[i].TS] = &samples[i]
	}

	fanRows, err := s.db.QueryContext(ctx, `SELECT ts, fan_id, rpm, pwm_percent FROM history_fans WHERE ts >= ? ORDER BY ts`, cutoff)
	if err != nil {
		return nil, err
	}
	defer fanRows.Close()
	for fanRows.Next() {
		var ts int64
		var fan HistoryFanSample
		if err := fanRows.Scan(&ts, &fan.ID, &fan.RPM, &fan.PWMPercent); err != nil {
			return nil, err
		}
		if sample := index[ts]; sample != nil {
			sample.Fans = append(sample.Fans, fan)
		}
	}
	if err := fanRows.Err(); err != nil {
		return nil, err
	}

	slotRows, err := s.db.QueryContext(ctx, `SELECT ts, slot_id, temperature_c FROM history_slots WHERE ts >= ? ORDER BY ts`, cutoff)
	if err != nil {
		return nil, err
	}
	defer slotRows.Close()
	for slotRows.Next() {
		var ts int64
		var disk HistoryDiskSample
		if err := slotRows.Scan(&ts, &disk.ID, &disk.TemperatureC); err != nil {
			return nil, err
		}
		if sample := index[ts]; sample != nil {
			sample.Disks = append(sample.Disks, disk)
		}
	}
	if err := slotRows.Err(); err != nil {
		return nil, err
	}

	sensorRows, err := s.db.QueryContext(ctx, `SELECT ts, grp, key, c FROM history_sensors WHERE ts >= ? ORDER BY ts`, cutoff)
	if err != nil {
		return nil, err
	}
	defer sensorRows.Close()
	for sensorRows.Next() {
		var ts int64
		var sensor HistorySensorSample
		if err := sensorRows.Scan(&ts, &sensor.Group, &sensor.Key, &sensor.C); err != nil {
			return nil, err
		}
		if sample := index[ts]; sample != nil {
			sample.Sensors = append(sample.Sensors, sensor)
		}
	}
	if err := sensorRows.Err(); err != nil {
		return nil, err
	}
	return samples, nil
}

// aggregateBucket 合并一个桶：数值字段取峰值（温度峰值更有意义），
// 风扇与单盘温度逐 ID 取峰值，时间戳取桶内最后一个点。
func aggregateBucket(bucket []HistorySample) HistorySample {
	merged := HistorySample{TS: bucket[len(bucket)-1].TS}
	fanIndex := map[string]int{}
	diskIndex := map[string]int{}
	sensorIndex := map[string]int{}
	for _, sample := range bucket {
		if sample.CPUC > merged.CPUC {
			merged.CPUC = sample.CPUC
		}
		if sample.HDDC > merged.HDDC {
			merged.HDDC = sample.HDDC
		}
		if sample.NVMeC > merged.NVMeC {
			merged.NVMeC = sample.NVMeC
		}
		for _, fan := range sample.Fans {
			idx, ok := fanIndex[fan.ID]
			if !ok {
				idx = len(merged.Fans)
				fanIndex[fan.ID] = idx
				merged.Fans = append(merged.Fans, HistoryFanSample{ID: fan.ID})
			}
			if fan.RPM > merged.Fans[idx].RPM {
				merged.Fans[idx].RPM = fan.RPM
			}
			if fan.PWMPercent > merged.Fans[idx].PWMPercent {
				merged.Fans[idx].PWMPercent = fan.PWMPercent
			}
		}
		for _, disk := range sample.Disks {
			idx, ok := diskIndex[disk.ID]
			if !ok {
				idx = len(merged.Disks)
				diskIndex[disk.ID] = idx
				merged.Disks = append(merged.Disks, HistoryDiskSample{ID: disk.ID})
			}
			if disk.TemperatureC > merged.Disks[idx].TemperatureC {
				merged.Disks[idx].TemperatureC = disk.TemperatureC
			}
		}
		for _, sensor := range sample.Sensors {
			combined := sensor.Group + "\x00" + sensor.Key
			idx, ok := sensorIndex[combined]
			if !ok {
				idx = len(merged.Sensors)
				sensorIndex[combined] = idx
				merged.Sensors = append(merged.Sensors, HistorySensorSample{Group: sensor.Group, Key: sensor.Key})
			}
			if sensor.C > merged.Sensors[idx].C {
				merged.Sensors[idx].C = sensor.C
			}
		}
	}
	return merged
}

// SampleFromStatus 从一次状态快照提取历史采样点：CPU 用展示温度，
// 硬盘/NVMe 各取在线槽位的最高温（0 表示不可用），风扇逐个记录。
func SampleFromStatus(st *Status, now time.Time) HistorySample {
	sample := HistorySample{TS: now.Unix()}
	if st == nil {
		return sample
	}
	if st.CPUTemperature.Available {
		sample.CPUC = st.CPUTemperature.DisplayC
	}
	for i := range st.Storage.Slots {
		slot := &st.Storage.Slots[i]
		if slot.TemperatureC <= 0 {
			continue // 空置、休眠未唤醒或读取失败的槽位不参与
		}
		switch slot.Kind {
		case "m2":
			if slot.TemperatureC > sample.NVMeC {
				sample.NVMeC = slot.TemperatureC
			}
		case "front":
			if slot.TemperatureC > sample.HDDC {
				sample.HDDC = slot.TemperatureC
			}
		default:
			continue
		}
		sample.Disks = append(sample.Disks, HistoryDiskSample{ID: slot.ID, TemperatureC: slot.TemperatureC})
	}
	for _, temp := range st.Temperatures {
		if temp.Celsius > 0 {
			sample.Sensors = append(sample.Sensors, HistorySensorSample{Group: "cpu", Key: temp.Label, C: temp.Celsius})
		}
	}
	for _, temp := range st.ExtraTemperatures {
		chip := temp.Label
		if idx := strings.Index(chip, ":"); idx > 0 {
			chip = chip[:idx]
		}
		if idx := strings.Index(chip, "#"); idx > 0 { // nvme#2 → nvme，后缀只是消歧
			chip = chip[:idx]
		}
		group := "other"
		if knownNICDrivers[chip] {
			group = "nic"
		} else if knownGPUDrivers[chip] {
			group = "gpu"
		}
		if temp.Celsius > 0 {
			sample.Sensors = append(sample.Sensors, HistorySensorSample{Group: group, Key: temp.Label, C: temp.Celsius})
		}
	}
	for i := range st.FanControl.Fans {
		fan := &st.FanControl.Fans[i]
		if fan.RPM == 0 && fan.PWMPercent == 0 {
			continue // 未接风扇的空通道全 0，不采集（前端据此过滤幽灵风扇）
		}
		sample.Fans = append(sample.Fans, HistoryFanSample{ID: fan.ID, RPM: fan.RPM, PWMPercent: fan.PWMPercent})
	}
	return sample
}

// SampleNow 供采样循环调用：基于当前状态生成历史采样点。
func (m *Manager) SampleNow(now time.Time) HistorySample {
	status := m.Status()
	return SampleFromStatus(&status, now)
}

// HistoryLoop 周期采样历史数据；启动先采一个点，让图表尽快有首条数据。
func HistoryLoop(ctx context.Context, manager *Manager, logger *log.Logger, store *HistoryStore) {
	store.appendAndLog(ctx, manager, logger)
	ticker := time.NewTicker(historyInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			store.appendAndLog(ctx, manager, logger)
		}
	}
}

func (s *HistoryStore) appendAndLog(ctx context.Context, manager *Manager, logger *log.Logger) {
	select {
	case <-ctx.Done():
		return
	default:
	}
	now := time.Now()
	settings := manager.HistorySettings()
	// 停用时只停写入：已入库的数据仍可查看，并由下面的清理按保留期与
	// 大小上限继续收敛，数据库不会一直膨胀。
	if settings.Enabled {
		if err := s.Append(manager.SampleNow(now)); err != nil {
			logger.Printf("history append failed: %v", err)
		}
	}
	if err := s.PruneIfNeeded(now, settings.MaxSizeMB); err != nil {
		logger.Printf("history prune failed: %v", err)
	}
}
