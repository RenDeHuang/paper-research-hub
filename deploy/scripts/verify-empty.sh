#!/bin/sh

set -eu

DEPLOY_SCRIPT_NAME=verify-empty
export DEPLOY_SCRIPT_NAME
. "$(dirname "$0")/lib.sh"

if [ -n "${API_URL:-}" ]; then
  api_url="$(deploy_normalize_origin "${API_URL}")"
else
  api_url="$(deploy_normalize_origin "$(deploy_discover_api_url)")"
fi
if [ -n "${WEB_URL:-}" ]; then
  web_url="$(deploy_normalize_origin "${WEB_URL}")"
else
  web_url="$(deploy_normalize_origin "$(deploy_discover_web_url)")"
fi
[ "${api_url}" != "${web_url}" ] ||
  deploy_fail "API_URL and WEB_URL must be different origins"

deploy_make_http_dir
trap deploy_cleanup_http_dir 0 1 2 3 15

deploy_assert_health_boundaries "${api_url}" "${web_url}"

catalog_status="$(deploy_request api-catalog "${api_url}/api/v1/home")"
[ "${catalog_status}" = "503" ] ||
  deploy_fail "expected empty Catalog (HTTP 503), got ${catalog_status}"
deploy_assert_content_type \
  "${deploy_http_dir}/api-catalog.headers" \
  "application/problem+json"
grep -q '"code":"catalog_not_published"' \
  "${deploy_http_dir}/api-catalog.body" ||
  deploy_fail "empty Catalog response has an unexpected problem code"

printf '%s\n' \
  "verify-empty: PASS" \
  "  API: ${api_url}" \
  "  Web: ${web_url}" \
  "  Catalog: 503 catalog_not_published"
