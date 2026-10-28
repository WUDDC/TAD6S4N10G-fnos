//go:build !windows

package powerguard

import (
	"errors"
	"fmt"
	"os"
)

// readCPUModelOS 非 Windows 平台读 /proc/cpuinfo 的 "model name" 行；
// Windows 实现见 cpu_model_windows.go。
func (m *Manager) readCPUModelOS() (string, error) {
	data, err := os.ReadFile(m.rooted("/proc/cpuinfo"))
	if err != nil {
		return "", fmt.Errorf("read /proc/cpuinfo: %w", err)
	}
	if model := parseProcCPUInfoModel(data); model != "" {
		return model, nil
	}
	return "", errors.New("CPU model name was not found in /proc/cpuinfo")
}
