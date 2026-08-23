#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 [--history] [repository]" >&2
}

scan_history=false
if [[ "${1:-}" == "--history" ]]; then
  scan_history=true
  shift
fi
if [[ $# -gt 1 ]]; then
  usage
  exit 2
fi

repo="${1:-$(git rev-parse --show-toplevel)}"
if ! git -C "$repo" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  echo "public-safety: not a Git repository: $repo" >&2
  exit 2
fi

# These expressions are intentionally generic. Organization-specific values live
# in the ignored .public-safety-denylist file, one extended regular expression per
# line, so the denylist itself never becomes public metadata.
patterns=(
  '(^|[^0-9])(10\.([0-9]{1,3}\.){2}[0-9]{1,3}|192\.168\.[0-9]{1,3}\.[0-9]{1,3}|172\.(1[6-9]|2[0-9]|3[01])\.[0-9]{1,3}\.[0-9]{1,3})([^0-9]|$)'
  '/Users/[A-Za-z0-9._-]+/'
  '/home/[A-Za-z0-9._-]+/'
  '([A-Za-z0-9-]+\.)+(local|lan|home|internal|corp)(:[0-9]+)?([^A-Za-z0-9.-]|$)'
  'https?://[^/@[:space:]]+:[^/@[:space:]]+@'
  '-----BEGIN ([A-Z0-9 ]+ )?PRIVATE KEY-----'
  '(^|[^A-Za-z0-9])(AKIA[0-9A-Z]{16}|gh[pousr]_[A-Za-z0-9]{30,}|glpat-[A-Za-z0-9_-]{20,}|xox[baprs]-[A-Za-z0-9-]{20,}|sk-(proj-)?[A-Za-z0-9_-]{20,})([^A-Za-z0-9]|$)'
)

local_patterns="$repo/.public-safety-denylist"
if [[ -f "$local_patterns" ]]; then
  while IFS= read -r pattern || [[ -n "$pattern" ]]; do
    [[ -z "$pattern" || "$pattern" == \#* ]] && continue
    patterns+=("$pattern")
  done < "$local_patterns"
fi

combined=""
for pattern in "${patterns[@]}"; do
  if [[ -z "$combined" ]]; then
    combined="($pattern)"
  else
    combined="$combined|($pattern)"
  fi
done

excluded=':(exclude)scripts/check-public-safety.sh'
failed=false

scan_tree() {
  local revision="${1:-}"
  local matches
  if [[ -n "$revision" ]]; then
    matches="$(git -C "$repo" grep -I -n -E "$combined" "$revision" -- . "$excluded" 2>/dev/null || true)"
  else
    matches="$(git -C "$repo" grep -I -n -E "$combined" -- . "$excluded" 2>/dev/null || true)"
  fi
  if [[ -n "$matches" ]]; then
    echo "public-safety: sensitive-looking tracked content${revision:+ in $revision}:" >&2
    echo "$matches" >&2
    failed=true
  fi
}

scan_paths() {
  local revision="${1:-}"
  local paths
  if [[ -n "$revision" ]]; then
    paths="$(git -C "$repo" ls-tree -r --name-only "$revision")"
  else
    paths="$(git -C "$repo" ls-files)"
  fi
  local matches
  matches="$(printf '%s\n' "$paths" | grep -E '^(automations|docs|reports)/.*(private|homelab|internal|migration-evidence)' || true)"
  if [[ -n "$matches" ]]; then
    echo "public-safety: environment-specific tracked paths${revision:+ in $revision}:" >&2
    echo "$matches" >&2
    failed=true
  fi
}

if "$scan_history"; then
  while IFS= read -r revision; do
    scan_tree "$revision"
    scan_paths "$revision"
  done < <(git -C "$repo" rev-list --all)
else
  scan_tree
  scan_paths
fi

if "$failed"; then
  echo "public-safety: FAILED" >&2
  exit 1
fi

scope="tracked tree"
"$scan_history" && scope="all reachable commits"
echo "public-safety: OK ($scope)"
