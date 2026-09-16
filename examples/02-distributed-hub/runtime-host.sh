#!/usr/bin/env bash
# 02 · runtime 主机:连接到 hub 并作为执行机注册。
# 用法: HUB=hub-host.example.com:7070 [ID=gpu-box-1] [CONCURRENCY=4] [TOKEN=secret] ./runtime-host.sh
set -euo pipefail
AMUX="${AMUX:-amux}"

if [ -z "${HUB:-}" ]; then
  echo "请设置 HUB=<hub 主机:port>，例如: HUB=hub-host.example.com:7070 ./runtime-host.sh" >&2
  exit 2
fi

say() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }

say "连接到 control $HUB 并启动本机 amux system"
"$AMUX" setup remote --hub "$HUB"

if [ -n "${TOKEN:-}" ]; then
  "$AMUX" system config set hub.token "$TOKEN"
fi
if [ -n "${ID:-}" ]; then
  say "设置 target id = $ID"
  "$AMUX" system config set runtime.id "$ID"
fi
if [ -n "${CONCURRENCY:-}" ]; then
  "$AMUX" system config set runtime.max_concurrency "$CONCURRENCY"
fi
# 可按需打标签供调度选址:  amux system config set runtime.labels.gpu true

"$AMUX" system restart

cat <<EOF

完成。回到 control 主机执行 'amux system target list' 应能看到本 target。
从任意配置了 hub 的机器:  amux task submit "..." --agent claude
EOF
