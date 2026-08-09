# N100/N305 功耗卫士（fnOS）

这是一个面向飞牛 fnOS 的第三方应用，通过 Linux Intel RAPL 接口限制 Intel Processor N100 与 Intel Core i3-N305 的 Package 功耗。它用于缓解 CPU 与核显共同负载时温度快速升高的问题。

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

## 安全行为

- 首次应用限制前，将每个 `package-*` 的原始 PL1/PL2 保存到 fnOS 的持久化应用数据目录。
- 写入后立即回读校验；任一 Package 写入失败时尝试回滚本轮修改。
- 每 5–300 秒校验并重应用，处理休眠恢复或系统服务覆盖功耗限制的情况。
- 在应用中心停止或卸载时恢复首次安装前捕获的限制。
- 不修改 BIOS、内核、GRUB、飞牛系统服务或飞牛温度告警配置。
- Web 后端只监听应用目录中的 Unix Socket；修改接口要求 fnOS 网关注入管理员身份。

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

生命周期脚本可使用 `bash -n cmd/* scripts/build.sh` 做语法检查。实际 RAPL 写入必须在 N100/N305 fnOS 主机上由 root 应用生命周期执行；开发机测试不会写主机功耗配置。

## 重要限制

降低 PL1/PL2 会牺牲部分峰值性能。不同主板 BIOS 可能锁定 RAPL，若写入被拒绝或回读值不一致，插件会报告错误而不会伪装成功。N100/N305 之外的处理器必须经过单独验证后才能加入支持列表。
