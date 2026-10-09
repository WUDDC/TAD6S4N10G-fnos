package powerguard

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- 提取与键名 ----

func TestExtractSerialTemperature(t *testing.T) {
	tests := []struct {
		name  string
		line  string
		want  float64
		valid bool
	}{
		{"bare", "25.6", 25.6, true},
		{"prefixed", "temp:25.6", 25.6, true},
		{"explicit plus", "+25.6", 25.6, true},
		{"negative", "-3.0", -3.0, true},
		{"json", `{"temp":25.63,"hum":50.1}`, 25.63, true},
		{"integer", "T=27", 27, true},
		// 行首地址/序号不在量程或为 0 时跳过，取真正的温度
		{"zero address then temp", "0 25.6", 25.6, true},
		// 已知局限：行首序号本身落在量程内会被当作温度
		{"address then temp", "1 25.6", 1, true},
		{"humidity first", "50,25.6", 50, true},
		// 区间外与哨兵一律拒绝：raw 值、DS18B20 断连 -127、恰好 0.0
		{"zero sentinel", "0.0", 0, false},
		{"disconnected sentinel", "-127.0", 0, false},
		{"below ds18b20 range", "-60.5", 0, false},
		{"raw centi", "1256", 0, false},
		{"over range", "temp:999", 0, false},
		{"no number", "OK", 0, false},
		{"empty", "", 0, false},
	}
	for _, test := range tests {
		got, valid := extractSerialTemperature(test.line)
		if valid != test.valid || (valid && got != test.want) {
			t.Fatalf("%s: extractSerialTemperature(%q)=(%v,%v), want (%v,%v)",
				test.name, test.line, got, valid, test.want, test.valid)
		}
	}
}

func TestSerialSensorKey(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/dev/ttyUSB0", "usb:tty:ttyUSB0"},
		{"/dev/serial/by-id/usb-1a86_USB_Serial-if00-port0", "usb:tty:usb-1a86_USB_Serial-if00-port0"},
		// 超长名称截断到 32 字符，避免传感器键过长（与 usbTempLabel 一致）
		{"/dev/serial/by-id/usb-very-long-name-0123456789abcdef0123456789", "usb:tty:usb-very-long-name-0123456789abc"},
	}
	for _, test := range tests {
		if got := serialSensorKey(test.path); got != test.want {
			t.Fatalf("serialSensorKey(%q)=%q, want %q", test.path, got, test.want)
		}
	}
}

// ---- 配置校验 ----

func TestNormalizeSerialSensorConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     SerialSensorConfig
		want    SerialSensorConfig
		wantErr string
	}{
		{
			name: "enabled defaults to 9600",
			cfg:  SerialSensorConfig{Enabled: true, Path: "/dev/ttyUSB0"},
			want: SerialSensorConfig{Enabled: true, Path: "/dev/ttyUSB0", Baud: 9600},
		},
		{
			name:    "enabled without path",
			cfg:     SerialSensorConfig{Enabled: true},
			want:    SerialSensorConfig{Enabled: true},
			wantErr: "设备路径",
		},
		{
			name:    "enabled with non-dev path",
			cfg:     SerialSensorConfig{Enabled: true, Path: "ttyUSB0", Baud: 9600},
			want:    SerialSensorConfig{Enabled: true, Path: "ttyUSB0", Baud: 9600},
			wantErr: "/dev/",
		},
		{
			name:    "enabled with bad baud",
			cfg:     SerialSensorConfig{Enabled: true, Path: "/dev/ttyUSB0", Baud: 1200},
			want:    SerialSensorConfig{Enabled: true, Path: "/dev/ttyUSB0", Baud: 1200},
			wantErr: "波特率",
		},
		{
			name: "disabled keeps values for re-enable",
			cfg:  SerialSensorConfig{Path: "/dev/ttyUSB0", Baud: 115200},
			want: SerialSensorConfig{Path: "/dev/ttyUSB0", Baud: 115200},
		},
		{
			name: "path trimmed",
			cfg:  SerialSensorConfig{Enabled: true, Path: "  /dev/ttyUSB0  ", Baud: 115200},
			want: SerialSensorConfig{Enabled: true, Path: "/dev/ttyUSB0", Baud: 115200},
		},
	}
	for _, test := range tests {
		got, err := NormalizeSerialSensorConfig(test.cfg)
		if test.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("%s: err=%v, want contains %q", test.name, err, test.wantErr)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Fatalf("%s: NormalizeSerialSensorConfig()=(%+v,%v), want (%+v,nil)", test.name, got, err, test.want)
		}
	}
}

// ---- 设备枚举 ----

