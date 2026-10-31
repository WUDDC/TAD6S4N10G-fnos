#!/usr/bin/env bash
# 打 Windows 版便携 zip 包（tad-module-windows-v<版本>.zip）。
#
# 用法:
#   scripts/build-windows.sh                    # 版本取 manifest（正式包）
#   TAD_WIN_VERSION=0.0.1 scripts/build-windows.sh   # 测试包（约定 0.0.1）
#
# 产物布局:
#   tad-module.exe           windows/amd64 后端（自带 serve/svc 子命令）
#   web/                     静态 UI（app/ui/static，根路径伺服）
#   install-service.ps1      一键注册 Windows 服务（svc install 标准布局）
#   WINDOWS-README.md        部署说明与功能可用性矩阵
#
# 本机无 Go 时回落 docker golang:1.25 交叉编译（与 scripts/build.sh 同一套
# 编译参数，GOOS=windows）。产物：仓库根的
# tad-module-windows-v<版本>.zip 与 .sha256。
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${TAD_WIN_VERSION:-$(sed -n 's/^version=//p' "${PROJECT_ROOT}/manifest" | head -1 | tr -d '"')}"
STAGE="${PROJECT_ROOT}/.cache/win-stage"
ZIP_NAME="tad-module-windows-v${VERSION}.zip"
# MSYS 下挂载串必须用 POSIX 风格（//c/...，无盘符冒号）——带 C: 冒号的写法
# 会被 Docker 的 host:container 分隔符解析破坏（invalid mode: /src）
DOCKER_SRC="/${PROJECT_ROOT}"

# 测试包（0.0.1）给静态资源加时间戳，强制浏览器拉新（与 build.sh 同规则）
CACHE_VERSION="${VERSION}"
if [[ "${VERSION}" =~ ^0\.0\.[0-9]+$ ]]; then
  CACHE_VERSION="${VERSION}-$(date -u +%Y%m%d%H%M%S)"
fi

mkdir -p "${PROJECT_ROOT}/.cache/go-build" "${PROJECT_ROOT}/.cache/go-tmp" "${STAGE}"
cd "${PROJECT_ROOT}"

if command -v go >/dev/null 2>&1; then
  export GOCACHE="${PROJECT_ROOT}/.cache/go-build" GOTMPDIR="${PROJECT_ROOT}/.cache/go-tmp"
  GO() { GOOS=windows GOARCH=amd64 go "$@"; }
else
  echo "==> 本机无 go，使用 docker golang:1.25 交叉编译 windows/amd64"
  GO() {
    MSYS_NO_PATHCONV=1 docker run --rm \
      -v "${DOCKER_SRC}:/src" -w /src \
      -e GOCACHE=/src/.cache/go-build -e GOTMPDIR=/src/.cache/go-tmp \
      -e GOMODCACHE=/src/.cache/gomod \
      -e GOOS=windows -e GOARCH=amd64 \
      golang:1.25 go "$@"
  }
fi

echo "==> 编译 Windows 后端（版本 ${VERSION}）"
rm -rf "${STAGE}/payload" "${STAGE}/root"
mkdir -p "${STAGE}/payload"
# -o 必须用相对路径（与 build-spk.sh 同理）：MSYS 绝对路径 /c/... 在容器内
# 会被当成容器自身目录树创建写入，--rm 后产物消失
GO build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
  -o .cache/win-stage/payload/tad-module.exe ./backend/powerguard
if [[ "$(head -c 2 "${STAGE}/payload/tad-module.exe" | od -An -c | tr -d ' \n')" != "MZ" ]]; then
  echo "产物不是 Windows PE（缺 MZ 头）" >&2
  exit 1
fi

echo "==> 组装载荷（web 静态资源 + 服务安装脚本 + 说明）"
mkdir -p "${STAGE}/payload/web"
cp -r "${PROJECT_ROOT}/app/ui/static/." "${STAGE}/payload/web/"
# 测试包给静态资源打时间戳，不改动工作区文件（与 build-spk.sh 同规则）
sed -i "s/styles\.css?v=[^\"]*/styles.css?v=${CACHE_VERSION}/g; s/app\.js?v=[^\"]*/app\.js?v=${CACHE_VERSION}/g" \
  "${STAGE}/payload/web/index.html"
cp "${PROJECT_ROOT}/scripts/windows/install-service.ps1" "${STAGE}/payload/install-service.ps1"
# MSYS 上写出的脚本可能是 CRLF/UTF-8 BOM 混杂，PowerShell 5.1 两者都吃，无需转换
cp "${PROJECT_ROOT}/docs/windows.md" "${STAGE}/payload/WINDOWS-README.md"

echo "==> 压缩 zip（PowerShell Compress-Archive）"
rm -f "${PROJECT_ROOT}/${ZIP_NAME}" "${PROJECT_ROOT}/${ZIP_NAME}.sha256"
PAYLOAD_WIN="$(cygpath -w "${STAGE}/payload")"
ZIP_WIN="$(cygpath -w "${PROJECT_ROOT}/${ZIP_NAME}")"
# 不用 Compress-Archive：其依赖的 Microsoft.PowerShell.Archive 模块在部分
# 机器上加载 ZipArchiveMode 失败；直接走 .NET ZipFile 稳定（目录内容落 zip 根）
powershell.exe -NoProfile -Command "\$ErrorActionPreference='Stop'; Add-Type -AssemblyName 'System.IO.Compression.FileSystem'; [System.IO.Compression.ZipFile]::CreateFromDirectory('${PAYLOAD_WIN}', '${ZIP_WIN}')"
sha256sum "${PROJECT_ROOT}/${ZIP_NAME}" > "${PROJECT_ROOT}/${ZIP_NAME}.sha256"

echo "==> 完成: ${ZIP_NAME} (+ .sha256)"
unzip -l "${ZIP_NAME}" 2>/dev/null | head -12 || powershell.exe -NoProfile -Command "(Get-Item '${ZIP_WIN}').Length"
