#!/usr/bin/env bash
set -euo pipefail

repo=${1:-$(git rev-parse --show-toplevel)}
workflows=$repo/.github/workflows
failed=false

while IFS= read -r match; do
  if [[ $match =~ ^([^:]+):([0-9]+):(.*uses:[[:space:]]*([^[:space:]#]+)) ]]; then
    file=${BASH_REMATCH[1]#"$repo"/}
    line=${BASH_REMATCH[2]}
    action=${BASH_REMATCH[4]}
    if [[ $action == ./* || $action == docker://* ]]; then
      continue
    fi
    if [[ ! $action =~ ^[^/@[:space:]]+/[^/@[:space:]]+@[0-9a-f]{40}$ ]]; then
      echo "workflow-security: $file:$line does not pin $action to a full commit SHA" >&2
      failed=true
    fi
  fi
done < <(rg --no-heading --line-number 'uses:[[:space:]]*[^[:space:]#]+' "$workflows")

while IFS= read -r file; do
  relative=${file#"$repo"/}
  if [[ $relative != .github/workflows/deploy-staging.yml ]]; then
    echo "workflow-security: self-hosted runner referenced by $relative" >&2
    failed=true
  fi
done < <(rg --files-with-matches 'self-hosted' "$workflows")

if rg --quiet 'pull_request_target' "$workflows"; then
  echo "workflow-security: pull_request_target is forbidden in this public repository" >&2
  failed=true
fi

if "$failed"; then
  echo "workflow-security: FAILED" >&2
  exit 1
fi

echo "workflow-security: OK"
