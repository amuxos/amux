#!/usr/bin/env bash
set -euo pipefail

# Standalone public installer: no dependency on the parent distribution.
runtime=""
agent_profile=""
model=""
source_dir=""
source_root="${AMUX_INSTALL_SOURCE_ROOT:-}"
from_source=0
[[ -z "$source_root" ]] || from_source=1
built_from_source=0
feishu=0
interaction_mode="singleton"
feishu_profile="default"
session_name=""
session_cwd="${AMUX_SESSION_CWD:-$PWD}"
feishu_options=0
update_shell=1
sidecar_dir=""
clean=0
stage_dir=""
download_dir=""
release_version=""
release_base_url="${AMUX_RELEASE_BASE_URL:-https://github.com/amuxos/amux-releases/releases}"
cleanup_install() {
  [[ -z "$stage_dir" ]] || rm -rf -- "$stage_dir"
  [[ -z "$download_dir" ]] || rm -rf -- "$download_dir"
}
trap cleanup_install EXIT

die() { printf 'install: %s\n' "$*" >&2; exit 1; }
progress() { printf '\n==> %s\n' "$*"; }
require_command() { command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"; }

usage() {
  cat <<'EOF'
Usage: bash scripts/install.sh [options]

Install and configure public amux for the current user. Detect installed agents,
select a runtime, and use its matching builtin Profile. Native agents must
already be installed and authenticated.

  --runtime codex|claude|pi     Installed runtime (otherwise detect/prompt).
  --provider <runtime>         Alias for --runtime.
  --agent-profile <ref>        Matching builtin/<runtime> (default: automatic).
  --model <model>              Native model override (default: agent default).
  --from-source                Build this public checkout (default in a checkout).
  --version <vX.Y.Z>           Download a specific public release (otherwise latest).
  --source-root <directory>    Public checkout, also AMUX_INSTALL_SOURCE_ROOT.
  --source <directory>         Use extracted local amux release binaries instead.
  --claude-sidecar <directory> Prepared SDK directory (dist/ and node_modules/).
  --feishu                     Provision/configure a Feishu Bot Profile.
  --interaction-mode <mode>   singleton (default) or thread; requires --feishu.
  --feishu-profile <name>      Bot Profile (default: default); requires --feishu.
  --session-name <name>        Singleton Session (default: <bot-profile>-main).
  --session-cwd <directory>    New singleton workspace (default: calling directory).
  --no-modify-path             Skip adding shell PATH (clean removes old entry).
  --clean                      Stop/uninstall first, deleting all amux data.
  -h, --help                   Show this help.

Examples:
  bash scripts/install.sh
  bash scripts/install.sh --runtime codex --feishu --interaction-mode singleton
  bash scripts/install.sh --provider pi --feishu --interaction-mode thread
  bash /path/to/install.sh --source /path/to/binaries --runtime codex
  bash scripts/install.sh --clean --runtime codex --feishu

Multiple detected runtimes require a choice; use --runtime for unattended runs.
Feishu needs an interactive terminal for QR provisioning. Existing credentials
are reused. No agent prompt or chat message is sent by this script.
Reruns stop the existing system before replacing binaries and preserve data.
--clean deletes amux configuration, credentials, Sessions, logs and Pack data;
Feishu must be provisioned again. Native agents and their logins are retained.
A standalone script downloads from amuxos/amux-releases without Go or make.
AMUX_RELEASE_BASE_URL overrides the releases base URL (HTTPS or file).
EOF
}

set_runtime() {
  [[ -z "$runtime" || "$runtime" == "$1" ]] || die "conflicting runtime selections"
  runtime="$1"
}

while (($#)); do
  option="$1"
  case "$option" in
    --version|--runtime|--provider|--agent-profile|--model|--source|--source-root|--claude-sidecar|--interaction-mode|--feishu-profile|--session-name|--session-cwd)
      [[ $# -ge 2 && -n "$2" && "$2" != --* ]] || die "$option requires a value"
      value="$2"; shift ;;
    --version=*|--runtime=*|--provider=*|--agent-profile=*|--model=*|--source=*|--source-root=*|--claude-sidecar=*|--interaction-mode=*|--feishu-profile=*|--session-name=*|--session-cwd=*)
      value="${option#*=}"; option="${option%%=*}"
      [[ -n "$value" ]] || die "$option requires a value" ;;
  esac
  case "$option" in
    --version) release_version="$value" ;;
    --runtime|--provider) set_runtime "$value" ;;
    --agent-profile) agent_profile="$value" ;;
    --model) model="$value" ;;
    --source) source_dir="$value" ;;
    --source-root) source_root="$value"; from_source=1 ;;
    --from-source) from_source=1 ;;
    --claude-sidecar) sidecar_dir="$value" ;;
    --feishu) feishu=1 ;;
    --interaction-mode) interaction_mode="$value"; feishu_options=1 ;;
    --feishu-profile) feishu_profile="$value"; feishu_options=1 ;;
    --session-name) session_name="$value"; feishu_options=1 ;;
    --session-cwd) session_cwd="$value"; feishu_options=1 ;;
    --no-modify-path) update_shell=0 ;;
    --clean) clean=1 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $option (see --help)" ;;
  esac
  shift