func TestSerialDeviceCandidates(t *testing.T) {
	root := t.TempDir()
	mkdir := func(rel string) {
		if err := os.MkdirAll(filepath.Join(root, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mkdir("sys/class/tty/ttyUSB0/device") // 真实 USB 串口
	mkdir("sys/class/tty/ttyACM1/device") // CDC-ACM
	mkdir("sys/class/tty/ttyS0")          // 板载串口：无 device 子目录，应排除
	mkdir("dev/serial/by-id/usb-1a86_USB_Serial-if00-port0")

	m := &Manager{Root: root}
	devices := m.serialDeviceCandidates()
	paths := make([]string, 0, len(devices))
	labels := make([]string, 0, len(devices))
	for _, device := range devices {
		paths = append(paths, device.Path)
		labels = append(labels, device.Label)
	}
	wantPaths := []string{
		filepath.Join(root, "/dev/serial/by-id/usb-1a86_USB_Serial-if00-port0"),
		filepath.Join(root, "/dev/ttyACM1"),
		filepath.Join(root, "/dev/ttyUSB0"),
	}
	wantLabels := []string{"1a86_USB_Serial", "ttyACM1", "ttyUSB0"}
	if strings.Join(paths, "|") != strings.Join(wantPaths, "|") ||
		strings.Join(labels, "|") != strings.Join(wantLabels, "|") {
		t.Fatalf("serialDeviceCandidates()=(%v,%v), want (%v,%v)", paths, labels, wantPaths, wantLabels)
	}
}

// ---- 读取循环 ----

// fakeSerialPort 假串口：按序返回预置数据块，耗尽后转 readErr（或阻塞模拟
// 静默连接）。
type fakeSerialPort struct {
	reads    [][]byte
	readAt   int
	readErr  error
	blocks   chan struct{}
	deadline time.Time
	closed   bool
}

func (f *fakeSerialPort) Read(b []byte) (int, error) {
	if f.readAt < len(f.reads) {
		n := copy(b, f.reads[f.readAt])
		f.readAt++
		return n, nil
	}
	if f.readErr != nil {
		return 0, f.readErr
	}
	if f.blocks != nil {
		// 模拟真实驱动：阻塞中的 Read 也要遵守 deadline 到期返回，
		// 否则循环无法在读间隙观察到 kick/ctx（真实 usb-serial 由 poll 保证）
		if !f.deadline.IsZero() {
			select {
			case <-f.blocks:
				return 0, errors.New("fake port force-closed")
			case <-time.After(time.Until(f.deadline)):
				return 0, os.ErrDeadlineExceeded
			}
		}
		<-f.blocks
		return 0, errors.New("fake port force-closed")
	}
	// VMIN=0/VTIME 到期的安静返回（无数据、无错误）
	return 0, nil
}

func (f *fakeSerialPort) SetReadDeadline(t time.Time) error { f.deadline = t; return nil }

func (f *fakeSerialPort) Close() error {
	f.closed = true
	if f.blocks != nil {
		close(f.blocks)
	}
	return nil
}

func serialTestConfig() SerialSensorConfig {
	return SerialSensorConfig{Enabled: true, Path: "/dev/ttyUSB0", Baud: 9600}
}

func TestReadSerialLinesCollectsLines(t *testing.T) {
	m := &Manager{serialKick: make(chan struct{}, 1)}
	port := &fakeSerialPort{
		// 一行拆多个包到达 + 多种行格式；末尾不留换行的残行不产生读数
		reads:   [][]byte{[]byte("25."), []byte("6\r\ntemp:26.5\n"), []byte(`{"temp":27.25}` + "\n"), []byte("no newline tail")},
		readErr: errors.New("port gone"),
	}
	err := m.readSerialLines(context.Background(), port, serialTestConfig())
	if err == nil || !strings.Contains(err.Error(), "read: port gone") {
		t.Fatalf("readSerialLines() err=%v, want read error", err)
	}
	if m.serialLatest.Celsius != 27.25 || m.serialLatest.Key != "usb:tty:ttyUSB0" {
		t.Fatalf("serialLatest=(%q,%v), want (usb:tty:ttyUSB0,27.25)", m.serialLatest.Key, m.serialLatest.Celsius)
	}
}

func TestReadSerialLinesStale(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	serialNow = func() time.Time { current = current.Add(5 * time.Second); return current }
	t.Cleanup(func() { serialNow = time.Now })
	m := &Manager{serialKick: make(chan struct{}, 1)}
	port := &fakeSerialPort{readErr: os.ErrDeadlineExceeded}
	err := m.readSerialLines(context.Background(), port, serialTestConfig())
	if !errors.Is(err, errNoSerialData) {
		t.Fatalf("readSerialLines() err=%v, want errNoSerialData", err)
	}
	if !m.serialLatest.At.IsZero() {
		t.Fatalf("stale loop must not store readings, got %v", m.serialLatest.Celsius)
	}
}

func TestReadSerialLinesKickCloses(t *testing.T) {
	// 直接调用 readSerialLines 时 serialKick 不会由 SerialSensorLoop 初始化
	m := &Manager{serialKick: make(chan struct{}, 1)}
	port := &fakeSerialPort{reads: [][]byte{[]byte("23.4\n")}, blocks: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- m.readSerialLines(ctx, port, serialTestConfig()) }()
	waitForSerialCondition(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !m.serialLatest.At.IsZero()
	})
	m.kickSerialReader()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("kick must end loop without error, got %v", err)
		}
	case <-time.After(6 * time.Second): // 阻塞中的 Read 最迟 serialReadTimeout 后返回
		t.Fatal("kick did not end readSerialLines")
	}
	// Close 由连接层负责（connectSerialSensor 的 defer），readSerialLines 不关
}

