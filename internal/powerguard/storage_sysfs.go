package powerguard

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// 群晖 DSM 宿主兜底：DSM（含 RR 加载的 DSM）没有 udev 的 /dev/disk/by-path
// 链接，块设备由 syno 驱动以 sataN 命名（/dev/sataN、/sys/block/sataN）。
// 设备的 PCI BDF 与 ATA 端口号在 sysfs 设备符号链接目标里完整可得，据此合成
// 与 by-path 等价的映射，供既有的仓位发现/解析管线使用。
//
// 例（TAD6S4N10G 前置仓）：
//	/sys/block/sata1 -> ../devices/pci0000:00/0000:00:1c.2/0000:02:00.0/
//	                    ata1/host0/target0:0:0/0:0:0:0/block/sata1
//	合成 pci-0000:02:00.0-ata-1（与 -ata-1.0 两种 udev 命名）→ sata1

var (
	sysfsBDFRe      = regexp.MustCompile(`^[0-9a-fA-F]{4}:[0-9a-fA-F]{2}:[0-9a-fA-F]{2}\.[0-9a-fA-F]$`)
	sysfsAtaRe      = regexp.MustCompile(`^ata(\d+)$`)
	sysfsNvmeCtrlRe = regexp.MustCompile(`^nvme(\d+)$`)
)

// storageSysfsKnameRelevant 过滤 ram/loop/dm/md/zram/sr/synoboot 等非物理盘。
func storageSysfsKnameRelevant(kname string) bool {
	for _, prefix := range []string{"dm-", "md", "loop", "ram", "zram", "sr", "fd", "synoboot"} {
		if strings.HasPrefix(kname, prefix) {
			return false
		}
	}
	return strings.HasPrefix(kname, "sd") || strings.HasPrefix(kname, "sata") ||
		strings.HasPrefix(kname, "nvme") || strings.HasPrefix(kname, "hd")
}

// parseSysfsBlockPath 从 /sys/block 设备符号链接目标解析 PCI BDF（取最后一个
// pci 组件，即设备所属控制器）、ATA 端口号与 NVMe 控制器序号。
func parseSysfsBlockPath(target string) (bdf string, ataPort int, nvmeCtrl int) {
	ataPort, nvmeCtrl = 0, -1
	for _, component := range strings.Split(target, "/") {
		if sysfsBDFRe.MatchString(component) {
			bdf = component
			continue
		}
		if m := sysfsAtaRe.FindStringSubmatch(component); m != nil {
			if port, err := strconv.Atoi(m[1]); err == nil {
				ataPort = port
			}
			continue
		}
		if m := sysfsNvmeCtrlRe.FindStringSubmatch(component); m != nil {
			if ctrl, err := strconv.Atoi(m[1]); err == nil {
				nvmeCtrl = ctrl
			}
		}
	}
	return bdf, ataPort, nvmeCtrl
}

// sysfsByPathEntries 合成 by-path 名 → 块设备名映射。udev 命名规则：
// pci-<bdf>-ata-<端口>[.0]（SATA）、pci-<bdf>-nvme-<序号>（NVMe；Glob 前缀
// 匹配下序号不敏感，两种习惯都生成）。无 BDF 或端口信息的设备跳过。
func (m *Manager) sysfsByPathEntries() map[string]string {
	entries := map[string]string{}
	blockDir := m.rooted("/sys/block")
	names, err := os.ReadDir(blockDir)
	if err != nil {
		return entries
	}
	for _, item := range names {
		kname := item.Name()
		if !storageSysfsKnameRelevant(kname) {
			continue
		}
		target, err := os.Readlink(filepath.Join(blockDir, kname))
		if err != nil {
			continue
		}
		bdf, ataPort, nvmeCtrl := parseSysfsBlockPath(target)
		switch {
		case ataPort > 0 && bdf != "":
			bdf = strings.ToLower(bdf)
			entries[fmt.Sprintf("pci-%s-ata-%d", bdf, ataPort)] = kname
			entries[fmt.Sprintf("pci-%s-ata-%d.0", bdf, ataPort)] = kname
		case nvmeCtrl >= 0 && bdf != "":
			bdf = strings.ToLower(bdf)
			entries[fmt.Sprintf("pci-%s-nvme-%d", bdf, nvmeCtrl)] = kname
			entries[fmt.Sprintf("pci-%s-nvme-%d", bdf, nvmeCtrl+1)] = kname
		}
	}
	return entries
}

