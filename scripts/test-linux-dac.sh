#!/usr/bin/env bash

set -euo pipefail

if [[ "$(uname -s)" != "Linux" ]]; then
  printf 'test-linux-dac: Linux is required\n' >&2
  exit 1
fi
if [[ "$(id -u)" != "0" ]]; then
  printf 'test-linux-dac: run with sudo -E\n' >&2
  exit 1
fi

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
build_root="${AMUX_DAC_BUILD_ROOT:-$repo_root}"
export GOWORK=off
uid_start="${AMUX_TEST_UID_START:-$((780000 + ($$ % 500) * 4))}"
uid_end="$((uid_start + 20))"
cache_dir="${GOCACHE:-/private/tmp/amux-go-cache}"
tmp_dir="$(mktemp -d)"
cleanup_enabled=0

cleanup() {
  if [[ "$cleanup_enabled" == "1" ]]; then
    for uid in $(seq "$uid_start" "$uid_end"); do
      pkill -KILL -u "$uid" 2>/dev/null || true
    done
    while IFS=: read -r account _ uid _; do
      if [[ "$uid" -ge "$uid_start" && "$uid" -le "$uid_end" && "$account" == amux_* ]]; then
        userdel "$account" 2>/dev/null || true
        groupdel "$account" 2>/dev/null || true
      fi
    done < <(getent passwd)
  fi
  rm -rf "$tmp_dir"
}
trap cleanup EXIT
chmod 0755 "$tmp_dir"

cd "$repo_root"
existing_accounts="$(getent passwd | awk -F: -v first="$uid_start" -v last="$uid_end" '$3 >= first && $3 <= last {print $1 ":" $3}')"
existing_processes="$(ps -eo uid=,pid=,comm= | awk -v first="$uid_start" -v last="$uid_end" '$1 >= first && $1 <= last {print}')"
if [[ -n "$existing_accounts" || -n "$existing_processes" ]]; then
  printf 'test-linux-dac: uid range %s..%s is not empty\n%s\n%s\n' \
    "$uid_start" "$uid_end" "$existing_accounts" "$existing_processes" >&2
  exit 1
fi
cleanup_enabled=1

printf 'test-linux-dac: uid range %s..%s\n' "$uid_start" "$uid_end"
(cd "$build_root" && GOCACHE="$cache_dir" go build -o "$tmp_dir/amux-isolation-helper" ./cmd/amux-isolation-helper)
AMUX_LINUX_DAC_E2E=1 \
AMUX_TEST_UID_START="$uid_start" \
AMUX_TEST_FILE_UID="$uid_end" \
AMUX_TEST_HELPER_BINARY="$tmp_dir/amux-isolation-helper" \
GOCACHE="$cache_dir" \
go test -count=1 -run '^Test(LinuxDAC(Root|HardeningRoot|HelperCrashRoot|FallbackRecoveryRoot|RevokeEscapedRoot)E2E|SessionFileTargetUID)$' -v ./internal/isolation

remaining_accounts="$(getent passwd | awk -F: -v first="$uid_start" -v last="$uid_end" '$3 >= first && $3 <= last {print $1 ":" $3}')"
remaining_processes="$(ps -eo uid=,pid=,comm= | awk -v first="$uid_start" -v last="$uid_end" '$1 >= first && $1 <= last {print}')"
if [[ -n "$remaining_accounts" || -n "$remaining_processes" ]]; then
  printf 'test-linux-dac: cleanup verification failed for uid range %s..%s\n%s\n%s\n' \
    "$uid_start" "$uid_end" "$remaining_accounts" "$remaining_processes" >&2
  exit 1
fi
