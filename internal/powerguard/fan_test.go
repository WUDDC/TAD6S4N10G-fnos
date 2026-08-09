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

func TestNormalizeConfigMigratesFanDefaults(t *testing.T) {
	cfg := Config{Enabled: true, PL1W: 15, PL2W: 15, ReapplySeconds: 30}
	if !normalizeConfig(&cfg) {
		t.Fatal("legacy config was not migrated")
	}
	if cfg.Fan.Enabled || cfg.Fan.MinPWMPercent != 60 || len(cfg.Fan.Curve) != 4 {
		t.Fatalf("unexpected migrated fan config: %+v", cfg.Fan)
	}
	if normalizeConfig(&cfg) {
		t.Fatal("normalized config was migrated a second time")
	}
}

func TestFanValidationRejectsDecreasingSpeed(t *testing.T) {
	cfg := DefaultFanConfig()
	cfg.Curve[2].PWMPercent = 40
	if err := (&Manager{}).validateFanLocked(cfg); err == nil {
		t.Fatal("decreasing PWM curve was accepted")
	}
}

func writeTestValue(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}
