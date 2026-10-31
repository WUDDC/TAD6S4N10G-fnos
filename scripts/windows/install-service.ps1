# TAD6S4N 模块（Windows 版）服务一键安装
# 用法: 管理员 PowerShell 中进入解压目录, 执行 .\install-service.ps1
# 卸载: .\tad-module.exe svc remove
# 详细说明见 WINDOWS-README.md

$ErrorActionPreference = 'Stop'
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8

$exe = Join-Path $PSScriptRoot 'tad-module.exe'
if (-not (Test-Path $exe)) { throw "未找到 $exe，请在解压目录内运行本脚本" }

# 幂等：装过旧服务先移除再装
if (Get-Service -Name 'tad-module' -ErrorAction SilentlyContinue) {
    & $exe svc remove
}

& $exe svc install --web-root (Join-Path $PSScriptRoot 'web')
& $exe svc start

Write-Host ''
Write-Host '安装完成，浏览器打开 http://127.0.0.1:8123/'
