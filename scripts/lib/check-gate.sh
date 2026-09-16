#!/usr/bin/env bash
# Shared by the public gate and the internal repository that consumes it.
# Git is used read-only: neither the real index nor a temporary index is written.

check_gate_quote_path() {
  local value="$1"
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  value="${value//$'\n'/\\n}"
  value="${value//$'\r'/\\r}"
  value="${value//$'\t'/\\t}"
  value="${value//$'\b'/\\b}"
  value="${value//$'\f'/\\f}"
  value="${value//$'\v'/\\v}"
  value="${value//$'\a'/\\a}"
  printf '"%s"' "$value"
}

check_gate_snapshot_source() (
  local source_root="$1" source_prefix source_path quoted_path manifest_dir digest
  git -C "$source_root" rev-parse --show-toplevel >/dev/null 2>&1 || return 1
  source_prefix="$(git -C "$source_root" rev-parse --show-prefix)"
  if [[ -n "$source_prefix" ]]; then
    # Do not mistake an ignored/unversioned dependency directory for an empty
    # subtree of its parent Git repository.
    git -C "$source_root" rev-parse "HEAD:${source_prefix%/}" >/dev/null 2>&1 || return 1
  fi
  manifest_dir="$(mktemp -d "${TMPDIR:-/tmp}/amux-check-tree.XXXXXX")" || return 1
  trap 'rm -rf -- "$manifest_dir"' EXIT
  cd "$source_root" || return 1
  git ls-files --cached --others --exclude-standard -z > "$manifest_dir/files" || return 1
  : > "$manifest_dir/paths"
  : > "$manifest_dir/metadata"
  while IFS= read -r -d '' source_path; do
    quoted_path="$(check_gate_quote_path "$source_path")"
    if [[ -L "$source_path" ]]; then
      digest="$(readlink "$source_path" | git hash-object --stdin)" || return 1
      printf 'symlink %s %s\n' "$quoted_path" "$digest" >> "$manifest_dir/metadata"
    elif [[ -f "$source_path" ]]; then
      check_gate_quote_path "$source_root/$source_path" >> "$manifest_dir/paths"
      printf '\n' >> "$manifest_dir/paths"
      if [[ -x "$source_path" ]]; then
        printf 'executable %s\n' "$quoted_path" >> "$manifest_dir/metadata"
      else
        printf 'file %s\n' "$quoted_path" >> "$manifest_dir/metadata"
      fi
    elif [[ -d "$source_path" && "$(git ls-files --stage -- "$source_path")" == 160000\ * ]]; then
      # Include the actual dependency files, including uncommitted edits. A
      # gitlink alone does not describe the source that Go will compile.
      [[ -z "$(git -C "$source_path" rev-parse --show-prefix)" ]] || return 1
      digest="$(check_gate_snapshot_source "$source_root/$source_path")" || return 1
      printf 'submodule %s %s\n' "$quoted_path" "$digest" >> "$manifest_dir/metadata"
    elif [[ -e "$source_path" ]]; then
      # Other special files cannot be fingerprinted reliably.
      return 1
    fi
  done < "$manifest_dir/files"
  # Sorting makes the snapshot depend only on current files: committing or
  # staging the same tree must not change its receipt. Batch content hashing
  # avoids starting a Git process for every tracked file.
  {
    printf 'root=%s\n' "$source_root"
    LC_ALL=C sort -u "$manifest_dir/metadata" || return 1
    LC_ALL=C sort -u "$manifest_dir/paths" | git hash-object --no-filters --stdin-paths || return 1
  } | git hash-object --stdin
)

check_gate_snapshot() {
  local source_root source_digest
  for source_root in "${check_gate_sources[@]}"; do
    source_digest="$(check_gate_snapshot_source "$source_root")" || return 1
    printf '%s\n' "$source_digest"
  done | git hash-object --stdin
}

check_gate_cleanup() {
  local status=$? owner_pid="" owner_token=""
  trap - EXIT
  if [[ -n "${check_gate_tmp:-}" ]]; then rm -rf -- "$check_gate_tmp"; fi
  if [[ "${check_gate_acquired:-false}" == true ]]; then
    if [[ -f "$check_gate_owner" ]]; then read -r owner_pid owner_token < "$check_gate_owner" || true; fi
    if [[ "$owner_pid" == "$$" && "$owner_token" == "$check_gate_token" ]]; then rm -rf -- "$check_gate_lock"; fi
  fi
  exit "$status"
}

