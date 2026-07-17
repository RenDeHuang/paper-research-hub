#!/bin/sh

set -eu

compose() {
  docker compose -f docker-compose.yml "$@"
}

fail() {
  printf 'verify-local: %s\n' "$*" >&2
  exit 1
}

discover_api_url() {
  api_port="$(
    compose exec -T api sh -c 'printf "%s" "${API_PORT:-8080}"'
  )"
  binding="$(compose port api "${api_port}" | sed -n '1p')"
  [ -n "${binding}" ] || fail "cannot discover the published API port"
  printf 'http://localhost:%s' "${binding##*:}"
}

discover_web_url() {
  web_port="$(
    compose exec -T web sh -c 'printf "%s" "${PORT:-3000}"'
  )"
  binding="$(compose port web "${web_port}" | sed -n '1p')"
  [ -n "${binding}" ] || fail "cannot discover the published Web port"
  printf 'http://localhost:%s' "${binding##*:}"
}

normalize_origin() {
  origin="${1%/}"
  case "${origin}" in
    http://* | https://*) ;;
    *) fail "URL must be an explicit http:// or https:// origin: ${origin}" ;;
  esac
  authority="${origin#*://}"
  case "${authority}" in
    "" | */* | *\?* | *\#* | *@*)
      fail "URL must be an origin without path, query, fragment, or credentials: ${origin}"
      ;;
  esac
  printf '%s' "${origin}"
}

request() {
  name="$1"
  url="$2"
  origin="${3:-}"
  headers="${work_dir}/${name}.headers"
  body="${work_dir}/${name}.body"

  if [ -n "${origin}" ]; then
    if ! status="$(
      curl --silent --show-error --max-time 15 \
        --dump-header "${headers}" \
        --output "${body}" \
        --write-out '%{http_code}' \
        --header "Origin: ${origin}" \
        "${url}"
    )"; then
      fail "request failed: ${url}"
    fi
  else
    if ! status="$(
      curl --silent --show-error --max-time 15 \
        --dump-header "${headers}" \
        --output "${body}" \
        --write-out '%{http_code}' \
        "${url}"
    )"; then
      fail "request failed: ${url}"
    fi
  fi

  printf '%s' "${status}"
}

header_value() {
  headers="$1"
  header_name="$2"
  awk -F ': ' -v expected="${header_name}" '
    tolower($1) == tolower(expected) {
      sub(/\r$/, "", $2)
      print $2
      exit
    }
  ' "${headers}"
}

assert_content_type() {
  headers="$1"
  expected_prefix="$2"
  actual="$(header_value "${headers}" "Content-Type")"
  case "${actual}" in
    "${expected_prefix}"*) ;;
    *) fail "expected Content-Type ${expected_prefix}*, got ${actual:-missing}" ;;
  esac
}

api_url="$(normalize_origin "${API_URL:-$(discover_api_url)}")"
web_url="$(normalize_origin "${WEB_URL:-$(discover_web_url)}")"
[ "${api_url}" != "${web_url}" ] ||
  fail "API_URL and WEB_URL must be different origins"

work_dir="$(mktemp -d "${TMPDIR:-/tmp}/medpaperhub-verify-local.XXXXXX")"
trap 'find "${work_dir}" -type f -delete 2>/dev/null; rmdir "${work_dir}" 2>/dev/null' 0 1 2 3 15

api_root_status="$(request api-root "${api_url}/")"
[ "${api_root_status}" = "200" ] ||
  fail "API root returned ${api_root_status}, expected 200"
assert_content_type "${work_dir}/api-root.headers" "application/json"
[ -z "$(header_value "${work_dir}/api-root.headers" "Location")" ] ||
  fail "API root must not redirect to the Web service"
api_root_payload="$(cat "${work_dir}/api-root.body")"
[ "${api_root_payload}" = '{"service":"medpaperhub-api","status":"ok","api_version":"v1","health":"/health"}' ] ||
  fail "API root returned an unexpected discovery payload"

api_health_status="$(request api-health "${api_url}/health")"
[ "${api_health_status}" = "200" ] ||
  fail "API health returned ${api_health_status}, expected 200"
assert_content_type "${work_dir}/api-health.headers" "application/json"
[ "$(cat "${work_dir}/api-health.body")" = '{"status":"ok","service":"medpaperhub-api"}' ] ||
  fail "API health returned an unexpected payload"

cors_status="$(request api-cors "${api_url}/health" "${web_url}")"
[ "${cors_status}" = "200" ] ||
  fail "API CORS probe returned ${cors_status}, expected 200"
[ "$(header_value "${work_dir}/api-cors.headers" "Access-Control-Allow-Origin")" = "${web_url}" ] ||
  fail "API does not allow the configured Web origin ${web_url}"

api_probe_status="$(request api-boundary "${api_url}/__medpaperhub_web_boundary_probe__")"
[ "${api_probe_status}" = "404" ] ||
  fail "unknown API path returned ${api_probe_status}, expected 404"
if grep -Eiq '<!doctype html|<html[ >]' "${work_dir}/api-boundary.body"; then
  fail "API is serving an HTML page on an unknown path"
fi

catalog_status="$(request api-catalog "${api_url}/api/v1/home")"
case "${catalog_status}" in
  200)
    assert_content_type "${work_dir}/api-catalog.headers" "application/json"
    [ -n "$(header_value "${work_dir}/api-catalog.headers" "X-Catalog-Generation")" ] ||
      fail "published Catalog response is missing X-Catalog-Generation"
    ;;
  503)
    assert_content_type "${work_dir}/api-catalog.headers" "application/problem+json"
    grep -q '"code":"catalog_not_published"' "${work_dir}/api-catalog.body" ||
      fail "unpublished Catalog response has an unexpected problem code"
    ;;
  *)
    fail "Catalog root returned ${catalog_status}, expected 200 or catalog_not_published 503"
    ;;
esac

web_health_status="$(request web-health "${web_url}/health")"
[ "${web_health_status}" = "200" ] ||
  fail "Web health returned ${web_health_status}, expected 200"
assert_content_type "${work_dir}/web-health.headers" "application/json"
[ "$(cat "${work_dir}/web-health.body")" = '{"status":"ok","service":"medpaperhub-web"}' ] ||
  fail "Web health returned an unexpected payload"

web_root_status="$(request web-root "${web_url}/")"
[ "${web_root_status}" = "200" ] ||
  fail "Web root returned ${web_root_status}, expected 200"
assert_content_type "${work_dir}/web-root.headers" "text/html"
grep -qi 'medpaperhub' "${work_dir}/web-root.body" ||
  fail "Web root does not contain the medpaperhub application shell"

printf '%s\n' \
  "verify-local: PASS" \
  "  Web UI:       ${web_url}/ (Nuxt HTML)" \
  "  Web health:   ${web_url}/health" \
  "  API discovery:${api_url}/ (Go JSON)" \
  "  API health:   ${api_url}/health" \
  "  Catalog state:${catalog_status}"
