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
// 从设备符号链接目标取 PCI BDF 与 ATA 全局编号，再读 ata_port/ataN/port_no，合成
// 与 by-path 等价的映射，供既有的仓位发现/解析管线使用。
//
// 例（TAD6S4N10G 前置仓）：
//	/sys/block/sata1 -> ../devices/pci0000:00/0000:00:1c.2/0000:02:00.0/
//	                    ata1/host0/target0:0:0/0:0:0:0/block/sata1
//	若 ata1/port_no=1，合成 pci-0000:02:00.0-ata-1（与 -ata-1.0）→ sata1

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
// pci 组件，即设备所属控制器）、ATA 全局编号与 NVMe 控制器序号（ATA 本地端口另读 port_no）。
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
	entries, _ := m.sysfsStoragePaths()
	return entries
}

// sysfsStoragePaths 同时记录无法确定 ATA 本地端口的控制器；不能将其仓位误报为空。
func (m *Manager) sysfsStoragePaths() (map[string]string, map[string]bool) {
	entries := map[string]string{}
	unknown := map[string]bool{}
	blockDir := m.rooted("/sys/block")
	names, err := os.ReadDir(blockDir)
	if err != nil {
		return entries, unknown
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
			// ataN 是全局 print_id；udev by-path 用控制器内的 port_no（从 1 起）。
			data, err := os.ReadFile(m.rooted(fmt.Sprintf("/sys/class/ata_port/ata%d/port_no", ataPort)))
			port, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil || parseErr != nil || port <= 0 {
				unknown["pci-"+bdf+"-ata-"] = true
				continue
			}
			ataPort = port
			entries[fmt.Sprintf("pci-%s-ata-%d", bdf, ataPort)] = kname
			entries[fmt.Sprintf("pci-%s-ata-%d.0", bdf, ataPort)] = kname
		case nvmeCtrl >= 0 && bdf != "":
			bdf = strings.ToLower(bdf)
			entries[fmt.Sprintf("pci-%s-nvme-%d", bdf, nvmeCtrl)] = kname
			entries[fmt.Sprintf("pci-%s-nvme-%d", bdf, nvmeCtrl+1)] = kname
		}
	}
	return entries, unknown
}

func (m *Manager) sysfsATAUnmappedError(busPath string) error {
	_, unknown := m.sysfsStoragePaths()
	for prefix := range unknown {
		if strings.HasPrefix(filepath.Base(busPath), prefix) {
			return fmt.Errorf("无法确认 %s 的 ATA 端口映射（port_no 缺失或无效）", busPath)
		}
	}
	return nil
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
// 任一来源读不到时返回 reliable=false——"未在用"的判读随之作废,调用方须
// 按未知处理,不得把"无法判断"显示为空闲可用(PR#39 review)。
func (m *Manager) readBlockTopologyFromProc() (map[string]blockInfo, bool, error) {
	type entry struct {
		info     blockInfo
		purposes map[string]bool
	}
	result := map[string]*entry{}
	add := func(rawName, purpose string) {
		// mdstat 成员形如 sata5p8[4] / sata3p8[2](F):先剥 [N] 下标与 (F)
		// 故障标记再裁分区后缀
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
	mdstatData, mdstatErr := os.ReadFile(m.rooted("/proc/mdstat"))
	mountsData, mountsErr := os.ReadFile(m.rooted("/proc/mounts"))
	if mdstatErr != nil || mountsErr != nil {
		err := mdstatErr
		if err == nil {
			err = mountsErr
		}
		return nil, false, fmt.Errorf("读取存储拓扑(/proc): %w", err)
	}
	for _, line := range strings.Split(string(mdstatData), "\n") {
		// 匹配一切 md 阵列行(active/inactive/degraded 都算):成员盘照样
		// 挂在阵列下,漏掉 inactive 会员会把在用盘误判为空闲
		fields := strings.Fields(line)
		// 行格式: "md3 : inactive sata2p1[2](F) sata4p1[1]" —— 冒号是独立字段
		if len(fields) < 4 || !strings.HasPrefix(fields[0], "md") || fields[1] != ":" {
			continue
		}
		for _, field := range fields[2:] {
			add(field, "RAID 成员")
		}
	}
	for _, line := range strings.Split(string(mountsData), "\n") {
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
	return out, true, nil
}
