#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

# Long-running app/cli tests call requireLongTest(t) as their first statement.
# Discover them from source so the full gate cannot drift from a hand-maintained
# list when a test is added or renamed.
awk '
  /^func Test[[:alnum:]_]+\(t \*testing\.T\) \{/ {
    name = $2
    sub(/\(.*/, "", name)
    in_test = 1
    next
  }
  /^func / {
    name = ""
    in_test = 0
  }
  in_test && /^[[:space:]]*requireLongTest\(t\)[[:space:]]*$/ {
    print name
    name = ""
    in_test = 0
  }
' "$repo_root"/app/cli/*_test.go | LC_ALL=C sort -u
