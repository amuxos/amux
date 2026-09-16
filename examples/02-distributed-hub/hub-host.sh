#!/usr/bin/env bash
# 02 · control 主机:启动调度中心(control service + 本机 runtime)。
# 用法: [BIND=0.0.0.0:7070] ./hub-host.sh
set -euo pipefail
AMUX="${AMUX:-amux}"

say() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }

say "初始化 control profile(写 hub.addr 默认值并启动本机 amux system)"
"$AMUX" setup hub

# 可选:让 hub 对外可达 + 开启注册鉴权。
needs_restart=0
if [ -n "${BIND:-}" ]; then
	say "设置对外监听地址 $BIND"
	"$AMUX" system config set hub.addr "$BIND"
	needs_restart=1
fi
if [ -n "${TOKEN:-}" ]; then
	say "设置注册鉴权 token"
	"$AMUX" system config set hub.token "$TOKEN"
	needs_restart=1
fi
if [ "$needs_restart" -eq 1 ]; then
	"$AMUX" system restart
fi

say "已注册 target:"
"$AMUX" system target list

cat <<'EOF'

下一步:
  - 在每台执行机上:  HUB=<本机可达地址:port> ../02-distributed-hub/runtime-host.sh
  - 在本(hub)主机注册 Agent Pack: amux agent pack add <namespace> <git-url-or-local-git-repo>
EOF
