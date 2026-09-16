#!/usr/bin/env bash
# 02 · 单机演练完整 hub 路径(隔离 HOME + 随机端口,自动清理)。
# 起 amuxd(hub) + amuxlet(runtime),注册一个 Git-backed Agent Pack,
# 用 task-path profile 经 hub 提交 task,验证 hub 把 profile ref 解析为 base 类型并路由执行。
# 用法: ./local-hub-demo.sh
set -euo pipefail
AMUX="${AMUX:-amux}"

# 用短路径基目录:unix socket 路径有长度上限(macOS ~104 字节),
# 而 $TMPDIR 在 macOS 上很长,会让 .amux/sessions 下的 broker socket 超限。
HOME_DIR="$(mktemp -d "/tmp/amux-hub-demo.XXXXXX")"
export HOME="$HOME_DIR"
PORT=$(( 20000 + RANDOM % 20000 ))
TARGET="demo-runtime"
TASK_ID=""
say() { printf '\n\033[1m==> %s\033[0m\n' "$1"; }
demo_broker_pids() {
  ps -axo pid=,command= | while read -r pid cmdline; do
    case "$cmdline" in
      *"amuxlet _acp_broker --socket $HOME_DIR/.amux/sessions/"*|*"amuxlet _structured_broker"*"--socket $HOME_DIR/.amux/sessions/"*) printf '%s\n' "$pid" ;;
    esac
  done
}
kill_demo_brokers() {
  local pids
  pids="$(demo_broker_pids || true)"
  if [ -n "$pids" ]; then
    # shellcheck disable=SC2086 # pids are numeric tokens from ps output.
    kill -TERM $pids >/dev/null 2>&1 || true
    sleep 0.2
  fi
  pids="$(demo_broker_pids || true)"
  if [ -n "$pids" ]; then
    # shellcheck disable=SC2086 # pids are numeric tokens from ps output.
    kill -KILL $pids >/dev/null 2>&1 || true
  fi
}
cleanup() {
  if [ -n "$TASK_ID" ]; then
    "$AMUX" task cancel "$TASK_ID" >/dev/null 2>&1 || true
    sleep 0.2
  fi
  "$AMUX" runtime stop --force >/dev/null 2>&1 || true
  "$AMUX" hub stop --force     >/dev/null 2>&1 || true
  kill_demo_brokers
  rm -rf "$HOME_DIR"
}
trap cleanup EXIT

say "隔离 HOME=$HOME  hub=127.0.0.1:$PORT"

PACK_DIR="$HOME/demo-agent-pack"
mkdir -p "$PACK_DIR/profiles"
cat > "$PACK_DIR/profiles/reviewer.toml" <<'TOML'
name = "reviewer"
type = "codex"
description = "demo task-path profile backed by codex"
permission_policy = "policy-allow"

[placement.requires]
agent_types = ["codex"]
features = ["structured_provider"]
TOML
git -C "$PACK_DIR" init -q >/dev/null
git -C "$PACK_DIR" add .
git -C "$PACK_DIR" -c user.name='amux demo' -c user.email='amux-demo@example.invalid' commit -m 'add demo reviewer profile' >/dev/null

say "配置并启动本机 amux system"
"$AMUX" system config set hub.addr "127.0.0.1:$PORT" >/dev/null
"$AMUX" system config set runtime.id "$TARGET" >/dev/null
"$AMUX" system start

say "等待 runtime 注册到 control"
for _ in $(seq 1 30); do
  "$AMUX" system target list 2>/dev/null | grep -q "$TARGET" && break
  sleep 0.5
done
"$AMUX" system target list

say "注册并检查 hub 主机的 Agent Pack"
"$AMUX" agent pack add demo "$PACK_DIR" >/dev/null
"$AMUX" agent show demo/reviewer

say "经 hub 提交 task --profile demo/reviewer(amuxd 解析为 base 类型 codex)"
OUT="$("$AMUX" task submit "list files" --profile demo/reviewer --isolation process --json 2>&1)"
echo "$OUT" | grep -Eo '"agent_type"[[:space:]]*:[[:space:]]*"[^"]*"' || echo "$OUT"
echo "$OUT" | grep -Eq '"agent_type"[[:space:]]*:[[:space:]]*"codex"'
TASK_ID="$(echo "$OUT" | grep -Eo '"id"[[:space:]]*:[[:space:]]*"[^"]*"' | head -1 | sed -E 's/.*"([^"]+)".*/\1/')"

say "确认 task-path profile 已解析并记录为 base agent"
"$AMUX" task show "$TASK_ID" --json | grep -Eq '"agent_type"[[:space:]]*:[[:space:]]*"codex"'
"$AMUX" task list | grep -q "codex"

say "取消演示 task 并等待 child session 清理"
"$AMUX" task cancel "$TASK_ID" >/dev/null || true
for _ in $(seq 1 30); do
  STATUS="$("$AMUX" task show "$TASK_ID" --json | sed -nE 's/.*"status"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/p' | head -1)"
  case "$STATUS" in
    succeeded|failed|cancelled|timed_out) break ;;
  esac
  sleep 0.2
done

say "演练完成 (退出时自动清理临时 HOME 与 daemon)"
