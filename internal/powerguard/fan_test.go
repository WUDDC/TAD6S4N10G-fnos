package powerguard

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultFanConfigIsSafeAndDisabled(t *testing.T) {
	cfg := DefaultFanConfig()
	if cfg.Enabled {
		t.Fatal("fan control must be disabled by default")
	}
	if cfg.MinPWMPercent != 60 || cfg.EmergencyTempC != 85 || cfg.PollSeconds != 2 {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if len(cfg.Curve) != 4 || cfg.Curve[len(cfg.Curve)-1].PWMPercent != 100 {
		t.Fatalf("unexpected default curve: %+v", cfg.Curve)
	}
	if len(cfg.DiskCurve) != 3 || cfg.DiskCurve[0].TempC != 25 || cfg.DiskCurve[0].PWMPercent != 60 || cfg.DiskCurve[1].TempC != 35 || cfg.DiskCurve[1].PWMPercent != 85 || cfg.DiskCurve[2].TempC != 50 || cfg.DiskCurve[2].PWMPercent != 100 {
		t.Fatalf("unexpected default disk curve: %+v", cfg.DiskCurve)
	}
}

func TestInterpolatePWMPercent(t *testing.T) {
	curve := DefaultFanConfig().Curve
	tests := []struct {
		temp float64
		want int
	}{
		{30, 60},
		{40, 60},
		{47.5, 65},
		{55, 70},
		{70, 85},
		{80, 100},
		{85, 100},
	}
	for _, test := range tests {
		if got := interpolatePWMPercent(curve, test.temp, 60, 85); got != test.want {
			t.Errorf("temperature %.1f: got %d%%, want %d%%", test.temp, got, test.want)
		}
	}
}

func TestDiscoverFansRequiresMatchingRPMAndPWMNodes(t *testing.T) {
	root := t.TempDir()
	hwmon := filepath.Join(root, "sys", "class", "hwmon", "hwmon7")
	if err := os.MkdirAll(hwmon, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestValue(t, filepath.Join(hwmon, "name"), "it8613")
	for name, value := range map[string]string{
		"fan2_input": "0", "pwm2": "26", "pwm2_enable": "2",
		"fan3_input": "1662", "pwm3": "80", "pwm3_enable": "2",
		"fan4_input": "900",
	} {
		writeTestValue(t, filepath.Join(hwmon, name), value)
	}

	fans, err := (&Manager{Root: root}).DiscoverFans()
	if err != nil {
		t.Fatal(err)
	}
	if len(fans) != 2 {
		t.Fatalf("got %d complete fan channels, want 2: %+v", len(fans), fans)
	}
	if fans[1].ID != "it8613:hwmon7:fan3" || fans[1].RPM != 1662 || fans[1].Channel != 3 {
		t.Fatalf("unexpected spinning fan: %+v", fans[1])
	}
}

func TestSetFanPWMAndRestore(t *testing.T) {
	dir := t.TempDir()
	pwm := filepath.Join(dir, "pwm3")
	enable := filepath.Join(dir, "pwm3_enable")
	writeTestValue(t, pwm, "80")
	writeTestValue(t, enable, "2")
	fan := FanDevice{ID: "it8613:test:fan3", Channel: 3, PWMPath: pwm, EnablePath: enable}

	if err := setFanPWM(fan, 50); err != nil {
		t.Fatal(err)
	}
	if got, _ := readInt(pwm); got != 128 {
		t.Fatalf("pwm=%d, want 128", got)
	}
	if got, _ := readInt(enable); got != 1 {
		t.Fatalf("mode=%d, want manual mode 1", got)
	}
	if err := restoreFan(fan, originalFan{ID: fan.ID, PWM: 80, Mode: 2}); err != nil {
		t.Fatal(err)
	}
	if got, _ := readInt(pwm); got != 80 {
		t.Fatalf("restored pwm=%d, want 80", got)
	}
	if got, _ := readInt(enable); got != 2 {
		t.Fatalf("restored mode=%d, want automatic mode 2", got)
	}
}

func TestSetFanPWMLeavesDriverFullSpeedMode(t *testing.T) {
	dir := t.TempDir()
	pwm := filepath.Join(dir, "pwm3")
	enable := filepath.Join(dir, "pwm3_enable")
	writeTestValue(t, pwm, "255")
	writeTestValue(t, enable, "0")
	fan := FanDevice{ID: "it8613:test:fan3", Channel: 3, PWMPath: pwm, EnablePath: enable}

	if err := setFanPWM(fan, 70); err != nil {
		t.Fatal(err)
	}
	if got, _ := readInt(pwm); got != 179 {
		t.Fatalf("pwm=%d, want 179", got)
	}
	if got, _ := readInt(enable); got != 1 {
		t.Fatalf("mode=%d, want manual mode 1", got)
	}

	writeTestValue(t, pwm, "255")
	writeTestValue(t, enable, "0")
	if err := setFanPWM(fan, 100); err != nil {
		t.Fatalf("already-full fan should be accepted: %v", err)
	}
}

func TestNormalizeConfigMigratesFanDefaults(t *testing.T) {
	cfg := Config{Enabled: true, PL1W: 15, PL2W: 15, ReapplySeconds: 30}
	if !normalizeConfig(&cfg) {
		t.Fatal("legacy config was not migrated")
	}
	if cfg.Fan.Enabled || cfg.Fan.MinPWMPercent != 60 || len(cfg.Fan.Curve) != 4 || len(cfg.Fan.DiskCurve) != 3 {
		t.Fatalf("unexpected migrated fan config: %+v", cfg.Fan)
	}
	if normalizeConfig(&cfg) {
		t.Fatal("normalized config was migrated a second time")
	}
}

func TestNormalizeConfigAddsDiskCurveWithoutReplacingCPUCurve(t *testing.T) {
	cfg := Config{Fan: DefaultFanConfig(), GPIO: DefaultGPIOConfig()}
	cfg.Fan.MinPWMPercent = 30
	cfg.Fan.Curve = []FanPoint{{TempC: 40, PWMPercent: 30}, {TempC: 70, PWMPercent: 90}}
	cfg.Fan.DiskCurve = nil
	if !normalizeConfig(&cfg) {
		t.Fatal("config without disk curve was not migrated")
	}
	if len(cfg.Fan.Curve) != 2 || cfg.Fan.Curve[0].PWMPercent != 30 {
		t.Fatalf("CPU curve was replaced during migration: %+v", cfg.Fan.Curve)
	}
	if len(cfg.Fan.DiskCurve) != 3 || cfg.Fan.DiskCurve[0].PWMPercent != 30 || cfg.Fan.DiskCurve[1].PWMPercent != 85 {
		t.Fatalf("disk curve did not inherit the configured minimum: %+v", cfg.Fan.DiskCurve)
	}
}

func TestFanTargetsUseHigherCurve(t *testing.T) {
	cfg := DefaultFanConfig()
	cfg.MinPWMPercent = 30
	cfg.Curve = []FanPoint{{TempC: 40, PWMPercent: 30}, {TempC: 80, PWMPercent: 100}}
	cfg.DiskCurve = defaultDiskFanCurve(cfg.MinPWMPercent)

	cpu, disk, target := fanTargets(cfg, 60, 25, true)
	if cpu != 65 || disk != 30 || target != 65 {
		t.Fatalf("CPU should control at a cool disk temperature: cpu=%d disk=%d target=%d", cpu, disk, target)
	}
	cpu, disk, target = fanTargets(cfg, 45, 35, true)
	if cpu != 39 || disk != 85 || target != 85 {
		t.Fatalf("disk should control: cpu=%d disk=%d target=%d", cpu, disk, target)
	}
	_, disk, target = fanTargets(cfg, 45, 50, true)
	if disk != 100 || target != 100 {
		t.Fatalf("disk at 50C should force full speed: disk=%d target=%d", disk, target)
	}
	_, disk, target = fanTargets(cfg, 45, 0, false)
	if disk != 0 || target != 39 {
		t.Fatalf("missing disk temperature should not affect target: disk=%d target=%d", disk, target)
	}
}

func TestMaximumStorageTemperature(t *testing.T) {
	temperature, available := maximumStorageTemperature(StorageStatus{Slots: []StorageSlot{
		{ID: "front-1", TemperatureC: 43},
		{ID: "front-2", TemperatureC: 0},
		{ID: "m2-1", TemperatureC: 58},
	}})
	if !available || temperature != 58 {
		t.Fatalf("temperature=%.1f available=%v, want 58 true", temperature, available)
	}
}

func TestFanValidationRejectsDecreasingSpeed(t *testing.T) {
	cfg := DefaultFanConfig()
	cfg.Curve[2].PWMPercent = 40
	if err := (&Manager{}).validateFanLocked(cfg); err == nil {
		t.Fatal("decreasing PWM curve was accepted")
	}
}

func TestFanValidationRejectsDecreasingDiskSpeed(t *testing.T) {
	cfg := DefaultFanConfig()
	cfg.DiskCurve[2].PWMPercent = 40
	if err := (&Manager{}).validateFanLocked(cfg); err == nil {
		t.Fatal("decreasing disk PWM curve was accepted")
	}
}

func writeTestValue(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}
