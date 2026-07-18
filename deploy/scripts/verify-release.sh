#!/bin/sh

set -eu

DEPLOY_SCRIPT_NAME=verify-release
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

api_health_status="$(deploy_request api-health "${api_url}/health")"
[ "${api_health_status}" = "200" ] ||
  deploy_fail "API health returned ${api_health_status}, expected 200"
deploy_assert_content_type \
  "${deploy_http_dir}/api-health.headers" \
  "application/json"
[ "$(cat "${deploy_http_dir}/api-health.body")" = '{"status":"ok","service":"medpaperhub-api"}' ] ||
  deploy_fail "API health returned an unexpected payload"

catalog_status="$(deploy_request api-catalog "${api_url}/api/v1/home")"
[ "${catalog_status}" = "200" ] ||
  deploy_fail "expected published Catalog (HTTP 200), got ${catalog_status}"
deploy_assert_content_type \
  "${deploy_http_dir}/api-catalog.headers" \
  "application/json"
catalog_generation="$(
  deploy_header_value \
    "${deploy_http_dir}/api-catalog.headers" \
    "X-Catalog-Generation"
)"
[ -n "${catalog_generation}" ] ||
  deploy_fail "published Catalog response is missing X-Catalog-Generation"

web_health_status="$(deploy_request web-health "${web_url}/health")"
[ "${web_health_status}" = "200" ] ||
  deploy_fail "Web health returned ${web_health_status}, expected 200"
deploy_assert_content_type \
  "${deploy_http_dir}/web-health.headers" \
  "application/json"
[ "$(cat "${deploy_http_dir}/web-health.body")" = '{"status":"ok","service":"medpaperhub-web"}' ] ||
  deploy_fail "Web health returned an unexpected payload"

web_root_status="$(deploy_request web-root "${web_url}/")"
[ "${web_root_status}" = "200" ] ||
  deploy_fail "Web root returned ${web_root_status}, expected 200"
deploy_assert_content_type \
  "${deploy_http_dir}/web-root.headers" \
  "text/html"
grep -qi 'medpaperhub' "${deploy_http_dir}/web-root.body" ||
  deploy_fail "Web root does not contain the medpaperhub application shell"

printf '%s\n' \
  "verify-release: PASS" \
  "  API: ${api_url}" \
  "  Web: ${web_url}" \
  "  Catalog generation: ${catalog_generation}"
