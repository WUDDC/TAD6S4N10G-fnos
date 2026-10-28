# TAD6S4N 模块 Windows 版

把 TAD6S4N10G 机箱监控模块（同一套 Go 后端 + Web UI）原生跑在 Windows 上。
发布物是一个便携 zip：`tad-module.exe` + `web/` 静态资源 + 服务安装脚本，
不需要安装器，解压即用。

## 快速开始

1. 解压 zip 到一个**路径不含空格**的目录（推荐 `C:\tad-module`）。
2. 以管理员身份打开 PowerShell，进入该目录，执行：

   ```powershell
   .\install-service.ps1
   ```

3. 浏览器打开 <http://127.0.0.1:8123/>。

脚本做的事：`tad-module.exe svc install`（注册开机自启的 Windows 服务，
数据目录 `%ProgramData%\tad-module`，崩溃自动拉起）→ `svc start`。

## 手动运行（不装服务）

```powershell
.\tad-module.exe serve `
  --config C:\tad-module\data\config.json `
  --state  C:\tad-module\data\state.json `
  --web-root C:\tad-module\web `
  --http 127.0.0.1:8123 `
  --base-path / `
  --log C:\tad-module\data\tad-module.log
```

`--base-path /` 是 Windows 独立部署的约定（fnOS/DSM 嵌入模式各有固定前缀）。

## 服务管理

```powershell
.\tad-module.exe svc start    # 启动
.\tad-module.exe svc stop     # 停止（退出前恢复已捕获的功耗/风扇状态）
.\tad-module.exe svc remove   # 停止并卸载服务
```

`svc install` 也支持显式传 serve 参数（数据目录自定义时用），例如：

```powershell
.\tad-module.exe svc install --web-root C:\tad-module\web `
  --config D:\tad-data\config.json --state D:\tad-data\state.json `
  --http 127.0.0.1:8123 --base-path /
```

## 功能可用性矩阵

Windows 没有 Linux 的 sysfs/it87 驱动，硬件控制类功能按模块既有约定**明确降级**
（界面显示"需要在 Linux 上运行"，不影响其余功能）：

| 功能 | Windows 状态 | 说明 |
| --- | --- | --- |
| Web UI / 监控面板 | ✅ 完整 | 同一套静态资源 |
| 历史记录（SQLite） | ✅ 完整 | `%ProgramData%\tad-module\history.db`，导出 SQL/CSV 可用 |
| 配置 / 日志 / 调试报告 | ✅ 完整 | |
| 磁盘 SMART 温度 | ✅（装 smartmontools 后） | `winget install smartmontools`，smartctl 需在 PATH |
| USB 串口温度传感器 | ✅ | 设备路径填 COM 口（如 `COM3`） |
| USB HID 温度计（TEMPer） | ✅ | |
| 风扇控制（it87/PWM） | ❌ 仅 Linux | 无 Super I/O 驱动 |
| RAPL 功耗限制与读数 | ❌ 仅 Linux | 需要 MSR/sysfs |
| GPIO 按键 / SD 卡 / 存储插入提示 | ❌ 仅 Linux | |
| CRPS 电源监控（PMBus） | ❌ 仅 Linux | 需要 `/dev/port` 位拍 |
| 开关机计划（RTC 唤醒） | ❌ 仅 Linux | |
| 固件刷写（BIOS/网卡） | ❌ 仅 Linux | flashrom/MFT 工具链 |
| 备份（rsync） | ❌ 仅 Linux | |

CPU 型号从注册表读取，功耗档案仍只认 N100 / N150 / i3-N305——其它 CPU 上
`serve` 会报 `unsupported CPU` 退出。这是刻意的：模块的核心价值绑定了
TAD6S4N10G 的目标硬件。

## 网络与鉴权

- 默认只监听回环地址（`127.0.0.1`），本机浏览器直连，无鉴权负担。
- 要从局域网其它设备访问：`--http 0.0.0.0:8123` **必须**同时配
  `--auth-file`（HTTP Basic 凭据 JSON，非回环监听缺它拒绝启动）：

  ```json
  {"username": "admin", "password": "改成强密码"}
  ```

  并记得在 Windows 防火墙放行该端口。

## 从源码构建

```bash
scripts/build-windows.sh                      # 版本取 manifest
TAD_WIN_VERSION=0.0.1 scripts/build-windows.sh   # 测试包（固定 0.0.1）
```

本机无 Go 时自动用 Docker `golang:1.25` 交叉编译 windows/amd64，产出仓库根
`tad-module-windows-v<版本>.zip`（附 `.sha256`）。

## 已知限制

- 风扇调试、CRPS、固件等页面会显示平台降级提示，属预期行为。
- 服务以 LocalSystem 运行；数据目录、日志文件都归它所有，手动改文件先 `svc stop`。
