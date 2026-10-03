package powerguard

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const fanStateVersion = 1

type FanPoint struct {
	TempC      float64 `json:"temp_c"`
	PWMPercent int     `json:"pwm_percent"`
}

type FanConfig struct {
	Enabled        bool       `json:"enabled"`
	DeviceID       string     `json:"device_id,omitempty"`
	CPUFanIDs      []string   `json:"cpu_fan_ids"`
	HDDFanIDs      []string   `json:"hdd_fan_ids"`
	NVMeFanIDs     []string   `json:"nvme_fan_ids"`
	MinPWMPercent  int        `json:"min_pwm_percent"`
	EmergencyTempC float64    `json:"emergency_temp_c"`
	PollSeconds    int        `json:"poll_seconds"`
	Curve          []FanPoint `json:"curve"`
	DiskCurve      []FanPoint `json:"disk_curve,omitempty"`
	HDDCurve       []FanPoint `json:"hdd_curve"`
	NVMeCurve      []FanPoint `json:"nvme_curve"`
	HDDSlotIDs     []string   `json:"hdd_slot_ids"`
	NVMeSlotIDs    []string   `json:"nvme_slot_ids"`
}

type FanDevice struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Channel    int    `json:"channel"`
	RPM        int64  `json:"rpm"`
	PWM        int64  `json:"pwm"`
	Mode       int64  `json:"mode"`
	InputPath  string `json:"-"`
	PWMPath    string `json:"-"`
	EnablePath string `json:"-"`
}

type FanStatus struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Channel        int      `json:"channel"`
	RPM            int64    `json:"rpm"`
	PWM            int64    `json:"pwm"`
	PWMPercent     int      `json:"pwm_percent"`
	Mode           int64    `json:"mode"`
	Selected       bool     `json:"selected"`
	ControlSources []string `json:"control_sources,omitempty"`
}

type FanControlStatus struct {
	Available            bool        `json:"available"`
	DriverDetected       bool        `json:"driver_detected"`
	Active               bool        `json:"active"`
	TemperatureC         float64     `json:"temperature_c,omitempty"`
	CPUTemperatureC      float64     `json:"cpu_temperature_c,omitempty"`
	DiskTemperatureC     float64     `json:"disk_temperature_c,omitempty"`
	HDDTemperatureC      float64     `json:"hdd_temperature_c,omitempty"`
	NVMeTemperatureC     float64     `json:"nvme_temperature_c,omitempty"`
	TargetPWMPercent     int         `json:"target_pwm_percent,omitempty"`
	CPUTargetPWMPercent  int         `json:"cpu_target_pwm_percent,omitempty"`
	DiskTargetPWMPercent int         `json:"disk_target_pwm_percent,omitempty"`
	HDDTargetPWMPercent  int         `json:"hdd_target_pwm_percent,omitempty"`
	NVMeTargetPWMPercent int         `json:"nvme_target_pwm_percent,omitempty"`
	ControlSource        string      `json:"control_source,omitempty"`
	Fans                 []FanStatus `json:"fans"`
	LastApply            time.Time   `json:"last_apply,omitempty"`
	LastError            string      `json:"last_error,omitempty"`
}

type originalFan struct {
	ID   string `json:"id"`
	PWM  int64  `json:"pwm"`
	Mode int64  `json:"mode"`
}

type originalFanState struct {
	Version    int           `json:"version"`
	CapturedAt time.Time     `json:"captured_at"`
	Fans       []originalFan `json:"fans"`
}

func DefaultFanConfig() FanConfig {
	config := FanConfig{
		Enabled:        false,
		MinPWMPercent:  60,
		EmergencyTempC: 85,
		PollSeconds:    2,
		Curve: []FanPoint{
			{TempC: 40, PWMPercent: 60},
			{TempC: 55, PWMPercent: 70},
			{TempC: 70, PWMPercent: 85},
			{TempC: 80, PWMPercent: 100},
		},
	}
	config.HDDCurve = defaultStorageFanCurve(config.MinPWMPercent)
	config.NVMeCurve = defaultStorageFanCurve(config.MinPWMPercent)
	config.HDDSlotIDs = storageSlotIDs("front")
	config.NVMeSlotIDs = storageSlotIDs("m2")
	return config
}

func storageSlotIDs(kind string) []string {
	var ids []string
	for _, spec := range storageSlotSpecs {
		if spec.Kind == kind {
			ids = append(ids, spec.ID)
		}
	}
	return ids
}

func defaultStorageFanCurve(minimum int) []FanPoint {
	minimum = max(30, min(minimum, 100))
	return []FanPoint{
		{TempC: 25, PWMPercent: minimum},
		{TempC: 35, PWMPercent: max(minimum, 85)},
		{TempC: 50, PWMPercent: 100},
	}
}

