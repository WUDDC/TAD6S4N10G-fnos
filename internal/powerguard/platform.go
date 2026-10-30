package powerguard

import (
	"os"
	"path/filepath"
	"strings"
)

// 平台标识常量：进入 /api/status 的 platform 字段，前端按它切换平台相关提示
// （fnOS 的 DKMS 自装指引 vs 群晖 DSM 的引导加载器/驱动盘指引）。
const (
	PlatformFnOS    = "fnos"
	PlatformDSM     = "dsm"
	PlatformGeneric = "generic"
)

// DetectPlatform 按宿主系统特征识别运行平台。fnOS 与 DSM 的判定文件都只在
// 各自系统上存在，顺序无所谓；两者都不命中时返回 generic（Debian TUI、
// 未知宿主等）。root 参数供测试注入替身系统根。
func DetectPlatform(root string) string {
	join := func(rel string) string {
		if root == "" || root == "/" {
			return rel
		}
		return filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(rel, "/")))
	}
	// DSM：/etc/VERSION 是 key=value 风格（productversion、unique 等），
	// /etc/synoinfo.conf 与 /usr/syno 也只在 DSM 上出现。
	if isDSMVersionFile(join("/etc/VERSION")) ||
		fileExists(join("/etc/synoinfo.conf")) ||
		dirExists(join("/usr/syno")) {
		return PlatformDSM
	}
	// fnOS：/etc/issue 的 "OS version: fnOS x.y" 或专用 release 标记文件。
	if data, err := os.ReadFile(join("/etc/issue")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "OS version:"))
			if strings.HasPrefix(strings.ToLower(value), "fnos") {
				return PlatformFnOS
			}
		}
	}
	for _, rel := range []string{"/etc/fnos-release", "/etc/fnos_version"} {
		if fileExists(join(rel)) {
			return PlatformFnOS
		}
	}
	return PlatformGeneric
}

// isDSMVersionFile 宽松解析 /etc/VERSION：命中 productversion 或 unique 键
// 即视为 DSM（不同 DSM 版本的键集合有差异，不做更严格的版本比对）。
func isDSMVersionFile(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		key, _, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "productversion", "unique", "buildnumber", "smallfixnumber":
			return true
		}
	}
	return false
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
