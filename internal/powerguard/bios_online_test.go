package powerguard

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 与 BIOS-FID-Tool 生成的 $FID 布局完全一致的合成镜像：
// $FID + 3 字节 + 8 字节主板 ID + 16 字节 + 4 组"dd\0"版本 + 6 字节日期。
func buildFIDImage(boardID string, version [4]string, year, month, day, hour, minute int) []byte {
	image := make([]byte, 256)
	copy(image[0:], []byte("$FID"))
	image[4], image[5], image[6] = 0x04, 0x78, 0x00
	copy(image[7:15], boardID)
	p := 7 + 8 + 16 // 31
	for g := 0; g < 4; g++ {
		image[p+g*3] = version[g][0]
		image[p+g*3+1] = version[g][1]
		image[p+g*3+2] = 0
	}
	dp := p + 12
	image[dp] = byte(year & 0xff)
	image[dp+1] = byte(year >> 8)
	image[dp+2] = byte(month)
	image[dp+3] = byte(day)
	image[dp+4] = byte(hour)
	image[dp+5] = byte(minute)
	return image
}

func TestParseBIOSFID(t *testing.T) {
	image := buildFIDImage("TADSN410", [4]string{"05", "27", "04", "10"}, 2026, 6, 24, 22, 23)
	fid, ok := parseBIOSFID(image)
	if !ok {
		t.Fatal("应能解析 $FID")
	}
	if fid.BoardID != "TADSN410" {
		t.Fatalf("主板 ID 错误: %q", fid.BoardID)
	}
	if fid.Version != "05.27.04.10" {
		t.Fatalf("版本号错误: %q", fid.Version)
	}
	if fid.BuildText != "2026-06-24 22:23" {
		t.Fatalf("构建日期错误: %q", fid.BuildText)
	}
	if _, ok := parseBIOSFID(make([]byte, 256)); ok {
		t.Fatal("无 $FID 的镜像不应解析成功")
	}
}

const biosIndexFixture = `BIOS说明
2026_06_25
1.修复对 4K 显示器兼容性差问题
2.新增 win 下更新工具
============================================================
BIOS文件解析
压缩包: TAD6S4N10G-N305-2026_06_25.zip
提取时间: 2026-10-23 00:32:39

固件文件: TADSN3V410.bin
主板ID:   TADSN410
版本号:   05.27.04.10
构建日期: 2026-06-24 22:23
文件大小: 16777216 字节
SHA256:   C0570702EDD622159308BEACE384D5796DD422CCD7AC01D4C4F181C61FEBC1C6

[upgrade]
file=TADSN3V410.bin
board_id=TADSN410
version=05.27.04.10
build_date=2026-06-24 22:23
size=16777216
sha256=C0570702EDD622159308BEACE384D5796DD422CCD7AC01D4C4F181C61FEBC1C6

============================================================
BIOS文件解析
压缩包: TAD6S4N10G-N150-2026_06_25.zip
提取时间: 2026-10-23 00:32:48

[upgrade]
file=TADSN15V410.bin
board_id=TADSN410
version=05.27.04.10
build_date=2026-06-24 22:50
size=16777216
sha256=DF0F4ADCF15578B840145AE97575AB8AB234A79A7F783F002F2795B483FDEEA1

============================================================
BIOS文件解析
压缩包: TAD6S4N10G-N100-2026_06_25.zip
提取时间: 2026-10-23 00:32:54

[upgrade]
file=TADSN1V510.bin
board_id=TADSN510
version=05.27.05.10
build_date=2026-06-24 21:55
size=16777216
sha256=A18642714DBE77615522BF5401179F6B8EEF31BA67D54BCC6465C028677A1798
`

