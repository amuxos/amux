#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"

# A dependent distribution may use this packager with its own Makefile and mains.
if [[ -n "${AMUX_PACKAGE_BUILD_ROOT:-}" ]]; then
  repo_root="$(cd "$AMUX_PACKAGE_BUILD_ROOT" && pwd)"
  [[ -f "$repo_root/go.mod" && -f "$repo_root/Makefile" ]] || {
    printf 'AMUX_PACKAGE_BUILD_ROOT must contain go.mod and Makefile\n' >&2
    exit 2
  }
fi

version="$(git -C "$repo_root" describe --tags --always --dirty 2>/dev/null || echo dev)"
goos=""
goarch=""
source_dir=""
dist_dir="$repo_root/dist"
base_url=""
cgo_enabled="${CGO_ENABLED:-0}"

usage() {
  cat <<EOF
Usage:
  scripts/package-release.sh [--version <version>] [--source <dir>] [--dist-dir <dir>] [--os <goos>] [--arch <goarch>] [--base-url <url>]

Build or package amux release binaries for one platform.

Main flow:
  make build VERSION=<version>
  tar.gz amux, amuxd, and amuxlet
  write .sha256 and latest.json

Options:
  --version <version>  Version embedded in binaries and package name (default: git describe)
  --source <dir>       Directory containing amux, amuxd, and amuxlet; skips make build
  --dist-dir <dir>     Output directory (default: ./dist)
  --os <goos>          Package OS label (default: go env GOOS)
  --arch <goarch>      Package arch label (default: go env GOARCH)
  --base-url <url>     Base HTTP URL used in latest.json asset URL

Examples:
  scripts/package-release.sh --version v0.1.5
  scripts/package-release.sh --source ./bin --version v0.1.5 --base-url https://example.com/amux

Environment:
  CGO_ENABLED          CGO mode for source builds (default: 0)
  AMUX_PACKAGE_BUILD_ROOT  Distribution module directory (default: this module)
EOF
}

detect_goos() {
  if command -v go >/dev/null 2>&1; then
    go env GOOS
    return
  fi
  case "$(uname -s)" in
    Darwin) printf 'darwin\n' ;;
    Linux) printf 'linux\n' ;;
    *) uname -s | tr '[:upper:]' '[:lower:]' ;;
  esac
}

detect_goarch() {
  if command -v go >/dev/null 2>&1; then
    go env GOARCH
    return
  fi
  case "$(uname -m)" in
    arm64 | aarch64) printf 'arm64\n' ;;
    x86_64 | amd64) printf 'amd64\n' ;;
    *) uname -m ;;
  esac
}

while (($#)); do
  case "$1" in
    --version)
      version="${2:?missing value for --version}"
      shift
      ;;
    --source)
      source_dir="${2:?missing value for --source}"
      shift
      ;;
    --dist-dir)
      dist_dir="${2:?missing value for --dist-dir}"
      shift
      ;;
    --os)
      goos="${2:?missing value for --os}"
      shift
      ;;
    --arch)
      goarch="${2:?missing value for --arch}"
      shift
      ;;
    --base-url)
      base_url="${2:?missing value for --base-url}"
      shift
      ;;
    -h | --help)
      usage
      exit 0
      ;;
    *)
      printf 'unknown argument: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
  shift
done

if [[ -z "$goos" ]]; then
  goos="$(detect_goos)"
fi
if [[ -z "$goarch" ]]; then
  goarch="$(detect_goarch)"
fi

[[ "$version" =~ ^[A-Za-z0-9][A-Za-z0-9._+-]*$ && "$goos" =~ ^[a-z0-9]+$ && "$goarch" =~ ^[a-z0-9]+$ ]] || {
  printf 'version and platform must be plain filename components\n' >&2
  exit 2
}

if [[ -z "$source_dir" ]]; then
  printf '\n==> make build VERSION=%s GOOS=%s GOARCH=%s CGO_ENABLED=%s\n' "$version" "$goos" "$goarch" "$cgo_enabled"
  CGO_ENABLED="$cgo_enabled" GOOS="$goos" GOARCH="$goarch" make -C "$repo_root" build VERSION="$version" BUILD_DIR="$repo_root/bin"
  source_dir="$repo_root/bin"
fi

source_dir="$(cd "$source_dir" && pwd)"
dist_dir="$(mkdir -p "$dist_dir" && cd "$dist_dir" && pwd)"

for bin in amux amuxd amuxlet; do
  if [[ ! -f "$source_dir/$bin" ]]; then
    printf 'missing %s in %s\n' "$bin" "$source_dir" >&2
    exit 1
  fi
done

package="amux_${version}_${goos}_${goarch}"
package_dir="$dist_dir/$package"
archive="$dist_dir/$package.tar.gz"
checksum_file="$archive.sha256"
manifest="$dist_dir/latest.json"

if [[ "$source_dir" == "$package_dir" || "$source_dir" == "$package_dir/"* ]]; then
  printf 'package staging directory would remove input binaries: %s\n' "$package_dir" >&2
  exit 2
fi

rm -rf "$package_dir" "$archive" "$checksum_file"
mkdir -p "$package_dir"
cp "$source_dir/amux" "$source_dir/amuxd" "$source_dir/amuxlet" "$package_dir/"
chmod 0755 "$package_dir/amux" "$package_dir/amuxd" "$package_dir/amuxlet"

COPYFILE_DISABLE=1 tar -C "$dist_dir" -czf "$archive" "$package"
shasum -a 256 "$archive" > "$checksum_file"
sha256="$(awk '{print $1}' "$checksum_file")"

asset_url="$package.tar.gz"
if [[ -n "$base_url" ]]; then
  asset_url="${base_url%/}/$package.tar.gz"
fi

cat > "$manifest" <<EOF
{
  "version": "$version",
  "assets": {
    "${goos}_${goarch}": {
      "url": "$asset_url",
      "sha256": "$sha256"
    }
  }
}
EOF

printf 'Packaged %s\n' "$archive"
printf 'Checksum %s\n' "$checksum_file"
printf 'Manifest %s\n' "$manifest"