func normalizeConfig(cfg *Config) bool {
	changed := false
	if cfg.Fan.Curve == nil {
		enabled := cfg.Fan.Enabled
		deviceID := cfg.Fan.DeviceID
		cpuFanIDs := append([]string(nil), cfg.Fan.CPUFanIDs...)
		hddFanIDs := append([]string(nil), cfg.Fan.HDDFanIDs...)
		nvmeFanIDs := append([]string(nil), cfg.Fan.NVMeFanIDs...)
		cfg.Fan = DefaultFanConfig()
		cfg.Fan.Enabled = enabled
		cfg.Fan.DeviceID = deviceID
		cfg.Fan.CPUFanIDs = cpuFanIDs
		cfg.Fan.HDDFanIDs = hddFanIDs
		cfg.Fan.NVMeFanIDs = nvmeFanIDs
		changed = true
	}
	if cfg.Fan.HDDCurve == nil {
		if cfg.Fan.DiskCurve != nil {
			cfg.Fan.HDDCurve = append([]FanPoint(nil), cfg.Fan.DiskCurve...)
		} else {
			cfg.Fan.HDDCurve = defaultStorageFanCurve(cfg.Fan.MinPWMPercent)
		}
		changed = true
	}
	if cfg.Fan.NVMeCurve == nil {
		cfg.Fan.NVMeCurve = defaultStorageFanCurve(cfg.Fan.MinPWMPercent)
		changed = true
	}
	if cfg.Fan.HDDSlotIDs == nil {
		cfg.Fan.HDDSlotIDs = storageSlotIDs("front")
		changed = true
	}
	if cfg.Fan.NVMeSlotIDs == nil {
		cfg.Fan.NVMeSlotIDs = storageSlotIDs("m2")
		changed = true
	}
	if cfg.Fan.DiskCurve != nil {
		cfg.Fan.DiskCurve = nil
		changed = true
	}
	if normalizeFanSelections(&cfg.Fan) {
		changed = true
	}
	if cfg.GPIO.Version == 0 {
		cfg.GPIO = DefaultGPIOConfig()
		changed = true
	}
	if cfg.History.MaxSizeMB == 0 {
		// 旧版本配置文件没有 history 段：默认启用采样并套用默认大小上限与保留期
		cfg.History = DefaultHistoryConfig()
		changed = true
	} else if clamped := ClampHistoryMaxSize(cfg.History.MaxSizeMB); clamped != cfg.History.MaxSizeMB {
		cfg.History.MaxSizeMB = clamped
		changed = true
	}
	if cfg.History.RetentionDays == 0 {
		// 旧配置没有 retention_days 字段：解析后为 0，静默归位为默认保留期
		cfg.History.RetentionDays = historyDefaultRetentionDays
		changed = true
	} else if clamped := ClampHistoryRetentionDays(cfg.History.RetentionDays); clamped != cfg.History.RetentionDays {
		cfg.History.RetentionDays = clamped
		changed = true
	}
	if !cfg.History.ArchiveEnabled && cfg.History.ArchiveDir != "" {
		// 长期记录关闭时清掉目录，避免配置里残留无意义路径
		cfg.History.ArchiveDir = ""
		changed = true
	}
	if cfg.Log.MaxSizeMB == 0 {
		// 旧配置没有 log 段：默认日志大小上限
		cfg.Log = DefaultLogConfig()
		changed = true
	} else if clamped := ClampLogMaxSize(cfg.Log.MaxSizeMB); clamped != cfg.Log.MaxSizeMB {
		cfg.Log.MaxSizeMB = clamped
		changed = true
	}
	for index := range cfg.GPIO.Buttons {
		if cfg.GPIO.Buttons[index].Actions.Short != GPIOActionNone {
			cfg.GPIO.Buttons[index].Actions.Short = GPIOActionNone
			changed = true
		}
	}
	return changed
}

