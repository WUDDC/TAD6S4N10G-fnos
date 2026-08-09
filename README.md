# N100/N305 功耗与风扇卫士（fnOS）

这是一个面向飞牛 fnOS 的第三方应用，通过 Linux Intel RAPL 接口限制 Intel Processor N100 与 Intel Core i3-N305 的 Package 功耗，并可通过 IT87 hwmon 接口按 CPU 真实温度控制单风扇。它用于缓解 CPU 与核显共同负载时温度快速升高的问题。

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

## 单风扇温控曲线

风扇功能默认关闭。安装并成功加载 [fnos-it87-kmod](https://github.com/IamAyang233/fnos-it87-kmod) 等兼容的 IT87 驱动后，插件只列出同时具有 `fanN_input`、`pwmN` 和 `pwmN_enable`，且转速大于 0 RPM 的通道。单风扇机器会自动选中唯一有转速反馈的通道，不会写入未接线的风扇接口。

默认曲线为 40°C/60%、55°C/70%、70°C/85%、80°C/100%，紧急满速温度为 85°C，采样间隔为 2 秒。60% 的保守下限用于单风扇 NAS，避免手动 PWM 模式下机箱与硬盘风量低于 BIOS 自动模式。控制温度取所有 `coretemp` 节点中的最高值，不读取无标签的 IT87 温度，也不会把 CPU 与 GPU 温度相加。

插件只使用驱动已暴露的标准 hwmon 节点，不会自动加载内核模块、设置 `force_id` 或修改驱动参数。驱动安装和硬件识别仍由用户负责。

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

降低 PL1/PL2 会牺牲部分峰值性能。不同主板 BIOS 可能锁定 RAPL 或 IT87 PWM，若写入被拒绝或回读值不一致，插件会报告错误而不会伪装成功。N100/N305 之外的处理器及多风扇主板必须经过单独验证后才能加入支持范围。