func TestParseBIOSIndex(t *testing.T) {
	records, changelogs := parseBIOSIndex(biosIndexFixture)
	if len(records) != 3 {
		t.Fatalf("应解析出 3 条固件记录，实际 %d", len(records))
	}
	n305 := records[0]
	if n305.ZipName != "TAD6S4N10G-N305-2026_06_25.zip" || n305.File != "TADSN3V410.bin" {
		t.Fatalf("N305 记录解析错误: %+v", n305)
	}
	if n305.BuildText != "2026-06-24 22:23" || n305.BuildDate.IsZero() {
		t.Fatalf("构建日期解析错误: %+v", n305)
	}
	if len(changelogs) != 1 {
		t.Fatalf("应解析出 1 条更新日志，实际 %d", len(changelogs))
	}
	if changelogs[0].Date != "2026_06_25" {
		t.Fatalf("更新日志日期错误: %q", changelogs[0].Date)
	}
	if !strings.Contains(changelogs[0].Text, "4K") || !strings.Contains(changelogs[0].Text, "win") {
		t.Fatalf("更新日志正文缺失: %q", changelogs[0].Text)
	}
	if strings.Contains(changelogs[0].Text, "BIOS说明") {
		t.Fatalf("更新日志不应包含标题行: %q", changelogs[0].Text)
	}
	// Windows 工具写出的 zip.txt 带 UTF-8 BOM，解析必须剥掉，否则会混进首条日志标题
	withBOM, bomChangelogs := parseBIOSIndex("\uFEFF" + biosIndexFixture)
	if len(withBOM) != 3 || len(bomChangelogs) != 1 {
		t.Fatalf("带 BOM 的索引解析条数错误: records=%d changelogs=%d", len(withBOM), len(bomChangelogs))
	}
	if strings.ContainsRune(bomChangelogs[0].Text, '\uFEFF') {
		t.Fatalf("BOM 不得混入更新日志: %q", bomChangelogs[0].Text)
	}
}

func TestDetectCPUModel(t *testing.T) {
	cases := map[string]string{
		"model name\t: Intel(R) N100":             "N100",
		"model name\t: Intel(R) Core(TM) i3-N305": "N305",
		"model name\t: Intel(R) N150":             "N150",
		"model name\t: Intel(R) Celeron(R) J4125": "",
	}
	for input, want := range cases {
		if got := detectCPUModel(input); got != want {
			t.Fatalf("detectCPUModel(%q) = %q，期望 %q", input, got, want)
		}
	}
}

func TestBIOSZipModel(t *testing.T) {
	if got := biosZipModel("TAD6S4N10G-N305-2026_06_25.zip"); got != "N305" {
		t.Fatalf("机型段提取错误: %q", got)
	}
	if got := biosZipModel("TAD6S4N10G-N00-2026_06_25.zip"); got != "" {
		t.Fatalf("无机型段应为空: %q", got)
	}
}