func normalizeFanSelections(cfg *FanConfig) bool {
	changed := false
	if cfg.DeviceID != "" && cfg.CPUFanIDs == nil && cfg.HDDFanIDs == nil && cfg.NVMeFanIDs == nil {
		cfg.CPUFanIDs = []string{cfg.DeviceID}
		cfg.HDDFanIDs = []string{cfg.DeviceID}
		cfg.NVMeFanIDs = []string{cfg.DeviceID}
		changed = true
	}
	for _, ids := range []*[]string{&cfg.CPUFanIDs, &cfg.HDDFanIDs, &cfg.NVMeFanIDs} {
		normalized := uniqueNonEmpty(*ids)
		if len(normalized) != len(*ids) {
			changed = true
		}
		*ids = normalized
	}
	return changed
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func selectedFanSources(cfg FanConfig) map[string][]string {
	result := make(map[string][]string)
	for source, ids := range map[string][]string{"cpu": cfg.CPUFanIDs, "hdd": cfg.HDDFanIDs, "nvme": cfg.NVMeFanIDs} {
		for _, id := range ids {
			result[id] = append(result[id], source)
		}
	}
	return result
}

func (m *Manager) validateFanLocked(cfg FanConfig) error {
	if cfg.MinPWMPercent < 30 || cfg.MinPWMPercent > 100 {
		return errors.New("fan min_pwm_percent must be between 30 and 100")
	}
	if cfg.EmergencyTempC < 70 || cfg.EmergencyTempC > 100 {
		return errors.New("fan emergency_temp_c must be between 70 and 100")
	}
	if cfg.PollSeconds < 1 || cfg.PollSeconds > 10 {
		return errors.New("fan poll_seconds must be between 1 and 10")
	}
	if err := validateFanCurve("CPU", cfg.Curve, cfg.MinPWMPercent, cfg.EmergencyTempC); err != nil {
		return err
	}
	if err := validateFanCurve("HDD", cfg.HDDCurve, cfg.MinPWMPercent, cfg.EmergencyTempC); err != nil {
		return err
	}
	if err := validateFanCurve("NVMe", cfg.NVMeCurve, cfg.MinPWMPercent, cfg.EmergencyTempC); err != nil {
		return err
	}
	if err := validateFanSlotIDs("HDD", "front", cfg.HDDSlotIDs); err != nil {
		return err
	}
	if err := validateFanSlotIDs("NVMe", "m2", cfg.NVMeSlotIDs); err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	selected := selectedFanSources(cfg)
	if len(selected) == 0 {
		return errors.New("at least one fan must be selected when fan control is enabled")
	}
	fans, err := m.DiscoverFans()
	if err != nil {
		return err
	}
	available := make(map[string]FanDevice, len(fans))
	for _, fan := range fans {
		available[fan.ID] = fan
	}
	for id := range selected {
		fan, ok := available[id]
		if !ok {
			return fmt.Errorf("configured fan %s was not found", id)
		}
		if fan.RPM <= 0 {
			return fmt.Errorf("fan %s reports no valid RPM and cannot be controlled safely", fan.ID)
		}
	}
	return nil
}

func validateFanSlotIDs(name, kind string, ids []string) error {
	allowed := make(map[string]bool)
	for _, spec := range storageSlotSpecs {
		if spec.Kind == kind {
			allowed[spec.ID] = true
		}
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !allowed[id] {
			return fmt.Errorf("fan %s slot %q is not a supported %s slot", name, id, kind)
		}
		if seen[id] {
			return fmt.Errorf("fan %s slot %q is duplicated", name, id)
		}
		seen[id] = true
	}
	return nil
}

func validateFanCurve(name string, curve []FanPoint, minimum int, emergencyTempC float64) error {
	if len(curve) < 2 || len(curve) > 8 {
		return fmt.Errorf("fan %s curve must contain between 2 and 8 points", name)
	}
	previousTemp := -1.0
	previousPWM := -1
	for i, point := range curve {
		if point.TempC < 20 || point.TempC > 100 {
			return fmt.Errorf("fan %s curve point %d temperature must be between 20 and 100", name, i+1)
		}
		if point.TempC <= previousTemp {
			return fmt.Errorf("fan %s curve temperatures must be strictly increasing", name)
		}
		if point.PWMPercent < minimum || point.PWMPercent > 100 {
			return fmt.Errorf("fan %s curve point %d PWM must be between minimum PWM and 100", name, i+1)
		}
		if point.PWMPercent < previousPWM {
			return fmt.Errorf("fan %s curve PWM values must not decrease as temperature rises", name)
		}
		previousTemp = point.TempC
		previousPWM = point.PWMPercent
	}
	if emergencyTempC < curve[len(curve)-1].TempC {
		return fmt.Errorf("fan emergency temperature must not be below the last %s curve point", name)
	}
	return nil
}

func (m *Manager) DiscoverFans() ([]FanDevice, error) {
	namePaths, _ := filepath.Glob(m.rooted("/sys/class/hwmon/hwmon*/name"))
	var fans []FanDevice
	for _, namePath := range namePaths {
		name, err := readTrim(namePath)
		if err != nil || !isIT87Name(name) {
			continue
		}
		dir := filepath.Dir(namePath)
		devicePath, err := filepath.EvalSymlinks(filepath.Join(dir, "device"))
		if err != nil {
			devicePath = dir
		}
		deviceName := filepath.Base(devicePath)
		inputs, _ := filepath.Glob(filepath.Join(dir, "fan*_input"))
		for _, inputPath := range inputs {
			base := filepath.Base(inputPath)
			channelText := strings.TrimSuffix(strings.TrimPrefix(base, "fan"), "_input")
			channel, err := strconv.Atoi(channelText)
			if err != nil || channel < 1 {
				continue
			}
			pwmPath := filepath.Join(dir, fmt.Sprintf("pwm%d", channel))
			enablePath := filepath.Join(dir, fmt.Sprintf("pwm%d_enable", channel))
			if _, err := os.Stat(pwmPath); err != nil {
				continue
			}
			if _, err := os.Stat(enablePath); err != nil {
				continue
			}
			rpm, rpmErr := readInt(inputPath)
			pwm, pwmErr := readInt(pwmPath)
			mode, modeErr := readInt(enablePath)
			if rpmErr != nil || pwmErr != nil || modeErr != nil {
				continue
			}
			fans = append(fans, FanDevice{
				ID:         fmt.Sprintf("%s:%s:fan%d", name, deviceName, channel),
				Name:       name,
				Channel:    channel,
				RPM:        rpm,
				PWM:        pwm,
				Mode:       mode,
				InputPath:  inputPath,
				PWMPath:    pwmPath,
				EnablePath: enablePath,
			})
		}
	}
	sort.Slice(fans, func(i, j int) bool { return fans[i].ID < fans[j].ID })
	return fans, nil
}

func isIT87Name(name string) bool {
	return name == "it87" || strings.HasPrefix(name, "it8")
}

func (m *Manager) IT87DriverDetected() bool {
	namePaths, _ := filepath.Glob(m.rooted("/sys/class/hwmon/hwmon*/name"))
	for _, namePath := range namePaths {
		name, err := readTrim(namePath)
		if err == nil && isIT87Name(name) {
			return true
		}
	}
	return false
}

func (m *Manager) ApplyFanCurrent() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cfg, err := m.loadConfigLocked()
	if err != nil {
		return m.setFanFailureLocked(err)
	}
	// 风扇调试接管：被接管的风扇脱离一切曲线控制(applyFanLocked 中锁定),
	// 未接管的风扇照常受曲线控制。紧急温度兜底覆盖所有风扇(含被接管)。
	if err := m.applyFanDebugEmergencyLocked(cfg.Fan); err != nil {
		return err
	}
	if !cfg.Fan.Enabled {
		m.fanLastError = ""
		return nil
	}
	if err := m.validateFanLocked(cfg.Fan); err != nil {
		return m.setFanFailureLocked(err)
	}
	if err := m.applyFanLocked(cfg.Fan); err != nil {
		m.fanLastError = err.Error()
		return err
	}
	m.fanLastError = ""
	return nil
}

func (m *Manager) setFanFailureLocked(cause error) error {
	failSafeErr := m.failSafeCapturedFansLocked()
	err := errors.Join(cause, failSafeErr)
	m.fanLastError = err.Error()
	return err
}

// applyFanDebugEmergencyLocked 有风扇被调试接管时的紧急满速兜底：CPU
// （coretemp 最大值）超过紧急温度时强制全部风扇 100% 并标记覆盖（调试转速
// 被覆盖,状态里显著提示）;磁盘温度不参与兜底判定（读盘会唤醒休眠盘）。
// 无接管时清除标记并放行常规曲线控制。
func (m *Manager) applyFanDebugEmergencyLocked(cfg FanConfig) error {
	if len(m.fanDebugTakenOver) == 0 {
		m.fanDebugEmergency = false
		return nil
	}
	temperature, err := m.controlTemperature()
	if err == nil && temperature >= cfg.EmergencyTempC {
		fans, ferr := m.DiscoverFans()
		if ferr != nil {
			return ferr
		}
		var errs []error
		for i := range fans {
			if err := setFanPWM(fans[i], 100); err != nil {
				errs = append(errs, err)
			}
		}
		m.fanDebugEmergency = true
		return errors.Join(errs...)
	}
	m.fanDebugEmergency = false
	return nil
}

