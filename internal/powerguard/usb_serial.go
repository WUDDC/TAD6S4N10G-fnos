package powerguard

// USB 转串口外置温度传感器（方案 A：通用文本行源）。用户经 USB 转串口模块
// （CH340/CP210x/FTDI 等）接 DIY 温度节点（Arduino/ESP32 + DS18B20/SHT30 最
// 常见），固件逐行自发输出含温度数字的文本（"25.6"、"temp:25.6"、
// `{"temp":25.6}` 均可）；行内第一个落在 (0,125] 的数字按摄氏度入库。
//
// 与 usb_temper.go 的 HID 温度计分工：那边没有内核驱动、协议一族一条命令，
// 成本在传输层；这里内核白送 /dev/ttyUSB*，成本在协议多样性——所以只做
// "读行取数" 的最大公约数，Modbus/1-wire 等专项协议等真实需求再立项。
//
// 读取模型：SerialSensorLoop 常驻 goroutine 持续读串口（传感器通常每秒一行），
// 最近读数缓存给 extraTemperatures 并入采样链（key 形如 "usb:tty:ttyUSB0"，
// 自动归「其它」组并享受改名链路）。开口即 DTR 复位类设备（Arduino）每次重连
// 需 1-2 秒才出数据，因此选常驻连接而非逐采样开关口。断线/无数据按
// serialRetryWait 重连，保存配置 kick 立即按新配置重连。

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	serialDefaultBaud = 9600             // 波特率缺省值（DIY 固件事实标准）
	serialReadTimeout = 3 * time.Second  // 单次 Read deadline，也是踢断检查周期
	serialStaleAfter  = 30 * time.Second // 超过此时长无新行视为失效，读数不入采样
	serialRetryWait   = 10 * time.Second // 连接失败/失效后的重连间隔
	serialIdleWait    = 15 * time.Second // 未启用时空转间隔
)

// serialBaudRates 配置界面与校验共用的波特率白名单。
var serialBaudRates = []int{2400, 4800, 9600, 19200, 38400, 57600, 115200}

// SerialSensorConfig 串口温度传感器配置，随全局 config.json 落盘。
type SerialSensorConfig struct {
	Enabled bool   `json:"enabled"`
	Path    string `json:"path,omitempty"` // 设备路径；优先 /dev/serial/by-id/*（插拔不漂移）
	Baud    int    `json:"baud,omitempty"` // 0 视为 serialDefaultBaud
}

// NormalizeSerialSensorConfig 校验串口配置：启用时必须给出 /dev/ 下的设备
// 路径，波特率必须在白名单内（0 归位默认值）。停用时保留已填的路径与波特
// 率，重新启用不必重选。
func NormalizeSerialSensorConfig(cfg SerialSensorConfig) (SerialSensorConfig, error) {
	cfg.Path = strings.TrimSpace(cfg.Path)
	if !cfg.Enabled {
		return cfg, nil
	}
	if cfg.Path == "" {
		return cfg, errors.New("启用串口传感器需先选择设备路径")
	}
	if !strings.HasPrefix(cfg.Path, "/dev/") {
		return cfg, fmt.Errorf("设备路径必须是 /dev/ 下的串口节点: %s", cfg.Path)
	}
	if cfg.Baud == 0 {
		cfg.Baud = serialDefaultBaud
	}
	for _, rate := range serialBaudRates {
		if rate == cfg.Baud {
			return cfg, nil
		}
	}
	return cfg, fmt.Errorf("不支持的波特率 %d（可选 %s）", cfg.Baud, serialBaudList())
}

func serialBaudList() string {
	parts := make([]string, 0, len(serialBaudRates))
	for _, rate := range serialBaudRates {
		parts = append(parts, strconv.Itoa(rate))
	}
	return strings.Join(parts, "/")
}

// serialPort 抽象串口的读与关闭，便于离线单测注入假设备。
type serialPort interface {
	Read(b []byte) (int, error)
	SetReadDeadline(t time.Time) error
	Close() error
}

