#!/usr/bin/env bash
# 两仓配套验证：必须明确提供 UI checkout，不将缺失集成测试视作通过。
set -euo pipefail
bridge_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
: "${PI_WEBUI_DIR:?请将 PI_WEBUI_DIR 指向配套的 pi-webui-htmx checkout}"
ui_dir="$(cd -- "$PI_WEBUI_DIR" && pwd)"
export PI_WEBUI_DIR="$ui_dir"
cd -- "$bridge_dir"
# 记录两边实际 revision 及工作树，便于复现；没有声明不存在的远程仓库地址。
printf 'Bridge revision: '
git rev-parse HEAD
git status --short
printf 'UI revision: '
git -C "$ui_dir" rev-parse HEAD
git -C "$ui_dir" status --short
pnpm --dir "$ui_dir" install --frozen-lockfile
pnpm --dir "$ui_dir" test
pnpm --dir "$ui_dir" typecheck
pnpm --dir "$ui_dir" build
pnpm --dir "$ui_dir" check
test -z "$(gofmt -l .)"
go vet ./...
go test -race -count=1 ./...