done

[[ -z "$release_version" || ( -z "$source_dir" && -z "$source_root" && "$from_source" == 0 ) ]] || die "--version conflicts with local source options"
[[ -z "$source_dir" || "$from_source" == 0 ]] || die "--source conflicts with --from-source/--source-root"
[[ "$feishu" == 1 || "$feishu_options" == 0 ]] || die "Feishu options require --feishu"
case "$interaction_mode" in singleton|thread) ;; *) die "unsupported interaction mode: $interaction_mode" ;; esac
[[ "$feishu_profile" =~ ^[a-zA-Z0-9][a-zA-Z0-9_-]*$ ]] || die "invalid Feishu Bot Profile name"
session_name="${session_name:-${feishu_profile}-main}"
[[ "$session_name" =~ ^[a-zA-Z0-9][a-zA-Z0-9_.-]*$ ]] || die "invalid Session name"
if [[ -n "$agent_profile" ]]; then
  case "$agent_profile" in
    builtin/codex|builtin/claude|builtin/pi) set_runtime "${agent_profile#builtin/}" ;;
    *) die "public installer Profiles are builtin/codex, builtin/claude and builtin/pi" ;;
  esac
fi

detect_runtime() {
  local candidate choice i
  local -a available=()
  for candidate in codex claude pi; do
    if type -P "$candidate" >/dev/null 2>&1; then available+=("$candidate"); fi
  done
  if [[ -n "$runtime" ]]; then
    case "$runtime" in codex|claude|pi) ;; *) die "unsupported runtime: $runtime" ;; esac
    type -P "$runtime" >/dev/null 2>&1 || die "$runtime is not installed/on PATH; install and authenticate it first"
  elif ((${#available[@]} == 0)); then
    die "no Codex, Claude or Pi found on PATH; install and authenticate one, then rerun with --runtime codex|claude|pi"
  elif ((${#available[@]} == 1)); then
    runtime="${available[0]}"
  else
    printf 'Available agent runtimes:\n' >&2
    for ((i=0; i<${#available[@]}; i++)); do
      printf '  %d) %s (%s)\n' "$((i+1))" "${available[$i]}" "$(type -P "${available[$i]}")" >&2
    done
    printf 'Choose a runtime by number or name: ' >&2
    if [[ -t 0 ]]; then
      read -r choice || die "no selection; pass --runtime codex|claude|pi"
    elif { exec 3</dev/tty; } 2>/dev/null; then
      read -r choice <&3 || die "no selection; pass --runtime codex|claude|pi"
      exec 3<&-
    else
      read -r choice || die "multiple runtimes found; pass --runtime codex|claude|pi"
    fi
    for ((i=0; i<${#available[@]}; i++)); do
      if [[ "$choice" == "$((i+1))" || "$choice" == "${available[$i]}" ]]; then runtime="${available[$i]}"; break; fi
    done
    [[ -n "$runtime" ]] || die "invalid runtime selection: $choice"
  fi
  agent_profile="builtin/$runtime"
  progress "Using $runtime with Agent Profile $agent_profile"
}

public_source() {
  [[ -f "$1/go.mod" && -f "$1/Makefile" ]] &&
    [[ "$(awk '$1 == "module" {gsub(/"/, "", $2); print $2; exit}' "$1/go.mod")" == github.com/amuxos/amux ]]
}

download_release() {
  require_command curl
  require_command tar
  local platform_os platform_arch name checksum actual entry
  case "$(uname -s)" in Darwin) platform_os=darwin ;; Linux) platform_os=linux ;; *) die 'only macOS and Linux releases are available' ;; esac
  case "$(uname -m)" in arm64|aarch64) platform_arch=arm64 ;; x86_64|amd64) platform_arch=amd64 ;; *) die 'unsupported CPU architecture' ;; esac
  case "$release_base_url" in https://*|file://*) ;; *) die 'release URL must use HTTPS or file' ;; esac
  release_base_url="${release_base_url%/}"
  download_dir="$(mktemp -d /tmp/amux-download.XXXXXX)"
  if [[ -z "$release_version" ]]; then
    release_version="$(curl --proto '=https,file' --proto-redir '=https' -fsSL --retry 3 --connect-timeout 15 --max-time 60 --max-filesize 128 "$release_base_url/latest/download/version.txt")" || die 'cannot resolve latest release'
  fi
  [[ "$release_version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || die 'release version must be vX.Y.Z'
  name="amux_${release_version}_${platform_os}_${platform_arch}"
  progress "Downloading $name"
  curl --proto '=https,file' --proto-redir '=https' -fsSL --retry 3 --connect-timeout 15 --max-time 600 "$release_base_url/download/$release_version/$name.tar.gz" -o "$download_dir/package.tar.gz"
  checksum="$(curl --proto '=https,file' --proto-redir '=https' -fsSL --retry 3 --connect-timeout 15 --max-time 60 --max-filesize 1024 "$release_base_url/download/$release_version/$name.tar.gz.sha256")"
  checksum="${checksum%% *}"
  [[ "$checksum" =~ ^[a-f0-9]{64}$ ]] || die 'invalid release checksum'
  if command -v shasum >/dev/null 2>&1; then
    actual="$(shasum -a 256 "$download_dir/package.tar.gz")"
  else
    require_command sha256sum
    actual="$(sha256sum "$download_dir/package.tar.gz")"
  fi
  [[ "${actual%% *}" == "$checksum" ]] || die 'release checksum mismatch; installation unchanged'
  tar -tzf "$download_dir/package.tar.gz" > "$download_dir/entries"
  while IFS= read -r entry; do
    case "$entry" in "$name"|"$name/"*) ;; *) die "unexpected archive path: $entry" ;; esac
    case "/$entry/" in */../*|*\\*) die 'unsafe release archive path' ;; esac
  done < "$download_dir/entries"
  tar -tvzf "$download_dir/package.tar.gz" > "$download_dir/types"
  while IFS= read -r entry; do
    case "${entry:0:1}" in -|d) ;; *) die 'release contains an unsupported archive entry' ;; esac
  done < "$download_dir/types"
  tar -xzf "$download_dir/package.tar.gz" -C "$download_dir"
  source_dir="$download_dir/$name"
  [[ -n "$sidecar_dir" ]] || sidecar_dir="$source_dir/sidecars/claude-sdk"
}

prepare_source() {
  if [[ -n "$source_dir" ]]; then
    [[ -d "$source_dir" ]] || die "binary directory not found: $source_dir"
    source_dir="$(cd "$source_dir" && pwd -P)"
  else
    if [[ -z "$source_root" ]]; then
      source_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
      if ! public_source "$source_root" && public_source "$PWD"; then source_root="$PWD"; fi
    fi
    if [[ -n "$release_version" ]] || { [[ "$from_source" == 0 ]] && ! public_source "$source_root"; }; then
      download_release
    else
      public_source "$source_root" || die "--from-source requires the public github.com/amuxos/amux checkout; use --source-root"
      source_root="$(cd "$source_root" && pwd -P)"
      if [[ "$clean" == 1 ]] && within_directory "$source_root" "$amux_dir"; then
        die 'source checkout is inside the --clean directory; use a checkout outside AMUX_HOME'
      fi
      require_command go
      require_command make
      if [[ "$runtime" == claude ]]; then require_command node; require_command npm; fi
      progress "Building public amux from $source_root"
      GOWORK=off make -C "$source_root" build BUILD_DIR="$source_root/bin"
      source_dir="$source_root/bin"
      built_from_source=1
    fi
  fi
  local binary
  for binary in amux amuxd amuxlet; do
    [[ -f "$source_dir/$binary" && -x "$source_dir/$binary" && ! -L "$source_dir/$binary" ]] || die "missing regular executable: $source_dir/$binary"
  done
}

prepare_claude_sidecar() {
  [[ "$runtime" == claude ]] || return 0
  require_command node
  if [[ -z "$sidecar_dir" ]]; then
    if [[ "$built_from_source" == 1 ]]; then
      progress 'Building the Claude SDK sidecar'
      make -C "$source_root" sidecar
      sidecar_dir="$source_root/sidecars/claude-sdk"
    else
      sidecar_dir="$source_dir/sidecars/claude-sdk"
      [[ -d "$sidecar_dir" ]] || sidecar_dir="$source_dir/../sidecars/claude-sdk"
    fi
  fi
  [[ -f "$sidecar_dir/dist/index.js" && -f "$sidecar_dir/package.json" && -d "$sidecar_dir/node_modules/@anthropic-ai/claude-agent-sdk" ]] ||
    die "Claude needs its prepared SDK sidecar; use --claude-sidecar <directory> or build from source"
  sidecar_dir="$(cd "$sidecar_dir" && pwd -P)"
}

within_directory() { [[ "$1" == "$2" || "$1" == "${2%/}/"* ]]; }

# Resolve existing symlinked parents without creating a new install directory
# until validation has succeeded. Also normalize missing paths ending in /../.
canonical_directory() {
  if [[ -d "$1" ]]; then (cd "$1" && pwd -P); return; fi
  [[ ! -e "$1" && ! -L "$1" ]] || die "not a usable directory: $1"
  local parent name
  parent="$(canonical_directory "$(dirname "$1")")" || return 1
  name="$(basename "$1")"
  case "$name" in
    .) printf '%s\n' "$parent" ;;
    ..) dirname "$parent" ;;
    *) printf '%s/%s\n' "${parent%/}" "$name" ;;
  esac
}

validate_clean_directory() {
  [[ "$clean" == 1 ]] || return 0
  local user_dir protected resolved
  user_dir="$(cd "$HOME" && pwd -P)"
  within_directory "$user_dir" "$amux_dir" && die "unsafe --clean directory (HOME or its ancestor): $amux_dir"
  for protected in / /bin /sbin /usr /usr/local /opt /var /tmp /private /private/tmp /etc /Users /home /Volumes; do
    [[ -d "$protected" ]] || continue
    resolved="$(cd "$protected" && pwd -P)"
    [[ "$amux_dir" != "$resolved" ]] || die "unsafe --clean directory: $amux_dir"
  done
  if [[ "$feishu" == 1 && "$interaction_mode" == singleton ]] && within_directory "$session_cwd" "$amux_dir"; then
    die "singleton workspace is inside the --clean directory; choose --session-cwd outside $amux_dir"
  fi
}

stage_payload() {
  # Use /tmp, not TMPDIR: a caller may have placed TMPDIR inside AMUX_HOME.
  stage_dir="$(mktemp -d /tmp/amux-install.XXXXXX)"
  stage_dir="$(cd "$stage_dir" && pwd -P)"
  within_directory "$stage_dir" "$amux_dir" && die 'staging directory must be outside AMUX_HOME'
  mkdir -p "$stage_dir/bin"
  local binary
  for binary in amux amuxd amuxlet; do
    cp -p "$source_dir/$binary" "$stage_dir/bin/$binary"
  done
  source_dir="$stage_dir/bin"
  if [[ "$runtime" == claude ]]; then
    mkdir -p "$stage_dir/sidecars/claude-sdk"
    # Materialize links too, so purging the original SDK cannot break staging.
    cp -RL "$sidecar_dir/dist" "$sidecar_dir/node_modules" "$sidecar_dir/package.json" "$stage_dir/sidecars/claude-sdk/"
    sidecar_dir="$stage_dir/sidecars/claude-sdk"
    chmod +x "$sidecar_dir/dist/index.js"
  fi
  # Check the new public Profiles independently of the old installation's Pack
  # registry and configuration. HOME still points to native agent login data.
  AMUX_HOME="$stage_dir" "$source_dir/amux" agent show "$agent_profile" >/dev/null || die "source binaries do not contain $agent_profile"
  local -a doctor_cmd=("$source_dir/amux" agent doctor "$agent_profile" --runtime "$runtime")
  [[ -z "$model" ]] || doctor_cmd+=(--model "$model")
  AMUX_HOME="$stage_dir" "${doctor_cmd[@]}"
}

stop_existing_installation() {
  [[ "$had_install" == 1 ]] || return 0
  local stop_bin="$source_dir/amux" status
  # The old CLI knows the installed sibling paths for orphan-process ownership
  # checks. A partial install without that CLI can still use recorded PIDs.
  if [[ -x "$amux_bin" ]]; then stop_bin="$amux_bin"; fi
  progress "Stopping existing amux in $amux_dir"
  status="$("$stop_bin" agent pack autosync status)" || die 'cannot inspect existing Pack autosync; installation unchanged'
  [[ -z "$(status_field error <<<"$status")" ]] || die 'cannot inspect existing Pack autosync; installation unchanged'
  if [[ "$(status_field state <<<"$status")" == running ]]; then restore_autosync=1; fi
  "$stop_bin" agent pack autosync stop || die 'could not stop Pack autosync; installation not replaced'
  "$stop_bin" system stop --force || die 'could not stop existing amux; installation not replaced'
}

install_claude_sidecar() {
  [[ "$runtime" == claude ]] || return 0
  # The installed runtime discovers this sibling layout after service restarts.
  local destination="$amux_dir/sidecars/claude-sdk" staging
  mkdir -p "$amux_dir/sidecars"
  if [[ "$sidecar_dir" == "$destination" ]]; then
    chmod +x "$destination/dist/index.js"
    return 0
  fi
  staging="$(mktemp -d "$amux_dir/sidecars/.claude-sdk.XXXXXX")"
  cp -R "$sidecar_dir/dist" "$sidecar_dir/node_modules" "$sidecar_dir/package.json" "$staging/"
  chmod +x "$staging/dist/index.js"
  rm -rf -- "$destination"
  mv "$staging" "$destination"
}

wait_for_system() {
  local attempt
  for ((attempt=1; attempt<=30; attempt++)); do
    if "$amux_bin" system doctor >/dev/null 2>&1; then return 0; fi
    sleep 1
  done
  "$amux_bin" system status >&2 || true
  die 'amux did not become ready; inspect amux system doctor'
}

status_field() { awk -v field="$1:" '$1 == field { $1=""; sub(/^[[:space:]]+/, ""); print; exit; }'; }

bot_status() {
  local status error
  status="$("$amux_bin" bridge feishu bot-session status --profile "$feishu_profile")" || die 'cannot inspect Feishu Bot Profile'
  error="$(status_field error <<<"$status")"
  [[ -z "$error" ]] || die "$error"
  printf '%s\n' "$status"
}

configure_singleton() {
  local status enabled bound_session bound_agent sessions row existing_runtime existing_profile state target target_ref
  # Unlike CLI Session commands, the Hub's bot binding RPC takes an actual
  # target ID/name, not the client-side "local" alias.
  target_ref="$("$amux_bin" system config get runtime.id 2>/dev/null)" || target_ref=""
  # Existing configs may omit runtime.id; the Runtime then uses os.Hostname.
  if [[ -z "$target_ref" ]]; then target_ref="$(hostname)" || die 'cannot resolve the local Runtime ID'; fi
  [[ -n "$target_ref" ]] || die 'local Runtime ID and hostname are empty'
  status="$(bot_status)"
  enabled="$(status_field enabled <<<"$status")"
  bound_session="$(status_field session <<<"$status")"
  bound_agent="$(status_field agent <<<"$status")"
  if [[ "$enabled" == yes ]]; then
    [[ "$bound_session" == */"$session_name" && "$bound_agent" == "$agent_profile" ]] ||
      die "existing singleton binding conflicts with $agent_profile / $session_name; preserve it or choose another --feishu-profile"
  fi
  # Recover interrupted installs without duplicating an already-created Session.
  sessions="$("$amux_bin" session list local)"
  row="$(awk -v name="$session_name" '$1 == name { print $2, $3, $4, $6; exit }' <<<"$sessions")"
  if [[ -n "$row" ]]; then
    read -r existing_runtime existing_profile state target <<<"$row"
    [[ "$existing_runtime" == "$runtime" && "$existing_profile" == "$agent_profile" ]] || die "Session $session_name uses a different Profile/runtime; choose --session-name"
    case "$state" in archived|dead|stopped|failed) die "Session $session_name is $state; choose another --session-name" ;; esac
    if [[ "$enabled" == yes ]]; then
      [[ "$bound_session" == "$target/$session_name" || "$bound_session" == "$target_ref/$session_name" || "$bound_session" == "local/$session_name" ]] || die 'existing singleton is bound to a different runtime target'
    fi
    progress "Reusing Session $session_name"
    if [[ -n "$model" ]]; then "$amux_bin" session set-model "local/$session_name" "$model"; fi
  else
    [[ "$enabled" != yes ]] || die "bound Session $session_name is not on the local runtime"
    local -a run_cmd=("$amux_bin" session run "$agent_profile" -n "$session_name" --on local --runtime "$runtime" --cwd "$session_cwd")
    if [[ -n "$model" ]]; then run_cmd+=(--model "$model"); fi
    "${run_cmd[@]}"
  fi
  "$amux_bin" bridge feishu bot-session bind --profile "$feishu_profile" "$target_ref/$session_name"
  status="$(bot_status)"
  [[ "$(status_field mode <<<"$status")" == singleton && "$(status_field enabled <<<"$status")" == yes ]] || die 'singleton binding did not become active'
}

configure_feishu() {
  [[ "$feishu" == 1 ]] || return 0
  local -a setup_cmd=("$amux_bin" bridge feishu setup --profile "$feishu_profile" --no-start)
  local status
  # Provision first: a successful QR command can still mean the user cancelled.
  if ! "$amux_bin" bridge feishu test --profile "$feishu_profile" >/dev/null 2>&1; then
    "${setup_cmd[@]}"
    "$amux_bin" bridge feishu test --profile "$feishu_profile" || die "Feishu Bot Profile $feishu_profile was not created; complete setup in an interactive terminal and rerun"
  fi
  "$amux_bin" system restart --force
  wait_for_system
  if [[ "$interaction_mode" == thread ]]; then
    status="$(bot_status)"
    if [[ "$(status_field enabled <<<"$status")" == yes ]]; then
      "$amux_bin" bridge feishu bot-session off --profile "$feishu_profile"
    fi
    setup_cmd+=(--interaction-mode thread --default-agent "$agent_profile" --default-runtime "$runtime")
    if [[ -n "$model" ]]; then setup_cmd+=(--default-model "$model"); fi
    # Reuse saved credentials without another QR or printing credential defaults.
    "${setup_cmd[@]}" --manual </dev/null >/dev/null
    "$amux_bin" system restart --force
    wait_for_system
    status="$(bot_status)"
    [[ "$(status_field mode <<<"$status")" == thread && "$(status_field default_agent <<<"$status")" == "$agent_profile" ]] || die 'Feishu thread defaults were not applied'
  else
    configure_singleton
  fi
  "$amux_bin" bridge feishu test --profile "$feishu_profile"
}

detect_runtime
if [[ "$feishu" == 1 && "$interaction_mode" == singleton ]]; then
  [[ -d "$session_cwd" ]] || die "Session directory not found: $session_cwd"
  session_cwd="$(cd "$session_cwd" && pwd -P)"
fi
amux_dir="${AMUX_HOME:-$HOME/.amux}"
amux_dir="$(canonical_directory "$amux_dir")"
export AMUX_HOME="$amux_dir"
validate_clean_directory
prepare_source
prepare_claude_sidecar
stage_payload
amux_bin="$amux_dir/bin/amux"
had_config=0
[[ ! -f "$amux_dir/config.toml" ]] || had_config=1
had_install=0
for entry in "$amux_dir"/* "$amux_dir"/.[!.]* "$amux_dir"/..?*; do
  if [[ -e "$entry" || -L "$entry" ]]; then had_install=1; break; fi
done
restore_autosync=0
stop_existing_installation
if [[ "$clean" == 1 && "$had_install" == 1 ]]; then
  progress "Uninstalling and deleting amux data in $amux_dir"
  # A CLI-only install may have been invoked from the directory being purged.
  cd "$stage_dir"
  # Use the staged CLI so uninstall remains available after removing bin/.
  "$source_dir/amux" system uninstall --yes --purge || die 'uninstall failed; new installation aborted'
  had_config=0
  restore_autosync=0
fi
install_cmd=("$source_dir/amux" install --source "$source_dir" --force)
[[ "$update_shell" == 0 ]] || install_cmd+=(--update-shell)
"${install_cmd[@]}"
export PATH="$amux_dir/bin:$PATH"
install_claude_sidecar
if [[ "$had_config" == 0 ]]; then "$amux_bin" setup local --no-start; fi
"$amux_bin" system config set bridge.feishu.workspace_defaults.agent "$agent_profile"
"$amux_bin" system start
wait_for_system
if [[ "$restore_autosync" == 1 ]]; then "$amux_bin" agent pack autosync start; fi
configure_feishu
"$amux_bin" system status
progress "Installed public amux with $agent_profile (runtime: $runtime)"
printf 'For this shell: export PATH=%q:$PATH\n' "$amux_dir/bin"
if [[ "$feishu" == 0 ]]; then
  next_cmd=(amux session run "$agent_profile" -n work --runtime "$runtime")
  [[ -z "$model" ]] || next_cmd+=(--model "$model")
  printf 'Start a Session:'
  printf ' %q' "${next_cmd[@]}"
  printf '\n'
fi
