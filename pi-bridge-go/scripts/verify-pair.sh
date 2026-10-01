#!/usr/bin/env bash
# 跨目录配套验证：UI 包 + 桥一起跑完整门禁。
#
# 单仓布局下两目录固定相邻，PI_WEBUI_DIR 可省略（testutil 会自动推断）；
# 显式提供时按模块根解析相对路径。整组测试都不可用时是**失败**，不是通过。
set -euo pipefail

bridge_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
repo_root="$(cd -- "$bridge_dir/.." && pwd)"
ui_dir="${PI_WEBUI_DIR:-$repo_root/pi-webui-htmx}"
ui_dir="$(cd -- "$ui_dir" && pwd)"
export PI_WEBUI_DIR="$ui_dir"

cd -- "$bridge_dir"

# 记录实际 revision 与工作树，便于复现。
printf 'revision: '
git rev-parse HEAD
git status --short

# 前端：锁定安装 → 测试 → 类型 → 构建 → 契约/对比度。
pnpm --dir "$ui_dir" install --frozen-lockfile
pnpm --dir "$ui_dir" test
pnpm --dir "$ui_dir" typecheck
pnpm --dir "$ui_dir" build
pnpm --dir "$ui_dir" check

# 桥：格式 → vet → 竞态测试（跨目录断言此时必须真正执行，不得跳过）。
test -z "$(gofmt -l .)"
go vet ./...
go test -race -count=1 ./...

# 明确拒绝"静默跳过"：UI 包既然已提供，就不允许有跨目录测试被跳过。
if go test -v -count=1 ./internal/transport/ ./internal/presentation/ 2>&1 | grep -q '^--- SKIP'; then
  echo "失败：提供了 UI 包但仍有测试被跳过" >&2
  exit 1
fi

echo "跨目录验证通过（UI + 桥，含竞态）"
