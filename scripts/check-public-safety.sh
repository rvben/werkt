#!/usr/bin/env bash
set -euo pipefail
# Git repositories may contain non-UTF-8 source. Scan bytes consistently on
# macOS and Linux instead of losing matches to locale-dependent text errors.
export LC_ALL=C

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
private_combined=""
if [[ -f "$local_patterns" ]]; then
  pattern_line=0
  while IFS= read -r pattern || [[ -n "$pattern" ]]; do
    pattern_line=$((pattern_line + 1))
    [[ -z "$pattern" || "$pattern" == \#* ]] && continue
    if grep -E -e "$pattern" </dev/null >/dev/null 2>&1; then
      :
    elif [[ $? -ne 1 ]]; then
      echo "public-safety: invalid private rule at line $pattern_line" >&2
      exit 2
    fi
    private_combined="${private_combined:+$private_combined|}($pattern)"
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

query_matches() {
  local pattern="$1" revision="$2" result status
  if [[ -n "$revision" ]]; then
    if result="$(git -C "$repo" grep -I -n -E "$pattern" "$revision" -- . "$excluded" 2>/dev/null)"; then
      printf '%s' "$result"
      return
    else
      status=$?
    fi
  else
    if result="$(git -C "$repo" grep -I -n -E "$pattern" -- . "$excluded" 2>/dev/null)"; then
      printf '%s' "$result"
      return
    else
      status=$?
    fi
  fi
  [[ "$status" -eq 1 ]] && return 0
  echo "public-safety: Git content scan failed (status $status)" >&2
  return "$status"
}

scan_tree() {
  local revision="${1:-}"
  local matches
  matches="$(query_matches "$combined" "$revision")" || { failed=true; return; }
  # These exact source constructs are not private hostnames or credentials.
  # Rescan the rest of each matching line: a fixture must not hide another hit.
  matches="$(while IFS= read -r match; do
    [[ -z "$match" ]] && continue
    sanitized="${match//Path.home()/python-home-call}"
    if ! sanitized="$(printf '%s\n' "$sanitized" | sed -E "s#https?://user:password@example[.]com([/?[:space:]'\",)]|$)#synthetic-credential-fixture\\1#g")"; then
      printf '%s\n' "$match"
      continue
    fi
    if grep -q -E "$combined" <<< "$sanitized"; then
      printf '%s\n' "$match"
    elif [[ $? -ne 1 ]]; then
      printf '%s\n' "$match"
    fi
  done <<< "$matches")"
  # Operator denylist rules always inspect the original, unmasked content.
  if [[ -n "$private_combined" ]]; then
    local private_matches
    private_matches="$(query_matches "$private_combined" "$revision")" || { failed=true; return; }
    matches="$(printf '%s\n%s\n' "$matches" "$private_matches" | sed '/^$/d' | sort -u)"
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
  revisions="$(git -C "$repo" rev-list --all)" || { echo "public-safety: history enumeration failed" >&2; exit 1; }
  while IFS= read -r revision; do
    [[ -z "$revision" ]] && continue
    scan_tree "$revision"
    scan_paths "$revision"
  done <<< "$revisions"
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
