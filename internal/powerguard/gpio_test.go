package powerguard

import (
	"context"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"
)

type fakeGPIOPort struct {
	values []byte
}

func (port *fakeGPIOPort) ReadAt(buffer []byte, offset int64) (int, error) {
	if offset < 0 || int(offset) >= len(port.values) {
		return 0, io.EOF
	}
	buffer[0] = port.values[offset]
	return 1, nil
}

func (port *fakeGPIOPort) set(spec gpioButtonSpec, high bool) {
	if high {
		port.values[spec.Port] |= 1 << spec.Bit
	} else {
		port.values[spec.Port] &^= 1 << spec.Bit
	}
}

func TestDefaultGPIOConfigIsDisabledAndSafe(t *testing.T) {
	config := DefaultGPIOConfig()
	if config.Enabled || config.Version != gpioConfigVersion || len(config.Buttons) != 3 {
		t.Fatalf("unexpected defaults: %+v", config)
	}
	if err := validateGPIOConfig(config); err != nil {
		t.Fatal(err)
	}
	for _, button := range config.Buttons {
		if button.Actions.Short != GPIOActionNone || button.Actions.Hold15S != GPIOActionNone {
			t.Fatalf("default action is not safe: %+v", button)
		}
	}
}

func TestGPIOValidationRejectsUnknownAction(t *testing.T) {
	config := DefaultGPIOConfig()
	config.Buttons[0].Actions.Short = "shell_command"
	if err := validateGPIOConfig(config); err == nil {
		t.Fatal("unknown GPIO action was accepted")
	}
}

func TestGPIOValidationAcceptsScriptBinding(t *testing.T) {
	config := DefaultGPIOConfig()
	config.Scripts = []GPIOScript{{ID: "backup-1", Name: "备份脚本", Body: "printf 'ok'\n"}}
	config.Buttons[0].Actions.Short = GPIOActionScriptPrefix + "backup-1"
	if err := validateGPIOConfig(config); err != nil {
		t.Fatal(err)
	}
}

func TestGPIOValidationRejectsInvalidScripts(t *testing.T) {
	valid := GPIOScript{ID: "script-1", Name: "测试脚本", Body: "true\n"}
	for _, test := range []struct {
		name   string
		mutate func(*GPIOConfig)
	}{
		{"path id", func(config *GPIOConfig) { config.Scripts[0].ID = "../bad" }},
		{"empty name", func(config *GPIOConfig) { config.Scripts[0].Name = "  " }},
		{"empty body", func(config *GPIOConfig) { config.Scripts[0].Body = "\n" }},
		{"missing binding", func(config *GPIOConfig) { config.Buttons[0].Actions.Short = GPIOActionScriptPrefix + "missing" }},
		{"duplicate id", func(config *GPIOConfig) { config.Scripts = append(config.Scripts, config.Scripts[0]) }},
		{"duplicate name", func(config *GPIOConfig) {
			config.Scripts = append(config.Scripts, GPIOScript{ID: "script-2", Name: " 测试脚本 ", Body: "true\n"})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := DefaultGPIOConfig()
			config.Scripts = []GPIOScript{valid}
			test.mutate(&config)
			if err := validateGPIOConfig(config); err == nil {
				t.Fatal("invalid GPIO script configuration was accepted")
			}
		})
	}
}

func TestGPIOPollDebouncesAndClassifiesRelease(t *testing.T) {
	config := DefaultGPIOConfig()
	config.Enabled = true
	config.Buttons[0].Actions.Short = GPIOActionLog
	port := &fakeGPIOPort{values: make([]byte, 0xA05)}
	for _, spec := range gpioButtonSpecs {
		port.set(spec, true)
	}
	manager := &Manager{}
	start := time.Unix(100, 0)
	if events, err := manager.PollGPIO(config, port, start); err != nil || len(events) != 0 {
		t.Fatalf("initial poll: events=%+v err=%v", events, err)
	}

	copyButton := gpioButtonSpecs[0]
	port.set(copyButton, false)
	_, _ = manager.PollGPIO(config, port, start.Add(10*time.Millisecond))
	_, _ = manager.PollGPIO(config, port, start.Add(120*time.Millisecond))
	port.set(copyButton, true)
	_, _ = manager.PollGPIO(config, port, start.Add(time.Second))
	events, err := manager.PollGPIO(config, port, start.Add(1120*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ButtonID != "copy" || events[0].Stage != "短按" || events[0].Action != GPIOActionLog {
		t.Fatalf("unexpected GPIO events: %+v", events)
	}
}

func TestGPIOActionThresholds(t *testing.T) {
	actions := GPIOActions{Short: "s", Hold3S: "3", Hold9S: "9", Hold15S: "15"}
	for _, test := range []struct {
		duration time.Duration
		want     string
	}{
		{2 * time.Second, "s"}, {3 * time.Second, "3"},
		{9 * time.Second, "9"}, {15 * time.Second, "15"},
	} {
		_, got := gpioActionForDuration(actions, test.duration)
		if got != test.want {
			t.Fatalf("duration %s got %q, want %q", test.duration, got, test.want)
		}
	}
}

func TestGPIOPollResolvesBoundScript(t *testing.T) {
	config := DefaultGPIOConfig()
	config.Enabled = true
	config.Scripts = []GPIOScript{{ID: "hello", Name: "问候", Body: "printf hello\n"}}
	config.Buttons[0].Actions.Short = GPIOActionScriptPrefix + "hello"
	port := &fakeGPIOPort{values: make([]byte, 0xA05)}
	for _, spec := range gpioButtonSpecs {
		port.set(spec, true)
	}
	manager := &Manager{}
	start := time.Unix(200, 0)
	_, _ = manager.PollGPIO(config, port, start)
	copyButton := gpioButtonSpecs[0]
	port.set(copyButton, false)
	_, _ = manager.PollGPIO(config, port, start.Add(10*time.Millisecond))
	_, _ = manager.PollGPIO(config, port, start.Add(120*time.Millisecond))
	port.set(copyButton, true)
	_, _ = manager.PollGPIO(config, port, start.Add(time.Second))
	events, err := manager.PollGPIO(config, port, start.Add(1120*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Script == nil || events[0].Script.ID != "hello" || events[0].Script.Body != "printf hello\n" {
		t.Fatalf("script was not resolved into GPIO event: %+v", events)
	}
}

func TestExecuteGPIOScriptUsesBashAndEventEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("/bin/bash execution is verified by Linux CI")
	}
	event := GPIOEvent{
		ButtonID: "copy", Stage: "短按", Duration: 1250 * time.Millisecond,
		Action: GPIOActionScriptPrefix + "env-test",
		Script: &GPIOScript{ID: "env-test", Name: "环境测试", Body: `printf '%s|%s|%s|%s' "$TAD_GPIO_BUTTON_ID" "$TAD_GPIO_STAGE" "$TAD_GPIO_DURATION_MS" "$TAD_GPIO_SCRIPT_NAME"`},
	}
	output, err := executeGPIOScript(context.Background(), event)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output, "copy|短按|1250|环境测试") {
		t.Fatalf("unexpected script output %q", output)
	}
}
