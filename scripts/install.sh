#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source_dir=""
case "${1:---from-source}" in
  --from-source)
    [[ $# -le 1 ]] || { echo 'unexpected arguments' >&2; exit 2; }
    make -C "$repo_root" build
    source_dir="$repo_root/bin"
    ;;
  --source)
    [[ $# -eq 2 && -d "$2" ]] || { echo '--source requires a local directory of release binaries' >&2; exit 2; }
    source_dir="$(cd "$2" && pwd)"
    ;;
  --help|-h)
    echo 'Usage: scripts/install.sh [--from-source | --source <extracted-release-directory>]'
    exit 0
    ;;
  *)
    echo 'Usage: scripts/install.sh [--from-source | --source <extracted-release-directory>]' >&2
    exit 2
    ;;
esac
for binary in amux amuxd amuxlet; do
  [[ -f "$source_dir/$binary" && -x "$source_dir/$binary" && ! -L "$source_dir/$binary" ]] || {
    echo "missing regular executable: $binary" >&2
    exit 1
  }
done
exec "$source_dir/amux" install --source "$source_dir" --force