func (m *Manager) applyFanLocked(cfg FanConfig) error {
	fans, err := m.DiscoverFans()
	if err != nil {
		return err
	}
	selected := selectedFanSources(cfg)
	byID := make(map[string]FanDevice, len(fans))
	for _, fan := range fans {
		byID[fan.ID] = fan
	}
	for id := range selected {
		fan, ok := byID[id]
		if !ok {
			return fmt.Errorf("configured fan %s was not found", id)
		}
		if err := m.captureOriginalFanLocked(fan); err != nil {
			return err
		}
	}

	cpuTemperature, cpuErr := m.controlTemperature()
	storage := m.StorageStatus()
	hddTemperature, hddAvailable := maximumStorageTemperature(storage, "front", cfg.HDDSlotIDs)
	nvmeTemperature, nvmeAvailable := maximumStorageTemperature(storage, "m2", cfg.NVMeSlotIDs)
	cpuTarget := 0
	if cpuErr == nil {
		cpuTarget = interpolatePWMPercent(cfg.Curve, cpuTemperature, cfg.MinPWMPercent, cfg.EmergencyTempC)
	}
	hddTarget := 0
	if hddAvailable {
		hddTarget = interpolatePWMPercent(cfg.HDDCurve, hddTemperature, cfg.MinPWMPercent, cfg.EmergencyTempC)
	}
	nvmeTarget := 0
	if nvmeAvailable {
		nvmeTarget = interpolatePWMPercent(cfg.NVMeCurve, nvmeTemperature, cfg.MinPWMPercent, cfg.EmergencyTempC)
	}
	emergency := (len(cfg.CPUFanIDs) > 0 && cpuErr == nil && cpuTemperature >= cfg.EmergencyTempC) ||
		(len(cfg.HDDFanIDs) > 0 && hddAvailable && hddTemperature >= cfg.EmergencyTempC) ||
		(len(cfg.NVMeFanIDs) > 0 && nvmeAvailable && nvmeTemperature >= cfg.EmergencyTempC)

	lastTarget := 0
	var errs []error
	for id, sources := range selected {
		fan := byID[id]
		if _, taken := m.fanDebugTakenOver[id]; taken {
			continue // 被调试接管:脱离曲线控制,由调试接口锁定转速
		}
		if fan.RPM <= 0 {
			errs = append(errs, fmt.Errorf("fan %s reports no valid RPM", id), setFanPWM(fan, 100))
			continue
		}
		target := 0
		if emergency {
			target = 100
		} else {
			for _, source := range sources {
				switch source {
				case "cpu":
					target = max(target, cpuTarget)
				case "hdd":
					target = max(target, hddTarget)
				case "nvme":
					target = max(target, nvmeTarget)
				}
			}
		}
		if target == 0 {
			target = 100
			errs = append(errs, fmt.Errorf("no selected temperature source is available for fan %s; forced to full speed", id))
		}
		if err := setFanPWM(fan, target); err != nil {
			errs = append(errs, fmt.Errorf("set fan %s to %d%%: %w", id, target, err), setFanPWM(fan, 100))
			continue
		}
		lastTarget = max(lastTarget, target)
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	m.fanLastApply = time.Now()
	m.fanLastTarget = lastTarget
	if cpuErr == nil {
		m.fanLastTemp = cpuTemperature
	}
	return nil
}

func setFanPWM(fan FanDevice, percent int) error {
	return setFanPWMRaw(fan, percentToPWM(percent))
}

// setFanPWMRaw 直接按原始占空比(0–255)写入;百分比调用方先换算。
func setFanPWMRaw(fan FanDevice, raw int) error {
	if raw < 0 || raw > 255 {
		return fmt.Errorf("invalid fan PWM raw %d", raw)
	}
	currentPWM, err := readInt(fan.PWMPath)
	if err != nil {
		return fmt.Errorf("read pwm%d: %w", fan.Channel, err)
	}
	currentMode, err := readInt(fan.EnablePath)
	if err != nil {
		return fmt.Errorf("read pwm%d mode: %w", fan.Channel, err)
	}
	if raw == 255 && currentPWM == 255 && currentMode == 0 {
		return nil
	}
	if raw < 255 && currentMode == 0 {
		// This IT87 driver represents full speed as mode 0 and refuses mode 1
		// while PWM is still 255. Lowering PWM first atomically re-enters manual mode.
		if err := writeAndVerify(fan.PWMPath, int64(raw)); err != nil {
			return fmt.Errorf("leave pwm%d full-speed mode: %w", fan.Channel, err)
		}
	}
	if err := writeAndVerify(fan.EnablePath, 1); err != nil {
		return fmt.Errorf("switch pwm%d to manual mode: %w", fan.Channel, err)
	}
	if err := writeAndVerify(fan.PWMPath, int64(raw)); err != nil {
		return fmt.Errorf("write pwm%d: %w", fan.Channel, err)
	}
	mode, err := readInt(fan.EnablePath)
	if err != nil {
		return err
	}
	if raw < 255 && mode != 1 {
		return fmt.Errorf("pwm%d mode changed to %d instead of manual mode", fan.Channel, mode)
	}
	if raw == 255 && mode != 0 && mode != 1 {
		return fmt.Errorf("pwm%d returned unexpected full-speed mode %d", fan.Channel, mode)
	}
	return nil
}

func percentToPWM(percent int) int {
	return int(math.Round(float64(percent) * 255 / 100))
}

func pwmToPercent(pwm int64) int {
	return int(math.Round(float64(pwm) * 100 / 255))
}

func interpolatePWMPercent(points []FanPoint, tempC float64, minimum int, emergencyTempC float64) int {
	if tempC >= emergencyTempC {
		return 100
	}
	value := points[0].PWMPercent
	if tempC <= points[0].TempC {
		value = points[0].PWMPercent
	} else if tempC >= points[len(points)-1].TempC {
		value = points[len(points)-1].PWMPercent
	} else {
		for i := 1; i < len(points); i++ {
			if tempC > points[i].TempC {
				continue
			}
			left, right := points[i-1], points[i]
			ratio := (tempC - left.TempC) / (right.TempC - left.TempC)
			value = int(math.Round(float64(left.PWMPercent) + ratio*float64(right.PWMPercent-left.PWMPercent)))
			break
		}
	}
	if value < minimum {
		value = minimum
	}
	if value > 100 {
		value = 100
	}
	return value
}

func fanTargets(cfg FanConfig, cpuTemperature, hddTemperature float64, hddAvailable bool, nvmeTemperature float64, nvmeAvailable bool) (int, int, int, int) {
	cpuTarget := interpolatePWMPercent(cfg.Curve, cpuTemperature, cfg.MinPWMPercent, cfg.EmergencyTempC)
	hddTarget := 0
	if hddAvailable {
		hddTarget = interpolatePWMPercent(cfg.HDDCurve, hddTemperature, cfg.MinPWMPercent, cfg.EmergencyTempC)
	}
	nvmeTarget := 0
	if nvmeAvailable {
		nvmeTarget = interpolatePWMPercent(cfg.NVMeCurve, nvmeTemperature, cfg.MinPWMPercent, cfg.EmergencyTempC)
	}
	return cpuTarget, hddTarget, nvmeTarget, max(cpuTarget, hddTarget, nvmeTarget)
}

func maximumStorageTemperature(status StorageStatus, kind string, slotIDs []string) (float64, bool) {
	selected := make(map[string]bool, len(slotIDs))
	for _, id := range slotIDs {
		selected[id] = true
	}
	maximum := 0.0
	available := false
	for _, slot := range status.Slots {
		if slot.Kind != kind || !selected[slot.ID] || slot.TemperatureC <= 0 {
			continue
		}
		if !available || slot.TemperatureC > maximum {
			maximum = slot.TemperatureC
			available = true
		}
	}
	return maximum, available
}

func fanControlSource(cpuTarget, hddTarget, nvmeTarget int) string {
	target := max(cpuTarget, hddTarget, nvmeTarget)
	if target <= 0 {
		return ""
	}
	var sources []string
	if cpuTarget == target {
		sources = append(sources, "cpu")
	}
	if hddTarget == target {
		sources = append(sources, "hdd")
	}
	if nvmeTarget == target {
		sources = append(sources, "nvme")
	}
	return strings.Join(sources, "+")
}

func (m *Manager) controlTemperature() (float64, error) {
	temperatures := m.temperatures()
	if len(temperatures) == 0 {
		return 0, errors.New("no readable coretemp sensor was found")
	}
	maximum := temperatures[0].Celsius
	for _, temperature := range temperatures[1:] {
		if temperature.Celsius > maximum {
			maximum = temperature.Celsius
		}
	}
	return maximum, nil
}

func (m *Manager) fanStatePath() string {
	return filepath.Join(filepath.Dir(m.StatePath), "original-fan-state.json")
}

func (m *Manager) captureOriginalFanLocked(fan FanDevice) error {
	if err := m.validateSavedHardwareIfAvailable(); err != nil {
		return err
	}
	state, err := m.loadFanStateLocked()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if errors.Is(err, fs.ErrNotExist) {
		state = originalFanState{Version: fanStateVersion, CapturedAt: time.Now()}
	}
	for _, original := range state.Fans {
		if original.ID == fan.ID {
			return nil
		}
	}
	state.Fans = append(state.Fans, originalFan{ID: fan.ID, PWM: fan.PWM, Mode: fan.Mode})
	return writeJSONAtomic(m.fanStatePath(), state, 0o600)
}

func (m *Manager) loadFanStateLocked() (originalFanState, error) {
	data, err := os.ReadFile(m.fanStatePath())
	if err != nil {
		return originalFanState{}, err
	}
	var state originalFanState
	if err := jsonUnmarshalStrict(data, &state); err != nil {
		return originalFanState{}, fmt.Errorf("decode fan state: %w", err)
	}
	if state.Version != fanStateVersion {
		return originalFanState{}, fmt.Errorf("unsupported fan state version %d", state.Version)
	}
	return state, nil
}

func (m *Manager) restoreFansLocked() error {
	state, err := m.loadFanStateLocked()
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := m.validateSavedHardwareIfAvailable(); err != nil {
		return err
	}
	fans, err := m.DiscoverFans()
	if err != nil {
		return err
	}
	byID := make(map[string]FanDevice, len(fans))
	for _, fan := range fans {
		byID[fan.ID] = fan
	}
	var errs []error
	for _, original := range state.Fans {
		fan, ok := byID[original.ID]
		if !ok {
			errs = append(errs, fmt.Errorf("fan %s is no longer present", original.ID))
			continue
		}
		if err := restoreFan(fan, original); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func restoreFan(fan FanDevice, original originalFan) error {
	if original.Mode < 0 || original.Mode > 2 || original.PWM < 0 || original.PWM > 255 {
		return fmt.Errorf("saved state for fan %s is invalid", fan.ID)
	}
	if err := writeAndVerify(fan.EnablePath, 1); err != nil {
		return fmt.Errorf("restore fan %s manual mode: %w", fan.ID, err)
	}
	if err := writeAndVerify(fan.PWMPath, original.PWM); err != nil {
		return fmt.Errorf("restore fan %s PWM: %w", fan.ID, err)
	}
	if err := writeAndVerify(fan.EnablePath, original.Mode); err != nil {
		return fmt.Errorf("restore fan %s mode: %w", fan.ID, err)
	}
	return nil
}

func (m *Manager) failSafeCapturedFansLocked() error {
	state, err := m.loadFanStateLocked()
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := m.validateSavedHardwareIfAvailable(); err != nil {
		return err
	}
	fans, err := m.DiscoverFans()
	if err != nil {
		return err
	}
	byID := make(map[string]FanDevice, len(fans))
	for _, fan := range fans {
		byID[fan.ID] = fan
	}
	var errs []error
	for _, original := range state.Fans {
		fan, ok := byID[original.ID]
		if !ok {
			errs = append(errs, fmt.Errorf("fan %s is no longer present for fail-safe", original.ID))
			continue
		}
		if err := setFanPWM(fan, 100); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) fanStatusLocked(cfg FanConfig) FanControlStatus {
	result := FanControlStatus{
		Active:           cfg.Enabled,
		DriverDetected:   m.IT87DriverDetected(),
		LastApply:        m.fanLastApply,
		LastError:        m.fanLastError,
		TargetPWMPercent: m.fanLastTarget,
		TemperatureC:     m.fanLastTemp,
	}
	fans, err := m.DiscoverFans()
	if err != nil {
		result.LastError = combineError(result.LastError, err)
		return result
	}
	selected := selectedFanSources(cfg)
	for _, fan := range fans {
		if fan.RPM > 0 {
			result.Available = true
		}
		result.Fans = append(result.Fans, FanStatus{
			ID:             fan.ID,
			Name:           fan.Name,
			Channel:        fan.Channel,
			RPM:            fan.RPM,
			PWM:            fan.PWM,
			PWMPercent:     pwmToPercent(fan.PWM),
			Mode:           fan.Mode,
			Selected:       len(selected[fan.ID]) > 0,
			ControlSources: append([]string(nil), selected[fan.ID]...),
		})
	}
	if temperature, err := m.controlTemperature(); err == nil {
		result.TemperatureC = temperature
		result.CPUTemperatureC = temperature
		if len(cfg.Curve) >= 2 {
			result.CPUTargetPWMPercent = interpolatePWMPercent(cfg.Curve, temperature, cfg.MinPWMPercent, cfg.EmergencyTempC)
		}
	} else if cfg.Enabled {
		result.LastError = combineError(result.LastError, err)
	}
	storage := m.StorageStatus()
	if temperature, available := maximumStorageTemperature(storage, "front", cfg.HDDSlotIDs); available {
		result.HDDTemperatureC = temperature
		if len(cfg.HDDCurve) >= 2 {
			result.HDDTargetPWMPercent = interpolatePWMPercent(cfg.HDDCurve, temperature, cfg.MinPWMPercent, cfg.EmergencyTempC)
		}
	}
	if temperature, available := maximumStorageTemperature(storage, "m2", cfg.NVMeSlotIDs); available {
		result.NVMeTemperatureC = temperature
		if len(cfg.NVMeCurve) >= 2 {
			result.NVMeTargetPWMPercent = interpolatePWMPercent(cfg.NVMeCurve, temperature, cfg.MinPWMPercent, cfg.EmergencyTempC)
		}
	}
	result.DiskTemperatureC = max(result.HDDTemperatureC, result.NVMeTemperatureC)
	result.DiskTargetPWMPercent = max(result.HDDTargetPWMPercent, result.NVMeTargetPWMPercent)
	result.TargetPWMPercent = max(result.CPUTargetPWMPercent, result.HDDTargetPWMPercent, result.NVMeTargetPWMPercent)
	result.ControlSource = fanControlSource(result.CPUTargetPWMPercent, result.HDDTargetPWMPercent, result.NVMeTargetPWMPercent)
	return result
}

func jsonUnmarshalStrict(data []byte, value any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

// ---- 风扇调试控制（调试页勾选显示；接管粒度为单个风扇） ----

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

type fanDebugAutoEntry struct {
	Step     int    // 每次递增量(单位随 Unit)
	Interval int    // 秒
	Unit     string // "percent"(转速%) 或 "pwm"(原始占空比)
	LastRamp time.Time
	Running  bool // 该风扇的自动递增进行中
	Done     bool // 已到该单位上限
}

// fanDebugAutoTest 持有全部风扇的自动递增状态;goroutine 在有风扇递增时存活。
type fanDebugAutoTest struct {
	stop    chan struct{}
	entries map[string]fanDebugAutoEntry // 风扇 ID → 各自的递增参数与进度
}

// SetFanDebugTakeover 接管/释放单个风扇：接管后该风扇脱离一切温控曲线
// （曲线目标不再写入），转速仅由调试接口驱动；释放后立即回到曲线控制。
// 接管瞬间捕获 BIOS 原始状态，供插件停止/卸载时恢复。
func (m *Manager) SetFanDebugTakeover(id string, taken bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	fans, err := m.DiscoverFans()
	if err != nil {
		return err
	}
	var target *FanDevice
	for i := range fans {
		if fans[i].ID == id {
			target = &fans[i]
			break
		}
	}
	if target == nil {
		return fmt.Errorf("fan %s was not found", id)
	}
	if m.fanDebugTakenOver == nil {
		m.fanDebugTakenOver = map[string]int{}
	}
	if m.fanDebugUnits == nil {
		m.fanDebugUnits = map[string]string{}
	}
	if taken {
		if _, ok := m.fanDebugTakenOver[id]; !ok {
			if err := m.captureOriginalFanLocked(*target); err != nil {
				return err
			}
		}
		// 从当前转速无缝接管，避免转速跳变
		m.fanDebugTakenOver[id] = pwmToPercent(target.PWM)
	} else {
		delete(m.fanDebugTakenOver, id)
		// 释放接管同时终止该风扇的自动递增:条目删除,行内开关随之复位
		if m.fanDebugAuto != nil {
			delete(m.fanDebugAuto.entries, id)
		}
	}
	return nil
}

// fanDebugUnitMax 返回单位对应的调试值上限:pwm 0–255,percent 0–100。
func fanDebugUnitMax(unit string) int {
	if unit == "pwm" {
		return 255
	}
	if unit == "rpm" {
		return 2000
	}
	return 100
}

// fanDebugRaw 把单位值换算为写入硬件的原始 PWM。rpm 按 2000 RPM=100% 线性换算。
func fanDebugRaw(unit string, value int) int {
	if unit == "pwm" {
		return value
	}
	if unit == "rpm" {
		return percentToPWM((value + 10) / 20)
	}
	return percentToPWM(value)
}

// SetFanDebugValue 设定单个被接管风扇的调试值。value 的单位由 unit 决定:
// "rpm" 为转速(0–2000,按 2000=100% 换算),"percent" 为百分比(0–100),
// "pwm" 为原始占空比(0–255)。value 存储与自动递增都按该单位进行。
func (m *Manager) SetFanDebugValue(id string, value int, unit string) error {
	if unit != "percent" && unit != "pwm" && unit != "rpm" {
		return fmt.Errorf("未知的调节单位 %q", unit)
	}
	value = clampInt(value, 0, fanDebugUnitMax(unit))
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.fanDebugTakenOver[id]; !ok {
		return fmt.Errorf("风扇 %s 未被接管", id)
	}
	fans, err := m.DiscoverFans()
	if err != nil {
		return err
	}
	raw := fanDebugRaw(unit, value)
	var errs []error
	for i := range fans {
		if fans[i].ID != id {
			continue
		}
		if err := setFanPWMRaw(fans[i], raw); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", fans[i].ID, err))
		}
	}
	m.fanDebugTakenOver[id] = value
	oldUnit := m.fanDebugUnits[id]
	m.fanDebugUnits[id] = unit
	// 递增条目在跑时同步单位并把步进等比换算(200 RPM → 10%),递增循环以 fanDebugUnits 为准
	if m.fanDebugAuto != nil {
		if entry, ok := m.fanDebugAuto.entries[id]; ok {
			if oldUnit == "" {
				oldUnit = entry.Unit
			}
			if oldUnit != "" && oldUnit != unit && fanDebugUnitMax(oldUnit) > 0 {
				entry.Step = entry.Step * fanDebugUnitMax(unit) / fanDebugUnitMax(oldUnit)
				if entry.Step < 1 {
					entry.Step = 1
				}
			}
			entry.Unit = unit
			m.fanDebugAuto.entries[id] = entry
		}
	}
	if len(errs) > 0 {
		m.fanDebugLastError = errors.Join(errs...).Error()
		return errors.Join(errs...)
	}
	m.fanDebugLastError = ""
	return nil
}

// SetFanDebugAuto 单风扇自动递增的开/关:开启后以该风扇当前调试转速为基础,
// 每 interval 秒 +step 递增到单位上限自动完成(停在上限);关闭即停在当前转速
// 并保留步进/间隔,便于原样重新打开。各风扇互相独立——一个风扇递增时另一个
// 可以手动测试。上次已跑到上限时再次开启会归零重跑。
func (m *Manager) SetFanDebugAuto(id string, running bool, step, intervalSeconds int, unit string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fanDebugAuto == nil {
		m.fanDebugAuto = &fanDebugAutoTest{entries: map[string]fanDebugAutoEntry{}}
	}
	if m.fanDebugAuto.entries == nil {
		m.fanDebugAuto.entries = map[string]fanDebugAutoEntry{}
	}
	if unit != "pwm" && unit != "rpm" {
		unit = "percent"
	}
	// 关闭总是允许(幂等);开启要求风扇已接管且参数在该单位范围内
	if running {
		if _, taken := m.fanDebugTakenOver[id]; !taken {
			return fmt.Errorf("风扇 %s 未被接管,请先勾选接管", id)
		}
		if step < 1 || step > fanDebugUnitMax(unit) {
			return fmt.Errorf("风扇 %s 的递增转速需在 1–%d 之间", id, fanDebugUnitMax(unit))
		}
		if intervalSeconds < 1 || intervalSeconds > 120 {
			return fmt.Errorf("风扇 %s 的递增间隔需在 1–120 秒之间", id)
		}
	}
	entry := m.fanDebugAuto.entries[id]
	if running {
		// 基准已在单位上限(上次跑完):归零重跑,避免开了立刻又完成
		if base, ok := m.fanDebugTakenOver[id]; ok && base >= fanDebugUnitMax(unit) {
			m.fanDebugTakenOver[id] = 0
		}
		entry.Step = step
		entry.Interval = intervalSeconds
		entry.Unit = unit
		entry.LastRamp = time.Now()
		entry.Done = false
		entry.Running = true
		if m.fanDebugUnits == nil {
			m.fanDebugUnits = map[string]string{}
		}
		m.fanDebugUnits[id] = unit
	} else {
		entry.Running = false
		entry.Done = false
	}
	m.fanDebugAuto.entries[id] = entry
	if running {
		m.ensureFanDebugAutoLoop()
	}
	return nil
}

// ensureFanDebugAutoLoop 保证递增 goroutine 存活;无风扇递增时自动退出。
func (m *Manager) ensureFanDebugAutoLoop() {
	if m.fanDebugAutoLoopRunning {
		return
	}
	m.fanDebugAutoLoopRunning = true
	stop := make(chan struct{})
	m.fanDebugAuto.stop = stop
	go m.runFanDebugAuto(stop)
}

// runFanDebugAuto 500ms tick 一次:对每个 Running 且未 Done 的风扇,距上次
// 递增满 interval 秒 → 值 +step(封顶单位上限),写 PWM 并推进;全部完成或
// 无 Running 条目时 goroutine 退出。
func (m *Manager) runFanDebugAuto(stop chan struct{}) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}
		m.mu.Lock()
		now := time.Now()
		fans, ferr := m.DiscoverFans()
		anyRunning := false
		if ferr == nil {
			var errs []error
			for id, entry := range m.fanDebugAuto.entries {
				if !entry.Running || entry.Done {
					continue
				}
				if now.Sub(entry.LastRamp) < time.Duration(entry.Interval)*time.Second {
					anyRunning = true
					continue
				}
				unit := m.fanDebugUnits[id]
				if unit == "" {
					unit = entry.Unit
				}
				value := m.fanDebugTakenOver[id] + entry.Step
				if value > fanDebugUnitMax(unit) {
					value = fanDebugUnitMax(unit)
				}
				raw := fanDebugRaw(unit, value)
				for i := range fans {
					if fans[i].ID == id {
						if err := setFanPWMRaw(fans[i], raw); err != nil {
							errs = append(errs, fmt.Errorf("%s: %w", id, err))
						}
						break
					}
				}
				m.fanDebugTakenOver[id] = value
				entry.LastRamp = now
				if value >= fanDebugUnitMax(unit) {
					entry.Done = true
					entry.Running = false
				} else {
					anyRunning = true
				}
				m.fanDebugAuto.entries[id] = entry
			}
			if len(errs) > 0 {
				m.fanDebugLastError = errors.Join(errs...).Error()
			}
		}
		if !anyRunning {
			m.fanDebugAutoLoopRunning = false
			m.mu.Unlock()
			return
		}
		m.mu.Unlock()
	}
}

// stopFanDebugAutoLocked 停止全部风扇的自动递增,保持当前转速。
func (m *Manager) stopFanDebugAutoLocked() {
	if m.fanDebugAuto != nil {
		for id, entry := range m.fanDebugAuto.entries {
			entry.Running = false
			m.fanDebugAuto.entries[id] = entry
		}
	}
}

// StartFanDebugAutoBatch 批量启动多个风扇的自动递增(条目须已接管且参数合法,
// 否则整批报错不启动)。
func (m *Manager) StartFanDebugAutoBatch(entries map[string]fanDebugAutoEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fanDebugAuto == nil {
		m.fanDebugAuto = &fanDebugAutoTest{entries: map[string]fanDebugAutoEntry{}}
	}
	if m.fanDebugAuto.entries == nil {
		m.fanDebugAuto.entries = map[string]fanDebugAutoEntry{}
	}
	for id, entry := range entries {
		if _, taken := m.fanDebugTakenOver[id]; !taken {
			return fmt.Errorf("风扇 %s 未被接管,请先勾选接管", id)
		}
		if entry.Step < 1 || entry.Step > fanDebugUnitMax(entry.Unit) {
			return fmt.Errorf("风扇 %s 的递增转速超出该单位上限", id)
		}
		if entry.Interval < 1 || entry.Interval > 120 {
			return fmt.Errorf("风扇 %s 的递增间隔需在 1–120 秒之间", id)
		}
	}
	m.stopFanDebugAutoLocked()
	now := time.Now()
	cleaned := make(map[string]fanDebugAutoEntry, len(entries))
	for id, entry := range entries {
		entry.LastRamp = now
		entry.Running = true
		cleaned[id] = entry
	}
	m.fanDebugAuto.entries = cleaned
	m.ensureFanDebugAutoLoop()
	return nil
}

// StopFanDebugAuto 停止自动递增测试，保持当前转速。
func (m *Manager) StopFanDebugAuto() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopFanDebugAutoLocked()
}

