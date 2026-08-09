#!/usr/bin/env bash
set -euo pipefail

PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="$(sed -n 's/^version=//p' "${PROJECT_ROOT}/manifest" | head -1 | tr -d '"')"
FNPACK="${FNPACK:-fnpack}"

mkdir -p "${PROJECT_ROOT}/app/bin" "${PROJECT_ROOT}/app/ui/images" "${PROJECT_ROOT}/.cache/go-build" "${PROJECT_ROOT}/.cache/go-tmp"

cd "${PROJECT_ROOT}"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
GOCACHE="${PROJECT_ROOT}/.cache/go-build" GOTMPDIR="${PROJECT_ROOT}/.cache/go-tmp" \
go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o app/bin/tad-module ./backend/powerguard

GOCACHE="${PROJECT_ROOT}/.cache/go-build" GOTMPDIR="${PROJECT_ROOT}/.cache/go-tmp" \
go run ./tools/icon-gen -out app/ui/images
cp app/ui/images/icon_64.png ICON.PNG
cp app/ui/images/icon_256.png ICON_256.PNG

if [[ "${1:-}" != "--skip-package" ]]; then
  PACK_OUTPUT="$("${FNPACK}" build 2>&1)"
  printf '%s\n' "${PACK_OUTPUT}"
  if grep -q "Packing failed" <<<"${PACK_OUTPUT}" || ! compgen -G "${PROJECT_ROOT}/*.fpk" >/dev/null; then
    echo "fnpack build failed" >&2
    exit 1
  fi
fi