// openSerialPort 打开真实串口并按波特率配置（linux 见 usb_serial_linux.go）；
// 测试替换为假实现。
var openSerialPort = openSerialPortOS

// serialNow 时钟注入点：失效判定（serialStaleAfter）的单测加速。
var serialNow = time.Now

// SerialSensorLoop 常驻串口传感器读取器，随 ctx 取消退出。每轮连接先重读
// 配置：保存新配置 kickSerialReader 踢断当前连接，最迟 serialReadTimeout 后
// 按新配置重连；未启用时低频空转。连接与错误状态记入 Manager 供
// /api/status 下发。
func (m *Manager) SerialSensorLoop(ctx context.Context, logger *log.Logger) {
	if logger == nil {
		logger = log.Default()
	}
	m.mu.Lock()
	if m.serialKick == nil {
		m.serialKick = make(chan struct{}, 1)
	}
	m.mu.Unlock()
	for ctx.Err() == nil {
		cfg, ok := m.serialSensorConfig()
		if !ok {
			m.setSerialOpen(false)
			if !serialSleep(ctx, serialIdleWait) {
				return
			}
			continue
		}
		m.connectSerialSensor(ctx, cfg)
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-m.serialKick: // 配置已保存：立即按新配置重连
		case <-time.After(serialRetryWait):
		}
	}
}

// serialSleep 在 ctx 取消或超时后返回；ctx 已结束返回 false。
func serialSleep(ctx context.Context, wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// serialSensorConfig 读取当前串口配置；未启用或配置不可读时 ok=false。
func (m *Manager) serialSensorConfig() (SerialSensorConfig, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, err := m.loadConfigLocked()
	if err != nil || !cfg.Serial.Enabled || cfg.Serial.Path == "" {
		return SerialSensorConfig{}, false
	}
	return cfg.Serial, true
}

// connectSerialSensor 建立一次连接并读到出错为止：连接失败记错误按重连节奏
// 返回；连接成功后连接状态与读数持续刷新。连接本身不打日志——错误日志
// （变化才记）已覆盖排查需要，重连风暴时不刷屏。
func (m *Manager) connectSerialSensor(ctx context.Context, cfg SerialSensorConfig) {
	port, err := openSerialPort(cfg.Path, cfg.Baud)
	if err != nil {
		m.noteSerialError(fmt.Errorf("%s: %w", cfg.Path, err))
		return
	}
	defer port.Close()
	m.setSerialOpen(true)
	m.noteSerialError(nil)
	defer m.setSerialOpen(false)
	if err := m.readSerialLines(ctx, port, cfg); err != nil && ctx.Err() == nil {
		m.noteSerialError(err)
	}
}

// errNoSerialData 连接成立但持续无数据的失效原因，与 IO 错误区分开。
var errNoSerialData = errors.New("no data")

// readSerialLines 持续读串口直到出错、踢断（保存配置）或 ctx 取消。跨 Read
// 攒行（传感器一行可能分多个包到达），每行交给 extractSerialTemperature，
// 有效读数写 m.serialLatest。
func (m *Manager) readSerialLines(ctx context.Context, port serialPort, cfg SerialSensorConfig) error {
	key := serialSensorKey(cfg.Path)
	var pending []byte
	lastData := serialNow()
	buf := make([]byte, 256)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-m.serialKick:
			return nil
		default:
		}
		if err := port.SetReadDeadline(serialNow().Add(serialReadTimeout)); err != nil {
			// 个别内核不支持 deadline：依赖 VTIME 兜底（usb_serial_linux.go）
			_ = err
		}
		n, err := port.Read(buf)
		if n > 0 {
			lastData = serialNow()
			pending = append(pending, buf[:n]...)
			for {
				idx := bytes.IndexAny(pending, "\r\n")
				if idx < 0 {
					break
				}
				line := string(pending[:idx])
				pending = pending[idx+1:]
				if celsius, ok := extractSerialTemperature(line); ok {
					m.storeSerialReading(key, celsius)
				}
			}
			// 迟迟不见换行的脏数据（波特率不匹配常见乱码）防积压
			if len(pending) > 4096 {
				pending = pending[:0]
			}
		}
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) {
				if serialNow().Sub(lastData) > serialStaleAfter {
					return fmt.Errorf("%s: %d 秒无数据: %w", key, int(serialStaleAfter.Seconds()), errNoSerialData)
				}
				continue // 行间静默属正常：继续等下一包
			}
			return fmt.Errorf("%s: read: %w", key, err)
		}
	}
}

