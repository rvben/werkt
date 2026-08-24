#!/usr/bin/env bash
set -euo pipefail

: "${WERKT_STAGING_URL:?set WERKT_STAGING_URL}"
: "${WERKT_MANAGEMENT_TOKEN:?set WERKT_MANAGEMENT_TOKEN}"

base_url=${WERKT_STAGING_URL%/}
temporary_directory=$(mktemp -d)
trap 'rm -rf -- "$temporary_directory"' EXIT

request() {
  curl --silent --show-error --connect-timeout 5 --max-time 15 "$@"
}

ready_status=$(request -o "$temporary_directory/ready.json" -w '%{http_code}' "$base_url/readyz")
if [[ $ready_status != 200 ]]; then
  echo "readiness returned HTTP $ready_status" >&2
  exit 1
fi
if [[ -n ${WERKT_EXPECTED_COMMIT:-} ]] && ! grep -Fq "\"commit\":\"$WERKT_EXPECTED_COMMIT\"" "$temporary_directory/ready.json"; then
  echo "readiness did not report expected commit $WERKT_EXPECTED_COMMIT" >&2
  exit 1
fi

workspace_status=$(request -o "$temporary_directory/workspace.html" -w '%{http_code}' "$base_url/app/")
if [[ $workspace_status != 200 ]] || ! grep -Fq '<title>Werkt' "$temporary_directory/workspace.html"; then
  echo "workspace smoke check failed with HTTP $workspace_status" >&2
  exit 1
fi

unauthorized_status=$(request -o "$temporary_directory/unauthorized.json" -w '%{http_code}' "$base_url/api/v1/automations")
if [[ $unauthorized_status != 401 ]]; then
  echo "management API accepted an unauthenticated request: HTTP $unauthorized_status" >&2
  exit 1
fi

authorized_status=$(request \
  -H "Authorization: Bearer $WERKT_MANAGEMENT_TOKEN" \
  -o "$temporary_directory/automations.json" \
  -w '%{http_code}' \
  "$base_url/api/v1/automations")
if [[ $authorized_status != 200 ]]; then
  echo "authenticated inventory returned HTTP $authorized_status" >&2
  exit 1
fi

echo "staging smoke checks passed"