// TestCheckBIOSOnline 全链路：索引下载注入 + 当前 flash 注入 + cpuinfo 注入。
func TestCheckBIOSOnline(t *testing.T) {
	root := t.TempDir()
	manager := &Manager{StatePath: root + "/state.json", Root: root}
	writeTestFile(t, root+"/proc/cpuinfo", "model name\t: Intel(R) N100\n")

	// 当前 flash 是 N100 旧版（V4.10，比索引里的 V5.10 旧一天）
	current := buildFIDImage("TADSN410", [4]string{"05", "27", "04", "10"}, 2026, 6, 23, 10, 0)
	biosFIDReadHook = func(ctx context.Context, m *Manager) ([]byte, error) { return current, nil }
	defer func() { biosFIDReadHook = nil }()
	firmwareDownloadHook = func(ctx context.Context, address string) ([]byte, error) {
		if address != biosIndexURL() {
			t.Fatalf("索引地址错误: %s", address)
		}
		return []byte(biosIndexFixture), nil
	}
	defer func() { firmwareDownloadHook = nil }()

	status := manager.CheckBIOSOnline(context.Background())
	if status.Error != "" {
		t.Fatalf("检查不应失败: %s", status.Error)
	}
	if status.Model != "N100" {
		t.Fatalf("机型识别错误: %q", status.Model)
	}
	// 必须选 N100 的包而不是 N305/N150 的（严格同机型）。
	if status.CandidateZip != "TAD6S4N10G-N100-2026_06_25.zip" {
		t.Fatalf("候选包选择错误: %q", status.CandidateZip)
	}
	if !status.UpdateAvailable {
		t.Fatal("索引构建日期更新，应判定可升级")
	}
	if status.CurrentVersion != "05.27.04.10" || status.CandidateVersion != "05.27.05.10" {
		t.Fatalf("版本展示错误: 当前 %s 候选 %s", status.CurrentVersion, status.CandidateVersion)
	}
	// 更新日志只显示日期与压缩包名匹配的段落。
	if !strings.Contains(status.Changelog, "4K") {
		t.Fatalf("更新日志缺失: %q", status.Changelog)
	}
	if !strings.Contains(status.DownloadURL, "TAD6S4N10G-N100-2026_06_25.zip") {
		t.Fatalf("下载地址错误: %s", status.DownloadURL)
	}

	// 当前已是新版（构建日期相同）→ 不可升级。
	newer := buildFIDImage("TADSN510", [4]string{"05", "27", "05", "10"}, 2026, 6, 24, 21, 55)
	biosFIDReadHook = func(ctx context.Context, m *Manager) ([]byte, error) { return newer, nil }
	status = manager.CheckBIOSOnline(context.Background())
	if status.UpdateAvailable {
		t.Fatal("构建日期相同不应提示升级")
	}
	if err := manager.StartBIOSOnlineFlash(); err == nil {
		t.Fatal("无可升级版本时应拒绝启动刷写")
	}
}

// TestCheckBIOSOnlineRejectsCrossModel 机型在索引中不存在时不得回退到其他机型。
func TestCheckBIOSOnlineRejectsCrossModel(t *testing.T) {
	root := t.TempDir()
	manager := &Manager{StatePath: root + "/state.json", Root: root}
	writeTestFile(t, root+"/proc/cpuinfo", "model name\t: Intel(R) Celeron(R) J4125\n")
	biosFIDReadHook = func(ctx context.Context, m *Manager) ([]byte, error) {
		return buildFIDImage("TADSN410", [4]string{"05", "27", "04", "10"}, 2026, 6, 23, 10, 0), nil
	}
	defer func() { biosFIDReadHook = nil }()
	firmwareDownloadHook = func(ctx context.Context, address string) ([]byte, error) {
		return []byte(biosIndexFixture), nil
	}
	defer func() { firmwareDownloadHook = nil }()

	status := manager.CheckBIOSOnline(context.Background())
	if status.Error == "" {
		t.Fatal("无法识别机型时必须报错并停止判定")
	}
	if status.CandidateZip != "" || status.UpdateAvailable {
		t.Fatalf("未识别机型不得给出候选包: %+v", status)
	}
}

func TestExtractBIOSFromZip(t *testing.T) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("TAD6S4N10G-N100-2026_06_25/TADSN1V510.bin")
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("fake-bios-image")
	if _, err := entry.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	image, err := extractBIOSFromZip(buffer.Bytes(), "TADSN1V510.bin")
	if err != nil {
		t.Fatalf("解包失败: %v", err)
	}
	if !bytes.Equal(image, payload) {
		t.Fatalf("解包内容不一致: %q", image)
	}
	if _, err := extractBIOSFromZip(buffer.Bytes(), "not-exist.bin"); err == nil {
		t.Fatal("找不到文件时必须报错")
	}
}