type FanDebugFan struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Channel      int    `json:"channel"`
	RPM          int64  `json:"rpm"`
	PWMPercent   int    `json:"pwm_percent"`
	Mode         int64  `json:"mode"`
	TakenOver    bool   `json:"taken_over"`
	DebugPercent int    `json:"debug_percent,omitempty"`
	DebugUnit    string `json:"debug_unit,omitempty"`    // 该风扇调试值的单位(percent/pwm),未设置按 percent
	AutoStep     int    `json:"auto_step,omitempty"`     // 自动递增:每 Interval 秒 +Step
	AutoInterval int    `json:"auto_interval,omitempty"` // 自动递增间隔(秒)
	AutoRunning  bool   `json:"auto_running"`            // 自动递增进行中
	AutoDone     bool   `json:"auto_done,omitempty"`     // 已到该单位上限
}

type FanDebugState struct {
	Emergency   bool          `json:"emergency"`
	AutoRunning bool          `json:"auto_running"` // 任一风扇的自动递增进行中
	LastError   string        `json:"last_error,omitempty"`
	Fans        []FanDebugFan `json:"fans"`
}

// FanDebugState 汇总调试状态与全部已发现风扇（按接口枚举，0 转也列出）。
func (m *Manager) FanDebugState() FanDebugState {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := FanDebugState{
		Emergency: m.fanDebugEmergency,
		LastError: m.fanDebugLastError,
		Fans:      []FanDebugFan{},
	}
	_ = m.fanDebugAuto
	fans, err := m.DiscoverFans()
	if err != nil {
		state.LastError = err.Error()
		return state
	}
	for _, fan := range fans {
		item := FanDebugFan{
			ID:         fan.ID,
			Name:       fan.Name,
			Channel:    fan.Channel,
			RPM:        fan.RPM,
			PWMPercent: pwmToPercent(fan.PWM),
			Mode:       fan.Mode,
		}
		if m.fanDebugAuto != nil {
			if entry, ok := m.fanDebugAuto.entries[fan.ID]; ok {
				item.AutoStep = entry.Step
				item.AutoInterval = entry.Interval
				item.AutoRunning = entry.Running
				item.AutoDone = entry.Done
				if entry.Running {
					state.AutoRunning = true
				}
			}
		}
		if unit, ok := m.fanDebugUnits[fan.ID]; ok {
			item.DebugUnit = unit
		}
		if percent, ok := m.fanDebugTakenOver[fan.ID]; ok {
			item.TakenOver = true
			item.DebugPercent = percent
		}
		state.Fans = append(state.Fans, item)
	}
	return state
}