check_gate_begin() {
  local module_root="$1" mode="$2" force="$3" git_dir lock_root module_key
  shift 3
  check_gate_sources=("$module_root" "$@")
  check_gate_started=$SECONDS
  check_gate_environment="$(env | LC_ALL=C sort | git hash-object --stdin)"
  module_key="$(printf '%s' "$module_root" | git hash-object --stdin)"
  if git_dir="$(git -C "$module_root" rev-parse --absolute-git-dir 2>/dev/null)"; then
    lock_root="$git_dir/amux-check/$module_key"
  else
    # Source archives still run the complete gate, without reusable receipts.
    lock_root="${TMPDIR:-/tmp}/amux-check-$module_key"
  fi
  check_gate_lock="$lock_root/lock"
  check_gate_owner="$check_gate_lock/owner"
  check_gate_token="$$-${RANDOM}-$module_key"
  check_gate_acquired=false
  check_gate_tmp=""
  trap check_gate_cleanup EXIT
  if [[ -L "$lock_root" || ( -e "$lock_root" && ! -d "$lock_root" ) ]]; then
    printf 'invalid scripts/check.sh repository lock root: %s\n' "$lock_root" >&2
    exit 1
  fi
  mkdir -p "$lock_root"
  if mkdir "$check_gate_lock" 2>/dev/null; then
    printf '%s %s\n' "$$" "$check_gate_token" > "$check_gate_lock/.owner.$$"
    mv "$check_gate_lock/.owner.$$" "$check_gate_owner"
    check_gate_acquired=true
  else
    local owner_pid="" owner_token="" attempt
    if [[ -L "$check_gate_lock" || ! -d "$check_gate_lock" ]]; then
      printf 'invalid scripts/check.sh lock path: %s\n' "$check_gate_lock" >&2
      exit 1
    fi
    for attempt in {1..20}; do
      if [[ -s "$check_gate_owner" ]]; then read -r owner_pid owner_token < "$check_gate_owner" || true; break; fi
      sleep 0.05
    done
    if [[ -z "$owner_pid" || -z "$owner_token" ]]; then
      printf 'scripts/check.sh lock exists without a readable owner: %s\n' "$check_gate_lock" >&2
    elif kill -0 "$owner_pid" 2>/dev/null; then
      printf 'another scripts/check.sh is already running for this worktree module (pid %s)\n' "$owner_pid" >&2
    else
      printf 'stale scripts/check.sh lock for pid %s: remove %s after confirming no gate is running\n' "$owner_pid" "$check_gate_lock" >&2
    fi
    exit 1
  fi
  check_gate_receipt="$lock_root/success.v2.$mode"
  check_gate_cacheable=true
  if ! check_gate_tree="$(check_gate_snapshot)"; then
    check_gate_cacheable=false
    printf 'Source state cannot be fingerprinted reliably; running without a reusable verification receipt.\n'
  fi
  local receipt_tree="" receipt_environment=""
  if [[ "$check_gate_cacheable" == true && -f "$check_gate_receipt" ]]; then
    receipt_tree="$(sed -n 's/^tree=//p' "$check_gate_receipt")"
    receipt_environment="$(sed -n 's/^environment=//p' "$check_gate_receipt")"
  fi
  if [[ "$force" == false && "$check_gate_cacheable" == true && "$receipt_tree" == "$check_gate_tree" && "$receipt_environment" == "$check_gate_environment" ]]; then
    printf 'Reusing successful verification receipt for tree %s. Use --force to run again.\n' "$check_gate_tree"
    exit 0
  fi
  # A failed forced run must not leave an earlier success eligible for reuse.
  rm -f -- "$check_gate_receipt"
  check_gate_tmp="$(mktemp -d "${TMPDIR:-/tmp}/amux-check.XXXXXX")"
}

check_gate_finish() {
  local ending_tree receipt_tmp
  if [[ "$check_gate_cacheable" == true ]]; then
    ending_tree="$(check_gate_snapshot)" || {
      printf 'Verification changed the worktree or dependency into an untrackable source state.\n' >&2
      return 1
    }
    if [[ "$ending_tree" != "$check_gate_tree" ]]; then
      printf 'Verification changed the worktree or dependency: started at %s, ended at %s. Review changes and run the gate again.\n' "$check_gate_tree" "$ending_tree" >&2
      return 1
    fi
    receipt_tmp="$check_gate_receipt.$$"
    {
      printf 'tree=%s\nenvironment=%s\n' "$check_gate_tree" "$check_gate_environment"
      printf 'verified_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    } > "$receipt_tmp"
    mv "$receipt_tmp" "$check_gate_receipt"
  fi
  printf '\nVerification timing: %ss total.\nAll checks passed.\n' "$((SECONDS - check_gate_started))"
}
