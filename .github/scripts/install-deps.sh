#!/usr/bin/env bash
# 安装 package.json 声明的 pnpm（packageManager 字段）与项目依赖。
# 工作目录必须是 pi-webui-htmx（脚本按相对路径读 package.json）。
#
# 为什么需要重试：
# 首次运行时「安装锁定的包管理器与依赖」以退出码 254 失败，而**同一条命令**
# 在同一次 push 触发的 Go 检查里 8 秒就成功了，重跑又直接通过——这是 runner
# 侧的瞬时网络故障，不是配置问题（lockfile 与 package.json 一致，本地从零
# 安装也通过）。所以：
#   1. 装 pnpm 时关掉与它无关的额外请求（--no-fund --no-audit）——audit 会额外
#      访问 registry，是这类瞬时失败的一个来源，而装一个包管理器不需要它；
#   2. 两个安装步骤各重试三次。npm 的全局安装与 pnpm 的依赖安装都是幂等的，
#      重试没有副作用。
#
# 只在安装阶段重试：workflow 里后续的测试步骤保持严格，失败一次就要红。
set -euo pipefail

log="${TMPDIR:-/tmp}/deps-install.log"

# annotate：把安装日志尾部写成 GitHub 注解。
# 失败详情在 Actions 日志里，而日志页面需要登录才能看；注解是公开可读的，
# 所以偶发失败时不必再靠猜或等复现。
annotate() {
  [ -n "${GITHUB_ACTIONS:-}" ] || return 0
  local tail_text
  tail_text="$(tail -c 1500 "$log" 2>/dev/null | sed ':a;N;$!ba;s/%/%25/g;s/\n/%0A/g' || true)"
  echo "::error title=$1::${tail_text:-（安装日志为空）}"
}

retry() {
  local label="$1"
  shift
  local attempt=1
  until "$@" 2>&1 | tee -a "$log"; do
    if [ "$attempt" -ge 3 ]; then
      echo "$label 失败，已重试 $attempt 次" >&2
      annotate "$label 失败"
      return 1
    fi
    echo "第 $attempt 次 $label 失败，3 秒后重试" >&2
    attempt=$((attempt + 1))
    sleep 3
  done
}

spec="$(node -p 'require("./package.json").packageManager')"
case "$spec" in
  ''|undefined)
    echo 'package.json 缺少 packageManager 字段，无法确定包管理器版本' >&2
    exit 1
    ;;
esac

: > "$log"
retry "安装 $spec" npm install --global --no-fund --no-audit "$spec"
echo "包管理器：pnpm $(pnpm --version)"
retry '安装依赖' pnpm install --frozen-lockfile
echo '依赖安装完成'