// ---- 保存配置 ----

func TestSaveSerialSensorConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(configPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &Manager{ConfigPath: configPath}
	m.serialKick = make(chan struct{}, 1)

	// 非法配置直接拒绝且不落盘
	if err := m.SaveSerialSensorConfig(SerialSensorConfig{Enabled: true}); err == nil {
		t.Fatal("enabled without path must fail")
	}
	// 合法保存：波特率归位默认值，kick 触发
	if err := m.SaveSerialSensorConfig(SerialSensorConfig{Enabled: true, Path: "/dev/ttyUSB0"}); err != nil {
		t.Fatalf("SaveSerialSensorConfig()=%v", err)
	}
	cfg, err := m.loadConfigLocked()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Serial.Enabled || cfg.Serial.Path != "/dev/ttyUSB0" || cfg.Serial.Baud != serialDefaultBaud {
		t.Fatalf("saved serial config=%+v", cfg.Serial)
	}
	select {
	case <-m.serialKick:
	default:
		t.Fatal("save must kick the reader")
	}
}

// ---- 完整循环 ----

func TestSerialSensorLoopServesAndDisables(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.json")
	seed := `{"serial":{"enabled":true,"path":"/dev/ttyUSB0","baud":9600}}`
	if err := os.WriteFile(configPath, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	m := &Manager{ConfigPath: configPath}
	port := &fakeSerialPort{reads: [][]byte{[]byte("23.4\n")}, blocks: make(chan struct{})}
	previousOpener := openSerialPort
	openSerialPort = func(path string, baud int) (serialPort, error) {
		if path != "/dev/ttyUSB0" || baud != 9600 {
			t.Errorf("openSerialPort(%q,%d), want (/dev/ttyUSB0,9600)", path, baud)
		}
		return port, nil
	}
	t.Cleanup(func() { openSerialPort = previousOpener })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		m.SerialSensorLoop(ctx, nil)
		close(done)
	}()
	waitForSerialCondition(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.serialOpen && m.serialLatest.Celsius == 23.4
	})

	// 防死锁回归：Status() 持 m.mu 调 extraTemperatures → 串口读数检查。
	// 若读数检查内部再取 m.mu（不可重入），这里会永久挂起。
	m.mu.Lock()
	extra := m.extraTemperatures()
	m.mu.Unlock()
	if !containsTemperature(extra, "usb:tty:ttyUSB0", 23.4) {
		t.Fatalf("extraTemperatures()=%+v, want usb:tty:ttyUSB0=23.4", extra)
	}

	// extraTemperatures 能拿到新鲜读数；模拟数据过期后不再产出。
	// serialTemperatureReadingLocked 须持 m.mu 调用（与 extraTemperatures
	// 的调用条件一致），这里同时验证读数新鲜/过期两态。
	func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if reading, ok := m.serialTemperatureReadingLocked(); !ok || reading.Celsius != 23.4 {
			t.Fatalf("serialTemperatureReadingLocked()=(%+v,%v), want 23.4", reading, ok)
		}
		m.serialLatest.At = time.Now().Add(-2 * serialStaleAfter)
		if _, ok := m.serialTemperatureReadingLocked(); ok {
			t.Fatal("stale reading must not be served")
		}
	}()

	// 保存停用：落盘 + kick → 循环退出连接进入空转
	if err := m.SaveSerialSensorConfig(SerialSensorConfig{}); err != nil {
		t.Fatalf("SaveSerialSensorConfig()=%v", err)
	}
	waitForSerialCondition(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return !m.serialOpen
	})

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit after cancel")
	}
}

func waitForSerialCondition(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(6 * time.Second) // 覆盖阻塞 Read 的 deadline 周期
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

func containsTemperature(list []Temperature, label string, celsius float64) bool {
	for _, item := range list {
		if item.Label == label && item.Celsius == celsius {
			return true
		}
	}
	return false
}
