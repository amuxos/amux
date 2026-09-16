#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${AMUX_CHECK_ROOT:-$script_dir/..}" && pwd)"
export GOWORK=off

base_ref=""
run_pattern=""
include_e2e=0
declare -a packages=()

usage() {
  cat <<'EOF'
Usage:
  scripts/check-fast.sh [options]

Run a disposable, focused edit-loop check. By default it tests the Go packages
containing files changed from HEAD, including staged and untracked files. This
is intentionally a fast developer check; scripts/check.sh remains the required
pre-push quality gate.

Options:
  --base <ref>       Also include committed changes since the merge-base of ref.
  --package <path>   Test an explicit Go package (repeatable).
  --run <regexp>     Pass a test-name regexp to go test.
  --include-e2e      Do not pass -short to go test.
  -h, --help         Show this help.

Examples:
  scripts/check-fast.sh
  scripts/check-fast.sh --package ./internal/bridge --run 'Skill|SlashCommand'
  scripts/check-fast.sh --base origin/main
EOF
}

while (($#)); do
  case "$1" in
    --base)
      [[ $# -ge 2 && -n "$2" ]] || { printf '%s\n' '--base requires a ref' >&2; exit 2; }
      base_ref="$2"
      shift
      ;;
    --package)
      [[ $# -ge 2 && -n "$2" ]] || { printf '%s\n' '--package requires a path' >&2; exit 2; }
      packages+=("$2")
      shift
      ;;
    --run)
      [[ $# -ge 2 && -n "$2" ]] || { printf '%s\n' '--run requires a regexp' >&2; exit 2; }
      run_pattern="$2"
      shift
      ;;
    --include-e2e)
      include_e2e=1
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      printf 'unknown argument: %s\n\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
  shift
done

cd "$repo_root"

declare -a changed_files=()
while IFS= read -r path; do
  [[ -n "$path" ]] && changed_files+=("$path")
done < <(
  {
    if git rev-parse --verify HEAD >/dev/null 2>&1; then
      git diff --relative --name-only --diff-filter=ACMRD HEAD -- .
    else
      git diff --relative --cached --name-only --diff-filter=ACMRD -- .
    fi
    git ls-files --others --exclude-standard -- .
    if [[ -n "$base_ref" ]]; then
      git diff --relative --name-only --diff-filter=ACMRD "$base_ref"...HEAD -- .
    fi
  } | sort -u
)

add_package() {
  local candidate="$1"
  local existing
  for existing in "${packages[@]:-}"; do
    [[ "$existing" == "$candidate" ]] && return
  done
  packages+=("$candidate")
}

# A checkout may contain independent Go modules. Only infer packages owned by
# this module; run this helper from each nested module to check its own changes.
belongs_to_module() {
  local directory
  directory="$(dirname "$1")"
  while [[ "$directory" != "." && "$directory" != "/" ]]; do
    [[ ! -f "$directory/go.mod" ]] || return 1
    directory="$(dirname "$directory")"
  done
}

proto_changed=0
if ((${#packages[@]} == 0)); then
  for path in "${changed_files[@]:-}"; do
    belongs_to_module "$path" || continue
    # A changed submodule can alter dependency APIs without changing go.mod.
    # Validate consumers in this module as well as the dependency's own gate.
    if [[ "$(git ls-files --stage -- "$path")" == 160000\ * ]]; then
      packages=("./...")
      break
    fi
    case "$path" in
      go.mod | go.sum)
        packages=("./...")
        break
        ;;
      proto/* | buf.yaml | buf.gen.yaml)
        proto_changed=1
        add_package "./internal/transport/pbgen"
        ;;
      internal/profiles/*)
        add_package "./internal/profiles"
        ;;
      agent-packs/*)
        add_package "./agent-packs"
        if [[ -d internal/agentprofile ]]; then
          add_package "./internal/agentprofile"
        fi
        if [[ -d internal/profiles ]]; then
          add_package "./internal/profiles"
        fi
        ;;
    esac
    if [[ "$path" == *.go ]]; then
      dir="$(dirname "$path")"
      if [[ "$dir" == "." ]]; then
        add_package "."
      elif [[ -d "$dir" ]]; then
        add_package "./$dir"
      fi
    fi
  done
else
  for path in "${changed_files[@]:-}"; do
    belongs_to_module "$path" || continue
    [[ "$path" == proto/* || "$path" == buf.yaml || "$path" == buf.gen.yaml ]] && proto_changed=1
  done
fi

if ((proto_changed == 1)); then
  printf '\n==> buf generate\n'
  buf generate
fi

tmpdir="$(mktemp -d)"
cleanup() {
  rm -rf "$tmpdir"
}
trap cleanup EXIT

if ((${#packages[@]} > 0)); then
  test_home="$tmpdir/test-home"
  mkdir -p "$test_home/.amux"
  printf '[hub]\naddr = "127.0.0.1:0"\n' > "$test_home/.amux/config.toml"

  test_args=(-count=1)
  if ((include_e2e == 0)); then
    test_args+=(-short)
  fi
  if [[ -n "$run_pattern" ]]; then
    test_args+=(-run "$run_pattern")
  fi

  printf '\n==> go test'
  printf ' %q' "${test_args[@]}" "${packages[@]}"
  printf '\n'
  env -u AMUX_HOME -u AMUX_HUB_ADDR \
    HOME="$test_home" \
    GOPATH="$(go env GOPATH)" \
    GOMODCACHE="$(go env GOMODCACHE)" \
    GOCACHE="${GOCACHE:-$(go env GOCACHE)}" \
    go test "${test_args[@]}" "${packages[@]}"
else
  printf '\n==> no changed Go package detected; skipping go test\n'
fi

printf '\n==> git diff --check\n'
git diff --check
git diff --cached --check
if [[ -n "$base_ref" ]]; then
  git diff --check "$base_ref"...HEAD
fi

printf '\nFast checks passed. Run scripts/check.sh before commit or push.\n'
