#!/usr/bin/env bash

set -euo pipefail

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "$script_dir/.." && pwd)"
core_root="$repo_root"

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
with_sidecar=0
cgo_enabled="${CGO_ENABLED:-0}"

usage() {
  cat <<EOF
Usage:
  scripts/package-release.sh [--version <version>] [--source <dir>] [--dist-dir <dir>] [--os <goos>] [--arch <goarch>] [--base-url <url>]

Build or package amux release binaries for one platform.

Main flow:
  make build VERSION=<version>
  tar.gz amux, amuxd, amuxlet, and public-core notices in licenses/amux/
  write .sha256 and latest.json

Options:
  --version <version>  Version embedded in binaries and package name (default: git describe)
  --source <dir>       Directory containing amux, amuxd, and amuxlet; skips make build
  --dist-dir <dir>     Output directory (default: ./dist)
  --os <goos>          Package OS label (default: go env GOOS)
  --arch <goarch>      Package arch label (default: go env GOARCH)
  --base-url <url>     Base HTTP URL used in latest.json asset URL
  --with-sidecar      Include the prepared public Claude SDK sidecar

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
    --with-sidecar)
      with_sidecar=1
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

exe=""
if [[ "$goos" == windows ]]; then
  exe=".exe"
fi

if [[ -z "$source_dir" ]]; then
  printf '\n==> make build VERSION=%s GOOS=%s GOARCH=%s CGO_ENABLED=%s\n' "$version" "$goos" "$goarch" "$cgo_enabled"
  CGO_ENABLED="$cgo_enabled" GOOS="$goos" GOARCH="$goarch" make -C "$repo_root" build VERSION="$version" BUILD_DIR="$repo_root/bin"
  source_dir="$repo_root/bin"
fi

source_dir="$(cd "$source_dir" && pwd)"
dist_dir="$(mkdir -p "$dist_dir" && cd "$dist_dir" && pwd)"

for bin in amux amuxd amuxlet; do
  if [[ ! -f "$source_dir/$bin$exe" ]]; then
    printf 'missing %s in %s\n' "$bin$exe" "$source_dir" >&2
    exit 1
  fi
done
if [[ "$goos" == windows && ! -f "$source_dir/Setup.exe" ]]; then
  printf 'missing Setup.exe in %s\n' "$source_dir" >&2
  exit 1
fi

# The public license applies to the core, even when this script builds a
# separately licensed distribution through AMUX_PACKAGE_BUILD_ROOT.
notice_files=(LICENSE NOTICE THIRD_PARTY_NOTICES.md THIRD_PARTY_NOTICES.zh-CN.md go.mod third_party/go-notices.txt)
for notice in "${notice_files[@]}"; do
  if [[ ! -s "$core_root/$notice" ]]; then
    printf 'missing public-core license material: %s\n' "$notice" >&2
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
cp "$source_dir/amux$exe" "$source_dir/amuxd$exe" "$source_dir/amuxlet$exe" "$package_dir/"
chmod 0755 "$package_dir/amux$exe" "$package_dir/amuxd$exe" "$package_dir/amuxlet$exe"
if [[ "$goos" == windows ]]; then
  cp "$source_dir/Setup.exe" "$package_dir/Setup.exe"
  chmod 0755 "$package_dir/Setup.exe"
fi

for notice in "${notice_files[@]}"; do
  destination="$package_dir/licenses/amux/$notice"
  mkdir -p "$(dirname "$destination")"
  cp "$core_root/$notice" "$destination"
done

if [[ "$with_sidecar" == 1 ]]; then
  [[ "$repo_root" == "$core_root" ]] || { printf 'sidecar packaging requires the public module\n' >&2; exit 2; }
  sdk="$core_root/sidecars/claude-sdk"
  [[ -f "$sdk/dist/index.js" && -f "$sdk/package.json" && -d "$sdk/node_modules/@anthropic-ai/claude-agent-sdk" ]] || {
    printf 'run make sidecar before packaging with --with-sidecar\n' >&2
    exit 1
  }
  mkdir -p "$package_dir/sidecars/claude-sdk"
  cp -RL "$sdk/dist" "$sdk/node_modules" "$sdk/package.json" "$package_dir/sidecars/claude-sdk/"
  chmod +x "$package_dir/sidecars/claude-sdk/dist/index.js"
fi

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

if [[ "$goos" == windows ]]; then
  setup_asset="$dist_dir/${package}_Setup.exe"
  cp "$package_dir/Setup.exe" "$setup_asset"
  shasum -a 256 "$setup_asset" > "$setup_asset.sha256"
  setup_sha="$(awk '{print $1}' "$setup_asset.sha256")"
  setup_url="${package}_Setup.exe"
  if [[ -n "$base_url" ]]; then
    setup_url="${base_url%/}/${package}_Setup.exe"
  fi
  # Prefer Setup.exe as the windows install entry in latest.json while still
  # shipping the full tar.gz for offline extraction next to sibling binaries.
  cat > "$manifest" <<EOF
{
  "version": "$version",
  "assets": {
    "${goos}_${goarch}": {
      "url": "$setup_url",
      "sha256": "$setup_sha",
      "package_url": "$asset_url",
      "package_sha256": "$sha256",
      "installer": "Setup.exe"
    }
  }
}
EOF
  printf 'Setup %s\n' "$setup_asset"
fi

printf 'Packaged %s\n' "$archive"
printf 'Checksum %s\n' "$checksum_file"
printf 'Manifest %s\n' "$manifest"
