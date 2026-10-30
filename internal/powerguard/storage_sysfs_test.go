package powerguard

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustSymlink(t *testing.T, link, target string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
}

func writeATAPortFixture(t *testing.T, root, ata, port string) {
	t.Helper()
	dir := filepath.Join(root, "sys/class/ata_port", ata)
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "port_no"), []byte(port+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

// 模拟群晖 DSM 宿主：/dev/disk 不存在，块设备由 syno 驱动以 sataN 命名。
func TestSysfsByPathEntriesDSM(t *testing.T) {
	root := t.TempDir()
	blockDir := filepath.Join(root, "sys", "block")
	mustSymlink(t, filepath.Join(blockDir, "sata1"),
		"../devices/pci0000:00/0000:00:1c.2/0000:02:00.0/ata1/host0/target0:0:0/0:0:0:0/block/sata1")
	mustSymlink(t, filepath.Join(blockDir, "sata2"),
		"../devices/pci0000:00/0000:00:1c.2/0000:02:00.0/ata2/host1/target1:0:0/1:0:0:0/block/sata2")
	mustSymlink(t, filepath.Join(blockDir, "nvme0n1"),
		"../devices/pci0000:00/0000:00:1c.0/0000:01:00.0/nvme/nvme0/nvme0n1")
	// 非 symlink / 过滤名单里的设备不应产生映射
	if err := os.MkdirAll(filepath.Join(blockDir, "ram0"), 0o755); err != nil {
		t.Fatal(err)
	}

	writeATAPortFixture(t, root, "ata1", "1")
	writeATAPortFixture(t, root, "ata2", "2")
	m := &Manager{Root: root}
	entries := m.sysfsByPathEntries()
	cases := map[string]string{
		"pci-0000:02:00.0-ata-1":   "sata1",
		"pci-0000:02:00.0-ata-1.0": "sata1",
		"pci-0000:02:00.0-ata-2":   "sata2",
		"pci-0000:01:00.0-nvme-0":  "nvme0n1",
		"pci-0000:01:00.0-nvme-1":  "nvme0n1",
	}
	for key, want := range cases {
		if got := entries[key]; got != want {
			t.Fatalf("entries[%q] = %q, want %q", key, got, want)
		}
	}
	if _, ok := entries["pci-0000:02:00.0-ata-3"]; ok {
		t.Fatal("空端口不应产生映射")
	}
	if _, ok := entries["ram0"]; ok {
		t.Fatal("ram 设备应被过滤")
	}
}

func TestResolveStorageDeviceViaSysfsFallback(t *testing.T) {
	root := t.TempDir()
	blockDir := filepath.Join(root, "sys", "block")
	mustSymlink(t, filepath.Join(blockDir, "sata1"),
		"../devices/pci0000:00/0000:00:1c.2/0000:02:00.0/ata1/host0/target0:0:0/0:0:0:0/block/sata1")
	mustSymlink(t, filepath.Join(blockDir, "nvme0n1"),
		"../devices/pci0000:00/0000:00:1c.0/0000:01:00.0/nvme/nvme0/nvme0n1")

	writeATAPortFixture(t, root, "ata1", "1")
	m := &Manager{Root: root}
	device, kname, err := m.resolveStorageDevice(storageSlotSpec{
		ID: "front-1", BusPath: "/dev/disk/by-path/pci-0000:02:00.0-ata-1",
	})
	if err != nil || device != "/dev/sata1" || kname != "sata1" {
		t.Fatalf("front slot fallback: %v %q %q", err, device, kname)
	}

	device, kname, err = m.resolveStorageDevice(storageSlotSpec{
		ID: "m2-1", Prefix: true, BusPath: "/dev/disk/by-path/pci-0000:01:00.0-nvme-",
	})
	if err != nil || device != "/dev/nvme0n1" || kname != "nvme0n1" {
		t.Fatalf("nvme slot fallback: %v %q %q", err, device, kname)
	}

	// 有 by-path 目录且链接存在时优先走原路径（行为不变）
	if err := os.MkdirAll(filepath.Join(root, "dev", "disk", "by-path"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, filepath.Join(root, "dev", "disk", "by-path", "pci-0000:02:00.0-ata-1"), "../../sda")
	if err := os.WriteFile(filepath.Join(root, "dev", "sda"), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	device, kname, err = m.resolveStorageDevice(storageSlotSpec{
		ID: "front-1", BusPath: "/dev/disk/by-path/pci-0000:02:00.0-ata-1",
	})
	if err != nil || device != "/dev/sda" || kname != "sda" {
		t.Fatalf("by-path takes precedence: %v %q %q", err, device, kname)
	}
}

func TestReadBlockTopologyFromProc(t *testing.T) {
	root := t.TempDir()
	procDir := filepath.Join(root, "proc")
	if err := os.MkdirAll(procDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mdstat := "Personalities : [raid1] [raid6]\n" +
		"md2 : active raid6 sata5p8[4] sata3p8[3] sata2p8[2] sata4p8[5]\n" +
		"      15623692288 blocks super 1.2 level 6, 64k chunk, algorithm 2 [4/4] [UUUU]\n" +
		"md0 : active raid1 sata2p1[0] sata1p1[1]\n" +
		"      2430176 blocks super 1.2\n"
	mounts := "/dev/md0 / ext4 rw 0 0\n" +
		"/dev/sata1 /volume1 ext4 rw 0 0\n"
	for name, content := range map[string]string{"mdstat": mdstat, "mounts": mounts} {
		if err := os.WriteFile(filepath.Join(procDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := &Manager{Root: root}
	blocks, reliable, err := m.readBlockTopologyFromProc()
	if err != nil || !reliable {
		t.Fatalf("拓扑应可靠: %v reliable=%v", err, reliable)
	}

	for _, kname := range []string{"sata1", "sata2", "sata3", "sata4", "sata5"} {
		info, ok := blocks[kname]
		if !ok || !info.Used {
			t.Fatalf("%s 应为在用: %+v", kname, info)
		}
		if len(info.Purposes) == 0 {
			t.Fatalf("%s 应有用途标注: %+v", kname, info)
		}
	}
	// sata2 同时是 RAID 成员（md0+md2 两条）,purposes 应去重
	raidCount := 0
	for _, purpose := range blocks["sata2"].Purposes {
		if purpose == "RAID 成员" {
			raidCount++
		}
	}
	if raidCount != 1 {
		t.Fatalf("RAID 成员应去重, got %d", raidCount)
	}
}

func TestTrimPartitionSuffix(t *testing.T) {
	cases := map[string]string{
		"sata5p8":   "sata5",
		"nvme0n1p2": "nvme0n1",
		"sda1":      "sda",
		"sda":       "sda",
		"sata1":     "sata1",
		"nvme0n1":   "nvme0n1",
	}
	for in, want := range cases {
		if got := trimPartitionSuffix(in); got != want {
			t.Fatalf("trimPartitionSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadBlockTopologyFromProcReadFailure(t *testing.T) {
	// PR#39 review:读不到 /proc 来源时必须报不可靠,不得静默得出"都没在用"
	root := t.TempDir()
	procDir := filepath.Join(root, "proc")
	if err := os.MkdirAll(procDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 只写 mdstat 不写 mounts
	if err := os.WriteFile(filepath.Join(procDir, "mdstat"), []byte("md2 : active raid6 sata5[4]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := &Manager{Root: root}
	blocks, reliable, err := m.readBlockTopologyFromProc()
	if err == nil || reliable {
		t.Fatalf("缺 mounts 应返回不可靠: %v reliable=%v", err, reliable)
	}
	if blocks != nil {
		t.Fatal("不可靠时不应给出部分结果")
	}
}

func TestReadBlockTopologyFromProcInactiveArrayMembers(t *testing.T) {
	// PR#39 review:inactive/degraded 阵列的成员同样是在用盘,不得漏判
	root := t.TempDir()
	procDir := filepath.Join(root, "proc")
	if err := os.MkdirAll(procDir, 0o755); err != nil {
		t.Fatal(err)
	}
	mdstat := "Personalities : [raid1]\n" +
		"md3 : inactive sata2p1[2](F) sata4p1[1]\n" +
		"      123456 blocks super 1.2\n"
	mounts := "/dev/md0 / ext4 rw 0 0\n"
	for name, content := range map[string]string{"mdstat": mdstat, "mounts": mounts} {
		if err := os.WriteFile(filepath.Join(procDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m := &Manager{Root: root}
	blocks, reliable, err := m.readBlockTopologyFromProc()
	if err != nil || !reliable {
		t.Fatalf("拓扑应可靠: %v reliable=%v", err, reliable)
	}
	for _, kname := range []string{"sata2", "sata4"} {
		info, ok := blocks[kname]
		if !ok || !info.Used {
			t.Fatalf("inactive 阵列成员 %s 应判在用: %+v", kname, info)
		}
	}
}

func TestSysfsATAPortUsesControllerLocalNumber(t *testing.T) {
	root := t.TempDir()
	for _, disk := range []struct{ kname, bdf, ata, port string }{
		{"sata1", "0000:02:00.0", "ata7", "1"},
		{"sata2", "0000:03:00.0", "ata8", "1"},
	} {
		mustSymlink(t, filepath.Join(root, "sys/block", disk.kname), "../devices/pci0000:00/"+disk.bdf+"/"+disk.ata+"/host0/target0:0:0/0:0:0:0/block/"+disk.kname)
		writeATAPortFixture(t, root, disk.ata, disk.port)
	}
	m := &Manager{Root: root}
	for _, bdf := range []string{"0000:02:00.0", "0000:03:00.0"} {
		name := "pci-" + bdf + "-ata-1"
		got := m.sysfsByPathEntries()[name]
		if got == "" {
			t.Fatalf("缺控制器本地端口: %v", m.sysfsByPathEntries())
		}
		if _, _, err := m.resolveStorageDevice(storageSlotSpec{BusPath: "/dev/disk/by-path/" + name}); err != nil {
			t.Fatal(err)
		}
	}
	if m.sysfsByPathEntries()["pci-0000:02:00.0-ata-7"] != "" {
		t.Fatal("不得使用全局 ATA 编号")
	}
}

func TestSysfsATAUnknownPortDoesNotInventSlot(t *testing.T) {
	for _, port := range []string{"missing", "0", "-1", "garbage", "99999999999999999999999999999999"} {
		t.Run(port, func(t *testing.T) {
			root := t.TempDir()
			mustSymlink(t, filepath.Join(root, "sys/block/sata1"), "../devices/pci0000:00/0000:02:00.0/ata7/host0/target0:0:0/0:0:0:0/block/sata1")
			// 保留另一个可识别 NVMe，覆盖部分控制器未知时的逐仓回退。
			mustSymlink(t, filepath.Join(root, "sys/block/nvme0n1"), "../devices/pci0000:00/0000:01:00.0/nvme/nvme0/nvme0n1")
			if port != "missing" {
				writeATAPortFixture(t, root, "ata7", port)
			}
			m := &Manager{Root: root}
			if len(m.sysfsByPathEntries()) != 2 {
				t.Fatalf("仅应留下 NVMe 映射: %v", m.sysfsByPathEntries())
			}
			_, _, err := m.resolveStorageDevice(storageSlotSpec{BusPath: "/dev/disk/by-path/pci-0000:02:00.0-ata-1"})
			if err == nil || errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "port_no") {
				t.Fatalf("必须明确未知映射，不能误报空仓: %v", err)
			}
		})
	}
}
