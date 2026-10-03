package powerguard

// CSV 导出性能探针：默认跳过（避免拖慢 CI），TAD_CSV_BENCH=1 时运行。
// 目的：量化大数据量（用户可把大小上限设到 1GB）下 WriteCSV 的耗时与内存
// 峰值，为"要不要改成流式导出"提供依据。缩放外推：scan+format 均为线性。

import (
	"context"
	"io"
	"os"
	"runtime"
	"testing"
	"time"
)

func TestWriteCSVPerformanceProbe(t *testing.T) {
	if os.Getenv("TAD_CSV_BENCH") == "" {
		t.Skip("set TAD_CSV_BENCH=1 to run the CSV export performance probe")
	}
	store := newTestStore(t)
	defer store.Close()

	// 满配单点：4 风扇 + 6 盘位 + 12 传感器（≈0.7KB/点，见文件头注释）
	const samples = 150000 // ≈104 天满配 ≈ 100MB 级数据库
	base := time.Now().Unix() - samples*60
	start := time.Now()
	for i := 0; samples > i; i++ {
		sample := HistorySample{TS: base + int64(i)*60, CPUC: 50, HDDC: 40, NVMeC: 45}
		for f := 0; f < 4; f++ {
			sample.Fans = append(sample.Fans, HistoryFanSample{ID: "it8613:fan", RPM: 1200, PWMPercent: 50})
		}
		for d := 0; d < 6; d++ {
			sample.Disks = append(sample.Disks, HistoryDiskSample{ID: "front-1", TemperatureC: 38})
		}
		for s := 0; s < 12; s++ {
			sample.Sensors = append(sample.Sensors, HistorySensorSample{Group: "cpu", Key: "core", C: 52})
		}
		if err := store.Append(sample); err != nil {
			t.Fatal(err)
		}
	}
	buildElapsed := time.Since(start)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	exportStart := time.Now()
	if err := store.WriteCSV(context.Background(), io.Discard); err != nil {
		t.Fatal(err)
	}
	exportElapsed := time.Since(exportStart)
	runtime.ReadMemStats(&after)

	dbSize := store.dbSizeBytes()
	_ = dbSize
	t.Logf("samples=%d rows≈%d build=%s db+wal=%dMB export=%s heapΔ=%.0fMB totalAllocΔ=%.0fMB",
		samples, samples*23, buildElapsed.Round(time.Millisecond), dbSize>>20,
		exportElapsed.Round(time.Millisecond),
		float64(after.HeapAlloc-before.HeapAlloc)/(1<<20),
		float64(after.TotalAlloc-before.TotalAlloc)/(1<<20))
}