// sysfsByPathPrefix 按前缀匹配合成映射（NVMe 槽的 BusPath 以 -nvme- 结尾，
// 靠 Glob 通配解析；这里对合成映射做同样的前缀匹配）。
func (m *Manager) sysfsByPathPrefix(busPath string) (string, bool) {
	prefix := filepath.Base(busPath)
	for name, kname := range m.sysfsByPathEntries() {
		if strings.HasPrefix(name, prefix) {
			return kname, true
		}
	}
	return "", false
}

// trimPartitionSuffix 去掉块设备名里的分区后缀：sata5p8→sata5、nvme0n1p2→
// nvme0n1、sda1→sda；无分区的设备原样返回。
func trimPartitionSuffix(kname string) string {
	trimmed := strings.TrimRight(kname, "0123456789")
	if strings.HasSuffix(trimmed, "p") && (strings.HasPrefix(kname, "nvme") || strings.HasPrefix(kname, "sata")) {
		return strings.TrimSuffix(trimmed, "p")
	}
	if kname != trimmed && strings.HasPrefix(kname, "sd") {
		return trimmed
	}
	return kname
}

// readBlockTopologyFromProc 在没有 lsblk 的宿主（群晖 DSM）上，用 /proc/mdstat
// 与 /proc/mounts 推断各物理盘是否在用（RAID 成员/直接挂载）。型号、序列号与
// 容量由 collectStorage 的 sysfs 回退读取，这里只补「使用状态」维度。
func (m *Manager) readBlockTopologyFromProc() map[string]blockInfo {
	type entry struct {
		info     blockInfo
		purposes map[string]bool
	}
	result := map[string]*entry{}
	add := func(rawName, purpose string) {
		// mdstat 成员形如 sata5p8[4]:先剥 [N] 下标再裁分区后缀
		if idx := strings.IndexByte(rawName, '['); idx >= 0 {
			rawName = rawName[:idx]
		}
		kname := trimPartitionSuffix(rawName)
		if !storageSysfsKnameRelevant(kname) {
			return
		}
		item := result[kname]
		if item == nil {
			item = &entry{info: blockInfo{KName: kname}, purposes: map[string]bool{}}
			result[kname] = item
		}
		item.info.Used = true
		item.purposes[purpose] = true
	}
	if data, err := os.ReadFile(m.rooted("/proc/mdstat")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if !strings.Contains(line, "active") || !strings.Contains(line, "raid") {
				continue
			}
			for _, field := range strings.Fields(line) {
				add(trimPartitionSuffix(field), "RAID 成员")
			}
		}
	}
	if data, err := os.ReadFile(m.rooted("/proc/mounts")); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 || !strings.HasPrefix(fields[0], "/dev/") {
				continue
			}
			if fields[1] == "/" {
				add(trimPartitionSuffix(filepath.Base(fields[0])), "系统盘")
			} else {
				add(trimPartitionSuffix(filepath.Base(fields[0])), "挂载 "+fields[1])
			}
		}
	}
	out := map[string]blockInfo{}
	for kname, item := range result {
		sysfs := m.readBlockInfoFromSysfs(kname)
		item.info.Model, item.info.Serial, item.info.SizeBytes = sysfs.Model, sysfs.Serial, sysfs.SizeBytes
		for purpose := range item.purposes {
			item.info.Purposes = append(item.info.Purposes, purpose)
		}
		sort.Strings(item.info.Purposes)
		out[kname] = item.info
	}
	return out
}