// TestRunBIOSOnlineFlashVerifiesSHA256 下载内容与索引 SHA256 不一致时必须停止。
func TestRunBIOSOnlineFlashVerifiesSHA256(t *testing.T) {
	root := t.TempDir()
	manager := &Manager{StatePath: root + "/state.json", Root: root}
	writeTestFile(t, root+"/proc/cpuinfo", "model name\t: Intel(R) N100\n")

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, _ := writer.Create("pkg/TADSN1V510.bin")
	_, _ = entry.Write([]byte("tampered-image"))
	_ = writer.Close()
	firmwareDownloadHook = func(ctx context.Context, address string) ([]byte, error) {
		return buffer.Bytes(), nil
	}
	defer func() { firmwareDownloadHook = nil }()

	injectFakeInhibitor(manager)
	if err := manager.beginFirmwareJob(firmwareJobKindBIOS); err != nil {
		t.Fatal(err)
	}
	manager.runBIOSOnlineFlash(biosIndexRecord{
		ZipName: "TAD6S4N10G-N100-2026_06_25.zip",
		File:    "TADSN1V510.bin",
		SHA256:  "0000000000000000000000000000000000000000000000000000000000000000",
	})
	status := manager.FirmwareStatus()
	if status.Job.State != firmwareJobFailed {
		t.Fatalf("SHA256 不一致必须失败，实际 %s", status.Job.State)
	}
	if !strings.Contains(status.Job.Error, "SHA256") {
		t.Fatalf("失败原因应说明 SHA256: %q", status.Job.Error)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFirmwareTransportRaisesTLSHandshakeTimeout(t *testing.T) {
	// 真机回归:ky.mi-d.cn 冷路径握手 ~9s,默认 10s 贴线,必须放宽
	if got := firmwareHTTPTransport().TLSHandshakeTimeout; got < 25*time.Second {
		t.Fatalf("TLS 握手超时应 >=25s, got %v", got)
	}
}

func TestFetchBIOSIndexRetriesOnce(t *testing.T) {
	calls := 0
	restore := firmwareDownloadHook
	firmwareDownloadHook = func(ctx context.Context, address string) ([]byte, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("TLS handshake timeout")
		}
		return []byte("zip.txt"), nil
	}
	t.Cleanup(func() { firmwareDownloadHook = restore })

	data, err := fetchBIOSIndex(context.Background())
	if err != nil || string(data) != "zip.txt" {
		t.Fatalf("首次失败应重试成功: %v %q", err, data)
	}
	if calls != 2 {
		t.Fatalf("应恰好调用两次, got %d", calls)
	}
}

func TestFetchBIOSIndexGivesUpAfterRetry(t *testing.T) {
	calls := 0
	restore := firmwareDownloadHook
	firmwareDownloadHook = func(ctx context.Context, address string) ([]byte, error) {
		calls++
		return nil, errors.New("DNS 解析超时")
	}
	t.Cleanup(func() { firmwareDownloadHook = restore })

	_, err := fetchBIOSIndex(context.Background())
	if err == nil || !strings.Contains(err.Error(), "DNS") {
		t.Fatalf("重试后仍失败应返回最后错误: %v", err)
	}
	if calls != 2 {
		t.Fatalf("不应超过两次, got %d", calls)
	}
}

func TestReadBIOSVersionViaDMI(t *testing.T) {
	manager := &Manager{}
	manager.firmwareExec = func(ctx context.Context, dir, name string, args []string) (string, error) {
		switch {
		case name == "dmidecode" && len(args) == 2 && args[0] == "-s":
			return "5.27\n", nil
		case name == "dmidecode":
			return "# dmidecode 3.3\nRelease Date: 07/07/2025\n", nil
		}
		return "", errors.New("unexpected " + name)
	}
	fid, ok := manager.readBIOSVersionViaDMI(context.Background())
	if !ok || fid.Version != "5.27" || fid.BuildText != "2025-07-07" {
		t.Fatalf("DMI 解析不符: %+v ok=%v", fid, ok)
	}
	if fid.BuildDate.Format("2006-01-02") != "2025-07-07" {
		t.Fatalf("构建日期不符: %v", fid.BuildDate)
	}
}

func TestReadCurrentBIOSFIDFallsBackToDMIWhenFlashromFails(t *testing.T) {
	// 真机形态:flashrom 缺依赖跑不起来(LIBPCI_3.8 not found),dmidecode 兜底
	restoreDL := firmwareDownloadHook
	firmwareDownloadHook = func(ctx context.Context, address string) ([]byte, error) {
		return nil, errors.New("自动下载失败")
	}
	t.Cleanup(func() { firmwareDownloadHook = restoreDL })

	manager := &Manager{}
	manager.firmwareExec = func(ctx context.Context, dir, name string, args []string) (string, error) {
		if name == "dmidecode" && len(args) == 2 && args[0] == "-s" {
			return "5.27\n", nil
		}
		if name == "dmidecode" {
			return "Release Date: 07/07/2025\n", nil
		}
		return "", errors.New("unexpected " + name)
	}
	fid, err := manager.readCurrentBIOSFID(context.Background())
	if err != nil {
		t.Fatalf("应回落 DMI 成功, got %v", err)
	}
	if fid.Version != "5.27" || !fid.degraded {
		t.Fatalf("应标记降级版本: %+v", fid)
	}
}

func TestCheckBIOSOnlinePublishesProgressively(t *testing.T) {
	// 用户要求:不要憋到最后一次性显示——候选(最新版本)必须在"当前版本
	// 读取"这一慢段完成之前就发布进缓存
	cpuRoot := t.TempDir()
	procDir := filepath.Join(cpuRoot, "proc")
	if err := os.MkdirAll(procDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cpuinfo := "model name\t: Intel(R) Processor N100\n"
	if err := os.WriteFile(filepath.Join(procDir, "cpuinfo"), []byte(cpuinfo), 0o644); err != nil {
		t.Fatal(err)
	}
	manager := &Manager{Root: cpuRoot}
	dmiRelease := make(chan struct{})
	manager.firmwareExec = func(ctx context.Context, dir, name string, args []string) (string, error) {
		if name == "dmidecode" {
			<-dmiRelease
			if len(args) == 2 && args[0] == "-s" {
				return "5.27\n", nil
			}
			return "Release Date: 07/07/2025\n", nil
		}
		return "", errors.New("unexpected " + name)
	}
	restore := firmwareDownloadHook
	firmwareDownloadHook = func(ctx context.Context, address string) ([]byte, error) {
		if strings.Contains(address, "zip.txt") {
			return []byte(biosIndexFixture), nil
		}
		return nil, errors.New("unexpected download " + address)
	}
	t.Cleanup(func() {
		firmwareDownloadHook = restore
		// 测试主体正常走完会先关一次;未关(异常路径)时兜底关,幂等
		select {
		case <-dmiRelease:
		default:
			close(dmiRelease)
		}
	})

	done := make(chan BIOSOnlineStatus, 1)
	go func() { done <- manager.CheckBIOSOnline(context.Background()) }()

	// 轮询等待候选发布(此刻 dmidecode 仍阻塞)
	deadline := time.Now().Add(3 * time.Second)
	var mid BIOSOnlineStatus
	for time.Now().Before(deadline) {
		mid = manager.BIOSOnline()
		if mid.CandidateVersion != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !mid.Checking {
		t.Fatalf("候选发布时仍应处于 checking 状态: %+v", mid)
	}
	if mid.CandidateVersion == "" {
		t.Fatalf("慢段完成前候选应已发布: %+v", mid)
	}

	close(dmiRelease)
	var final BIOSOnlineStatus
	select {
	case final = <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("检查未在时限内完成")
	}
	if final.Checking {
		t.Fatal("终态不应 checking")
	}
	if final.CurrentVersion != "5.27" {
		t.Fatalf("当前版本应来自 dmidecode 兜底: %+v", final)
	}
	if !final.UpdateAvailable {
		t.Fatalf("候选构建日期晚于 DMI 日期,应判定可更新: %+v", final)
	}
}

func TestBIOSOnlinePublishesCurrentBeforeIndexFetch(t *testing.T) {
	// 真机反馈:页面要等在线索引拉完才见本机信息——当前版本(本地,快)
	// 必须先于索引(CDN,慢)读取并发布
	manager := &Manager{}
	manager.firmwareExec = func(ctx context.Context, dir, name string, args []string) (string, error) {
		if name == "dmidecode" && len(args) == 2 && args[0] == "-s" {
			return "5.27\n", nil
		}
		if name == "dmidecode" {
			return "Release Date: 07/07/2025\n", nil
		}
		return "", errors.New("unexpected " + name)
	}
	restore := firmwareDownloadHook
	firmwareDownloadHook = func(ctx context.Context, address string) ([]byte, error) {
		// flashrom 工具下载(无数据目录时本就到不了)与索引下载共用注入点:
		// 只对索引断言"本机版本已发布"——并行启动有微秒级竞态,本地段(注入的
		// dmidecode 即时返回)必须在索引仍在进行的窗口内可见
		if !strings.Contains(address, "zip.txt") {
			return nil, errors.New("flashrom 下载失败(测试注入)")
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			snapshot := manager.BIOSOnline()
			if snapshot.Checking && snapshot.CurrentVersion == "5.27" {
				break
			}
			if !time.Now().Before(deadline) {
				t.Fatalf("索引拉取进行中本机版本应已发布: %+v", snapshot)
			}
			time.Sleep(10 * time.Millisecond)
		}
		return nil, errors.New("索引下载失败(测试注入)")
	}
	t.Cleanup(func() { firmwareDownloadHook = restore })

	status := manager.CheckBIOSOnline(context.Background())
	if status.CurrentVersion != "5.27" || status.Error == "" {
		t.Fatalf("当前版本应先于索引可用,索引失败单独报错: %+v", status)
	}
	if status.Checking {
		t.Fatal("失败收尾应清除 checking")
	}
}

func TestStartBIOSOnlineCheckRunsInBackground(t *testing.T) {
	// POST 端点立即返回的关键:检查在后台跑,进行中挡并发,完成后可再来
	manager := &Manager{}
	manager.firmwareExec = func(ctx context.Context, dir, name string, args []string) (string, error) {
		if name == "dmidecode" && len(args) == 2 && args[0] == "-s" {
			return "5.27\n", nil
		}
		return "Release Date: 07/07/2025\n", nil
	}
	release := make(chan struct{})
	restore := firmwareDownloadHook
	firmwareDownloadHook = func(ctx context.Context, address string) ([]byte, error) {
		if !strings.Contains(address, "zip.txt") {
			return nil, errors.New("flashrom 下载失败(测试注入)")
		}
		<-release
		return nil, errors.New("索引下载失败(测试注入)")
	}
	t.Cleanup(func() { firmwareDownloadHook = restore })

	if !manager.StartBIOSOnlineCheck() {
		t.Fatal("首次启动应成功")
	}
	if snapshot := manager.BIOSOnline(); !snapshot.Checking {
		t.Fatalf("启动后快照应立即是检查中: %+v", snapshot)
	}
	if manager.StartBIOSOnlineCheck() {
		t.Fatal("进行中应拒绝并发启动")
	}
	close(release)
	deadline := time.After(5 * time.Second)
	for manager.BIOSOnline().Checking {
		select {
		case <-deadline:
			t.Fatal("后台检查未在期限内结束")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if !manager.StartBIOSOnlineCheck() {
		t.Fatal("结束后应允许再次启动")
	}
	// 等第二次收尾,避免测试泄漏 goroutine 写缓存
	deadline = time.After(5 * time.Second)
	for manager.BIOSOnline().Checking {
		select {
		case <-deadline:
			t.Fatal("第二次检查未收尾")
		case <-time.After(10 * time.Millisecond):
		}
	}
}
