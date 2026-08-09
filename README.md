# N100/N305 温度、硬盘与功耗卫士（fnOS）

这是一个面向飞牛 fnOS 的第三方应用。它按 RR CPUinfo 的口径显示 Intel Processor N100 与 Intel Core i3-N305 的 CPU 核心最高温度，同时保留 Package 原始温度供核对；还可限制 Package 功耗、控制单风扇、显示 TAD6S4N10G 的硬盘物理仓位，并配置机箱 GPIO 按键映射。

配置界面分为温度、风扇控制、按键控制和硬盘槽位四个 Tab；每次只显示当前功能，窄屏下硬盘及按键信息改用卡片布局，不需要横向拖动表格。

## 下载与安装

从 [GitHub Releases](https://github.com/luodaoyi/TAD6S4N10G-fnos/releases) 下载最新的 `powerguard.fpk` 和 `powerguard.fpk.sha256`，校验 SHA256 后，在飞牛应用中心选择手动安装。

仓库的 GitHub Actions 会在每次提交时完成测试和打包；推送与 `manifest` 版本一致的 `v*` 标签时，会自动创建 Release 并附加插件包。

## 默认策略

| CPU | PL1（持续） | PL2（短时） | 可调 PL1 | 可调 PL2 |
| --- | ---: | ---: | ---: | ---: |
| N305 | 15 W | 15 W | 6–硬件上限 W | PL1–35 W |
| N100 | 6 W | 15 W | 4–硬件上限 W | PL1–25 W |

插件会读取 `constraint_*_max_power_uw`，当内核提供了有效上限时，界面和后端都会拒绝超过该上限的配置。只支持明确识别出的 N100/N305；其他 CPU 会拒绝安装或启动。

N305 默认将 PL2 与 PL1 同样限制为 15 W，避免 CPU 与核显联合负载在短时窗口内放行过高的 Package 功耗。

## 温度显示口径

在已验证的 fnOS 主机上，系统温度服务会枚举 `coretemp`，但只读取匹配到的首个 `temp*_input`；该节点是 `Package id 0`。飞牛资源监控再把这个 Package 热点作为 CPU 温度显示，并以 65°C/75°C 着色告警。Linux `coretemp` 本身读取 Intel 数字温度传感器，不会把 CPU 与 GPU 温度相加。

RR 的 CPUinfo 实现会筛选标签为 `Core N` 的温度并取最大值。本插件迁移相同口径：主卡片显示所有 `Core N` 中的最高温度，另一个卡片显示所有 `Package id N` 中的最高原始值，诊断区列出每个 `coretemp` 节点方便核对。若系统没有暴露任何 `Core N` 标签，才回退显示 Package 温度并明确标注。

插件不会修改飞牛的私有温度服务、资源监控前端或告警阈值，因此飞牛桌面原有的 Package 温度和红色告警仍会存在；请以插件中分开的两组读数判断二者差异。

## 单风扇温控曲线

风扇功能默认关闭。安装并成功加载 [fnos-it87-kmod](https://github.com/IamAyang233/fnos-it87-kmod) 等兼容的 IT87 驱动后，插件只列出同时具有 `fanN_input`、`pwmN` 和 `pwmN_enable`，且转速大于 0 RPM 的通道。单风扇机器会自动选中唯一有转速反馈的通道，不会写入未接线的风扇接口。

默认曲线为 40°C/60%、55°C/70%、70°C/85%、80°C/100%，紧急满速温度为 85°C，采样间隔为 2 秒。60% 的保守下限用于单风扇 NAS，避免手动 PWM 模式下机箱与硬盘风量低于 BIOS 自动模式。控制温度取所有 `coretemp` 节点中的最高值，不读取无标签的 IT87 温度，也不会把 CPU 与 GPU 温度相加。

曲线编辑器不再要求逐项填写温度和转速。直接拖动图上的圆点即可调整，节点会自动保持温度严格递增、转速不下降，并受最低转速和紧急满速温度约束。可以增加或删除节点，支持 2–8 个节点；鼠标、触屏和方向键均可操作。

插件只使用驱动已暴露的标准 hwmon 节点，不会自动加载内核模块、设置 `force_id` 或修改驱动参数。驱动安装和硬件识别仍由用户负责。

## 硬盘仓位表

插件通过稳定的 `/dev/disk/by-path` 路径映射 TAD6S4N10G 的物理仓位：前置 SATA 仓对应控制器 `0000:02:00.0` 的 ATA 端口 1–6，界面按机箱正面从左到右显示为 6、5、4、3、2、1；四个 M.2 仓依次对应 PCI 地址 `0000:04:00.0` 到 `0000:07:00.0`。

首版使用文字表格区分空置、已插入、已使用和告警，并显示 Linux 设备名、型号、序列号、容量、阵列/挂载用途、SMART 健康与温度。后台每 60 秒刷新一次，普通刷新对 SATA 使用 `smartctl -n standby`，不会为了读取健康状态强制唤醒休眠盘；按键动作中的“SMART 检查”会执行一次主动检查。仓位映射仅适用于该主板和背板，不能按设备名 `sda/sdb` 猜测物理位置。

## GPIO 按键映射

按键功能默认关闭。插件以 root 只读访问 `/dev/port`，每 100 ms 读取固定 GPIO 并进行 100 ms 消抖：复制按键为 `0xA04/bit6`，网络按键为 `0xA00/bit3`，后置重置按键为 `0xA03/bit6`。每个按键都能分别配置短按、长按 3 秒、长按 9 秒和长按 15 秒松开后的动作。

当前动作白名单只有：无动作、仅记录日志、刷新硬盘仓位、刷新仓位并检查 SMART、重新应用插件配置。插件不接受 shell 命令，也不实现尚未验证的飞牛私有 API，因此首版不会重置账户密码或改网络配置。

## 安全行为

- 首次应用限制前，将每个 `package-*` 的原始 PL1/PL2 保存到 fnOS 的持久化应用数据目录。
- 写入后立即回读校验；任一 Package 写入失败时尝试回滚本轮修改。
- 每 5–300 秒校验并重应用，处理休眠恢复或系统服务覆盖功耗限制的情况。
- 在应用中心停止或卸载时恢复首次安装前捕获的限制。
- 首次启用风扇曲线前单独保存原始 PWM 与控制模式；停止、卸载或关闭曲线时恢复 BIOS 自动模式。
- 温度读取失败、风扇转速丢失或 PWM 写入异常时，将已捕获的风扇通道切到 100% 作为故障保护。
- 兼容 IT87 驱动在 100% PWM 时自动切换到模式 0 的行为；温度回落后会先降低 PWM，再恢复手动曲线模式。
- 不修改 BIOS、内核、GRUB、飞牛系统服务、驱动参数或飞牛温度告警配置。
- Web 后端只监听 `root:www-data`、权限 `0660` 的 Unix Socket；修改接口还要求 fnOS 网关注入管理员身份。
- GPIO 端口地址和位号写死为已核对的硬件映射，用户只能选择动作，不能把界面改成任意 I/O 读写器。
- 仓位刷新在独立缓存中执行，不会让状态接口等待 `smartctl`；探测失败时显示“未知”，不会把读取错误伪装成空仓位。

## 构建

需要 Go 1.24+ 和官方 `fnpack`。

Windows PowerShell：

```powershell
./scripts/build.ps1
```

Linux：

```bash
./scripts/build.sh
```

仅生成 Linux 二进制和图标、不打 FPK：

```powershell
./scripts/build.ps1 -SkipPackage
```

构建完成后使用飞牛应用中心上传生成的 `.fpk`，或者在 NAS 上执行：

```bash
appcenter-cli install-fpk powerguard.fpk
```

## 开发验证

```powershell
$env:GOCACHE="$PWD/.cache/go-build"
$env:GOTMPDIR="$PWD/.cache/go-tmp"
New-Item -ItemType Directory -Force $env:GOCACHE,$env:GOTMPDIR | Out-Null
go test ./...
go vet ./...
```

生命周期脚本可使用 `bash -n cmd/* scripts/build.sh` 做语法检查。实际 RAPL/PWM 写入必须在 N100/N305 fnOS 主机上由 root 应用生命周期执行；开发机测试不会写主机功耗或风扇配置。

## 重要限制

降低 PL1/PL2 会牺牲部分峰值性能。不同主板 BIOS 可能锁定 RAPL 或 IT87 PWM，若写入被拒绝或回读值不一致，插件会报告错误而不会伪装成功。N100/N305 之外的处理器及多风扇主板必须经过单独验证后才能加入支持范围。硬盘仓位与 GPIO 映射针对 TAD6S4N10G 固定硬件设计；后续若支持其他机型，需要单独提供并验证映射表。
