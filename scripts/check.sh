#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")/.."

force=false
for arg in "$@"; do
  case "$arg" in
    --force) force=true ;;
    -h|--help) printf 'Usage: scripts/check.sh [--force]\n'; exit 0 ;;
    *) printf 'Usage: scripts/check.sh [--force]\n' >&2; exit 2 ;;
  esac
done
source scripts/lib/check-gate.sh
check_gate_begin "$PWD" public "$force"

# Avoid developer workspace overrides and installation state during validation.
export GOWORK=off
export GOTOOLCHAIN=local
export GOPROXY="${AMUX_CHECK_GOPROXY:-https://proxy.golang.org}"
export GOSUMDB=sum.golang.org
export GOPRIVATE= GONOPROXY= GONOSUMDB=
test_root="$check_gate_tmp"
export GOPATH="$(go env GOPATH)"
export GOMODCACHE="$(go env GOMODCACHE)"
export GOCACHE="${GOCACHE:-$(go env GOCACHE)}"
unset AMUX_HOME AMUX_HUB_ADDR
export HOME="$test_root/home"
mkdir -p "$HOME/.amux" "$test_root/generated"
printf '[hub]\naddr="127.0.0.1:0"\n' > "$HOME/.amux/config.toml"

run_parallel_checks() {
  local log_dir="$1"
  shift
  local pids=() names=() logs=() idx=0 name cmd log_file failed=0
  mkdir -p "$log_dir"
  while [[ $# -ge 2 ]]; do
    name="$1"
    cmd="$2"
    shift 2
    idx=$((idx + 1))
    log_file="$log_dir/$idx.log"
    ( eval "$cmd" ) >"$log_file" 2>&1 &
    pids+=("$!")
    names+=("$name")
    logs+=("$log_file")
  done
  local i
  for i in "${!pids[@]}"; do
    if ! wait "${pids[$i]}"; then
      printf 'check failed: %s\n' "${names[$i]}" >&2
      cat "${logs[$i]}" >&2
      failed=1
    fi
  done
  return "$failed"
}

cp internal/transport/pbgen/amux.pb.go internal/transport/pbgen/amux_grpc.pb.go "$test_root/generated/"
buf generate
cmp internal/transport/pbgen/amux.pb.go "$test_root/generated/amux.pb.go"
cmp internal/transport/pbgen/amux_grpc.pb.go "$test_root/generated/amux_grpc.pb.go"

run_parallel_checks "$test_root/static-checks" \
  "go mod verify" "go mod verify" \
  "update-go-notices" "python3 scripts/update-go-notices.py --check" \
  "test_public_release" "python3 -B -m unittest discover -s scripts -p test_public_release.py" \
  "docs-check" "make docs-check" \
  "build" "make build"

unset AMUX_RUN_LONG_TESTS
AMUX_TEST_BIN_DIR="$PWD/bin" go test -timeout=10m ./...
long_test_names="$(bash scripts/list-long-tests.sh)"
if [[ -z "$long_test_names" ]]; then
  printf 'no CLI long-running tests discovered\n' >&2
  exit 1
fi
long_test_pattern="^($(printf '%s\n' "$long_test_names" | paste -sd '|' -))$"
long_test_parallel="${AMUX_LONG_TEST_PARALLEL:-6}"
run_claude_sidecar_checks() {
  cp -R sidecars/claude-sdk "$test_root/sidecar"
  if [[ -f sidecars/claude-sdk/package-lock.json ]]; then
    local claude_lock_hash claude_nm_cache claude_nm_tmp
    claude_lock_hash="$(git hash-object sidecars/claude-sdk/package-lock.json)"
    claude_nm_cache="${AMUX_NPM_CACHE_DIR:-${TMPDIR:-/tmp}/amux-check-node-modules}/claude-sdk-$claude_lock_hash"
    if [[ -d "$claude_nm_cache/node_modules" ]]; then
      ln -s "$claude_nm_cache/node_modules" "$test_root/sidecar/node_modules"
    else
      (cd "$test_root/sidecar" && npm ci --registry=https://registry.npmjs.org --ignore-scripts --no-audit --no-fund)
      if [[ -d "$test_root/sidecar/node_modules" ]]; then
        mkdir -p "$(dirname "$claude_nm_cache")"
        claude_nm_tmp="$claude_nm_cache.tmp.$$"
        rm -rf -- "$claude_nm_tmp"
        mkdir -p "$claude_nm_tmp"
        cp -R "$test_root/sidecar/node_modules" "$claude_nm_tmp/node_modules"
        mv "$claude_nm_tmp" "$claude_nm_cache" 2>/dev/null || rm -rf -- "$claude_nm_tmp"
      fi
    fi
  else
    (cd "$test_root/sidecar" && npm ci --registry=https://registry.npmjs.org --ignore-scripts --no-audit --no-fund)
  fi
  (cd "$test_root/sidecar" && npm run build && npm test)
}
long_test_cmd="AMUX_RUN_LONG_TESTS=1 AMUX_TEST_BIN_DIR=\"$PWD/bin\" go test -timeout=10m ./app/cli -run \"$long_test_pattern\" -count=1 -parallel=\"$long_test_parallel\""
if command -v npm >/dev/null 2>&1; then
  run_parallel_checks "$test_root/long-and-sidecar" \
    "cli-long-tests" "$long_test_cmd" \
    "claude-sdk-sidecar" "run_claude_sidecar_checks"
else
  eval "$long_test_cmd"
fi

git diff --check
check_gate_finish