// serialTempPattern 数字片段（允许正负号与小数；ParseFloat 兼容 "+25.6"）。
// 取值时再过滤合理区间，因为 "0 25.6"（地址+温度）这类行首数字不是温度。
var serialTempPattern = regexp.MustCompile(`[+-]?\d+(?:\.\d+)?`)

// extractSerialTemperature 取行内第一个落在 [-55,125] 的数字当摄氏温度。
// 区间取 DS18B20 的物理量程：真机固件按带符号小数输出（stty 4800 下
// `cat /dev/ttyUSB0` 得 "+25.6"/"-3.0"），负温是合法值；-127（DS18B20 断连
// 哨兵）与区间外的 raw/错位值（1256、999）一并拒绝。恰好 0.0 也拒：DIY
// 固件常用 0 做错误哨兵，且"地址+温度"行式（"0 25.6"）的行首 0 不是温度。
func extractSerialTemperature(line string) (float64, bool) {
	for _, match := range serialTempPattern.FindAllString(line, -1) {
		value, err := strconv.ParseFloat(match, 64)
		if err == nil && value != 0 && value >= -55 && value <= 125 {
			return value, true
		}
	}
	return 0, false
}

// serialSensorKey 由设备路径派生稳定传感器键（usb:tty:<名称>）。by-id 路径
// 末段对同一物理设备稳定；/dev/ttyUSB* 编号会随插拔漂移，设备下拉已优先
// 展示 by-id。名称截断到 32 字符，与 usbTempLabel 的约束一致。
func serialSensorKey(path string) string {
	label := filepath.Base(path)
	if len(label) > 32 {
		label = label[:32]
	}
	return "usb:tty:" + label
}

// serialReading 最近一次有效读数（At 为零值表示尚无数据）。
type serialReading struct {
	Key     string
	Celsius float64
	At      time.Time
}

func (m *Manager) storeSerialReading(key string, celsius float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.serialLatest = serialReading{Key: key, Celsius: celsius, At: serialNow()}
}

// serialTemperatureReadingLocked 返回仍新鲜的串口读数（供 extraTemperatures
// 并入采样链）。停止输出超过 serialStaleAfter 即失效——采样链不展示冻结值，
// 曲线在传感器拔掉后自然断线。调用方须持 m.mu（extraTemperatures 的调用
// 条件；内部再取锁会与 Status() 的持锁重入死锁）。
func (m *Manager) serialTemperatureReadingLocked() (Temperature, bool) {
	if m.serialLatest.At.IsZero() || serialNow().Sub(m.serialLatest.At) > serialStaleAfter {
		return Temperature{}, false
	}
	return Temperature{Label: m.serialLatest.Key, Celsius: m.serialLatest.Celsius}, true
}

func (m *Manager) setSerialOpen(open bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.serialOpen = open
}

