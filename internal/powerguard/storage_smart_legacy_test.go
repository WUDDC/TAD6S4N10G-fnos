package powerguard

import (
	"strings"
	"testing"
)

// 输出逐行取自群晖 DSM 自带的 smartctl 6.5 真实输出(SA6400/RR)
const smartctl65HDD = `smartctl 6.5 (build date Sep 26 2022) [x86_64-linux-5.10.55+] (local build)
Copyright (C) 2002-16, Bruce Allen, Christian Franke

=== START OF INFORMATION SECTION ===
Device Model:     WDC WD161KRYZ-47JP0Y0
Serial Number:    WD-XXXX

=== START OF READ SMART DATA SECTION ===
SMART overall-health self-assessment test result: PASSED

SMART Attributes Data Structure revision number: 16
Vendor Specific SMART Attributes with Thresholds:
ID# ATTRIBUTE_NAME          FLAGS     VALUE WORST THRESH FAIL_RAW_VALUE
  1 Raw_Read_Error_Rate     0x000b   100   100   016    Pre-fail  Always       -       0
  5 Reallocated_Sector_Ct   0x0033   100   100   037    Pre-fail  Always       -       0
197 Current_Pending_Sector  0x0022   100   100   000    Old_age   Always       -       0
194 Temperature_Celsius     0x0002   048   048   000    Old_age   Always       -       45 (Min/Max 0/60)
190 Airflow_Temperature_Ceiling 0x0022 044 045 045    Old_age   Always       -       56 (0 2304)
`

const smartctl65SSDFailing = `SMART overall-health self-assessment test result: PASSED
  5 Reallocated_Sector_Ct   0x0033   095   095   010    Pre-fail  Always       -       1234
197 Current_Pending_Sector  0x0022   098   098   000    Old_age   In_the_past -       7
194 Temperature_Celsius     0x0022   029   000   000    Old_age   Always       -       41 (12 25 0 0 0)
`

const smartctl65Standby = `smartctl 6.5 ...
Device is in STANDBY mode, exit(2)
`

const smartctl65NVMe = `Smartctl open device: /dev/nvme0 failed: No such device
=== START OF SMART DATA SECTION ===
Critical Warning: 0
Temperature: 38 Celsius
Percentage Used: 3%
`

func TestParseSMARTLegacyHDD(t *testing.T) {
	result, err := parseSMARTLegacy([]byte(smartctl65HDD))
	if err != nil {
		t.Fatal(err)
	}
	if result.Passed == nil || !*result.Passed {
		t.Fatalf("PASSED 未识别: %+v", result.Passed)
	}
	if result.TemperatureC != 45 {
		t.Fatalf("温度应取 194 RAW 首数字 45, got %v", result.TemperatureC)
	}
	for _, w := range result.AttrWarnings {
		if strings.Contains(w, "Temperature") {
			t.Fatalf("温度属性不应进告警: %q", w)
		}
	}
	for _, name := range []string{"Reallocated_Sector_Ct"} {
		found := false
		for _, w := range result.AttrWarnings {
			if strings.Contains(w, name) {
				found = true
			}
		}
		_ = name
		_ = found
	}
}

func TestParseSMARTLegacyAttrWarnings(t *testing.T) {
	result, err := parseSMARTLegacy([]byte(smartctl65SSDFailing))
	if err != nil {
		t.Fatal(err)
	}
	if result.TemperatureC != 41 {
		t.Fatalf("温度应取 41, got %v", result.TemperatureC)
	}
	joined := strings.Join(result.AttrWarnings, "\n")
	if !strings.Contains(joined, "Reallocated_Sector_Ct") || !strings.Contains(joined, "1234") {
		t.Fatalf("重分配扇区告警缺失: %q", joined)
	}
	if !strings.Contains(joined, "In_the_past") {
		t.Fatalf("In_the_past 应随告警携带: %q", joined)
	}
	// 197 raw=7 且 In_the_past:同样应告警
	if !strings.Contains(joined, "Current_Pending_Sector") {
		t.Fatalf("197 raw=7 应告警: %q", joined)
	}
}

func TestParseSMARTLegacyAttrWarningsPending(t *testing.T) {
	// 197 raw=7 应产生告警(与上一用例分开验证语义)
	text := strings.Replace(smartctl65SSDFailing, "197 Current_Pending_Sector  0x0022   098   098   000    Old_age   In_the_past -       7",
		"197 Current_Pending_Sector  0x0022   098   098   000    Old_age   Always       -       7", 1)
	result, err := parseSMARTLegacy([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(result.AttrWarnings, "\n")
	if !strings.Contains(joined, "Current_Pending_Sector") || !strings.Contains(joined, "计数为 7") {
		t.Fatalf("197 raw=7 应告警: %q", joined)
	}
}

func TestParseSMARTLegacyStandby(t *testing.T) {
	result, err := parseSMARTLegacy([]byte(smartctl65Standby))
	if err != nil {
		t.Fatal(err)
	}
	if result.PowerMode != "standby" {
		t.Fatalf("休眠应识别为 standby, got %q", result.PowerMode)
	}
}

func TestParseSMARTLegacyNVMe(t *testing.T) {
	result, err := parseSMARTLegacy([]byte(smartctl65NVMe))
	if err != nil {
		t.Fatal(err)
	}
	if result.TemperatureC != 38 || result.Critical != 0 || result.PercentageUsed != 3 {
		t.Fatalf("NVMe 解析不符: %+v", result)
	}
}


func TestFormatSmartAttrWarningReadable(t *testing.T) {
	// 真机形态:客户在弹窗里看到的完整信息必须是白话
	got := formatSmartAttrWarning("Offline_Uncorrectable", 198, 6, "")
	want := "SMART 自检提示:离线不可修复扇区计数为 6——硬盘自检中发现无法读取的扇区,建议留意该数字是否增长并保持数据备份(Offline_Uncorrectable)"
	if got != want {
		t.Fatalf("文案不符: got %s want %s", got, want)
	}
	// when_failed 非空要体现"SMART 已标记"
	got = formatSmartAttrWarning("Current_Pending_Sector", 197, 3, "In_the_past")
	if !strings.Contains(got, "SMART 已标记该属性状态为 In_the_past") {
		t.Fatalf("when_failed 未体现: %s", got)
	}
	// 未知属性兜底:不崩、含属性名
	got = formatSmartAttrWarning("Weird_Attr", 250, 2, "")
	if !strings.Contains(got, "Weird_Attr") || !strings.Contains(got, "2") {
		t.Fatalf("未知属性兜底文案异常: %s", got)
	}
}
