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
export GOPROXY=https://proxy.golang.org
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

cp internal/transport/pbgen/amux.pb.go internal/transport/pbgen/amux_grpc.pb.go "$test_root/generated/"
buf generate
cmp internal/transport/pbgen/amux.pb.go "$test_root/generated/amux.pb.go"
cmp internal/transport/pbgen/amux_grpc.pb.go "$test_root/generated/amux_grpc.pb.go"
go mod verify
make docs-check
unset AMUX_RUN_LONG_TESTS
go test -timeout=10m ./...
long_test_names="$(bash scripts/list-long-tests.sh)"
if [[ -z "$long_test_names" ]]; then
  printf 'no CLI long-running tests discovered\n' >&2
  exit 1
fi
long_test_pattern="^($(printf '%s\n' "$long_test_names" | paste -sd '|' -))$"
AMUX_RUN_LONG_TESTS=1 go test -timeout=10m ./app/cli -run "$long_test_pattern" -count=1 -parallel=2
make build
if command -v npm >/dev/null 2>&1; then
  cp -R sidecars/claude-sdk "$test_root/sidecar"
  (cd "$test_root/sidecar" && npm ci --registry=https://registry.npmjs.org --ignore-scripts --no-audit --no-fund && npm run build && npm test)
fi

git diff --check
check_gate_finish