// noteSerialError 记录/清除串口读取错误，错误变化时打一条日志（与
// noteUSBTempError 同款：每分钟重连不刷屏，恢复即清空）。
func (m *Manager) noteSerialError(err error) {
	message := ""
	if err != nil {
		message = err.Error()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if message == m.serialLastError {
		return
	}
	m.serialLastError = message
	if err != nil {
		m.logf("serial temperature sensor read failed: %v (logging once until the error changes)", err)
	}
}

// kickSerialReader 非阻塞通知读取器按新配置重连；读取器未运行时无事发生。
func (m *Manager) kickSerialReader() {
	m.mu.Lock()
	kick := m.serialKick
	m.mu.Unlock()
	if kick == nil {
		return
	}
	select {
	case kick <- struct{}{}:
	default:
	}
}

// SaveSerialSensorConfig 校验并保存串口传感器配置，踢断当前读取让新配置
// 立即生效（写盘走 saveConfigLocked，自动记 "config.json saved" 运行日志）。
func (m *Manager) SaveSerialSensorConfig(cfg SerialSensorConfig) error {
	cfg, err := NormalizeSerialSensorConfig(cfg)
	if err != nil {
		return err
	}
	m.mu.Lock()
	current, err := m.loadConfigLocked()
	if err != nil {
		m.lastError = err.Error()
		m.mu.Unlock()
		return err
	}
	current.Serial = cfg
	if err := m.saveConfigLocked(current); err != nil {
		m.lastError = err.Error()
		m.mu.Unlock()
		return err
	}
	m.mu.Unlock()
	m.kickSerialReader()
	return nil
}

// SerialSensorInfo 是随 /api/status 下发的串口传感器运行状态。
type SerialSensorInfo struct {
	Config      SerialSensorConfig `json:"config"`
	Devices     []SerialDeviceInfo `json:"devices"`
	Open        bool               `json:"open"`
	Key         string             `json:"key,omitempty"`
	LastCelsius float64            `json:"last_celsius,omitempty"`
	LastAt      time.Time          `json:"last_at,omitempty"`
	LastError   string             `json:"last_error,omitempty"`
}

// SerialDeviceInfo 是设备下拉里的一个候选串口。
type SerialDeviceInfo struct {
	Path  string `json:"path"`
	Label string `json:"label"`
}

// serialStatusLocked 汇总串口传感器运行状态。调用方须持 m.mu（与 Status()
// 一致）；设备枚举是纯 sysfs/目录读取，与 Status 现有的 DiscoverPackages
// 同量级开销。
func (m *Manager) serialStatusLocked(cfg SerialSensorConfig) SerialSensorInfo {
	info := SerialSensorInfo{Config: cfg, Devices: m.serialDeviceCandidates()}
	info.Open = m.serialOpen
	info.LastError = m.serialLastError
	if !m.serialLatest.At.IsZero() {
		info.Key = m.serialLatest.Key
		info.LastCelsius = m.serialLatest.Celsius
		info.LastAt = m.serialLatest.At
	}
	return info
}

// serialDeviceCandidates 枚举可用的候选串口：优先 /dev/serial/by-id/（名字
// 含 VID:PID 与序列号，插拔编号不漂移），再补 /sys/class/tty 下的 ttyUSB*/
// ttyACM*（有 device 子目录 = 真实硬件，排除板载 ttyS* 虚拟口）。
func (m *Manager) serialDeviceCandidates() []SerialDeviceInfo {
	var result []SerialDeviceInfo
	seen := make(map[string]bool)
	add := func(path, label string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		result = append(result, SerialDeviceInfo{Path: path, Label: label})
	}
	if entries, err := os.ReadDir(m.rooted("/dev/serial/by-id")); err == nil {
		for _, entry := range entries {
			name := entry.Name()
			label := strings.TrimPrefix(name, "usb-")
			label = strings.TrimSuffix(label, "-if00-port0")
			label = strings.TrimSuffix(label, "-if00")
			if label == "" {
				label = name
			}
			add(m.rooted(filepath.Join("/dev/serial/by-id", name)), label)
		}
	}
	for _, pattern := range []string{"/sys/class/tty/ttyUSB*", "/sys/class/tty/ttyACM*"} {
		nodes, _ := filepath.Glob(m.rooted(pattern))
		sort.Strings(nodes)
		for _, node := range nodes {
			if _, err := os.Stat(filepath.Join(node, "device")); err != nil {
				continue
			}
			name := filepath.Base(node)
			add(m.rooted(filepath.Join("/dev", name)), name)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Label < result[j].Label })
	return result
}
