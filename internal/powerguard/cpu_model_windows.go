//go:build windows

package powerguard

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows/registry"
)

// readCPUModelOS Windows 平台优先读注册表的 CPU 型号
// （HKLM\HARDWARE\DESCRIPTION\System\CentralProcessor\0\ProcessorNameString），
// 型号串与 Linux /proc/cpuinfo 一致（如 "Intel(R) Celeron(R) N100"），
// DetectProfile 可直接识别。测试注入 sys-root 时优先读替身根下的
// /proc/cpuinfo，与 Linux 行为对齐。
func (m *Manager) readCPUModelOS() (string, error) {
	if m.Root != "" && m.Root != "/" {
		if data, err := os.ReadFile(m.rooted("/proc/cpuinfo")); err == nil {
			if model := parseProcCPUInfoModel(data); model != "" {
				return model, nil
			}
		}
	}
	key, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`HARDWARE\DESCRIPTION\System\CentralProcessor\0`, registry.QUERY_VALUE)
	if err != nil {
		return "", fmt.Errorf("read CPU model from registry: %w", err)
	}
	defer key.Close()
	name, _, err := key.GetStringValue("ProcessorNameString")
	if err != nil {
		return "", fmt.Errorf("read CPU model from registry: %w", err)
	}
	model := strings.TrimSpace(name)
	if model == "" {
		return "", errors.New("CPU model name is empty in registry")
	}
	return model, nil
}
