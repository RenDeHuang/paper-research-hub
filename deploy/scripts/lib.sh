#!/bin/sh

deploy_script_name="${DEPLOY_SCRIPT_NAME:-deploy}"
deploy_repo_root="$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)"

deploy_fail() {
  printf '%s: %s\n' "${deploy_script_name}" "$*" >&2
  exit 1
}

deploy_compose() {
  if [ -n "${LOCAL_ENV_FILE:-}" ]; then
    case "${LOCAL_ENV_FILE}" in
      /*) ;;
      *) deploy_fail "LOCAL_ENV_FILE must be an absolute path" ;;
    esac
    [ -f "${LOCAL_ENV_FILE}" ] ||
      deploy_fail "LOCAL_ENV_FILE does not exist: ${LOCAL_ENV_FILE}"
    docker compose \
      --project-directory "${deploy_repo_root}" \
      --env-file "${LOCAL_ENV_FILE}" \
      -f "${deploy_repo_root}/docker-compose.yml" \
      -f "${deploy_repo_root}/deploy/compose/compose.local.yml" \
      "$@"
  else
    docker compose \
      --project-directory "${deploy_repo_root}" \
      -f "${deploy_repo_root}/docker-compose.yml" \
      -f "${deploy_repo_root}/deploy/compose/compose.local.yml" \
      "$@"
  fi
}

deploy_discover_api_url() {
  container_port="$(
    deploy_compose exec -T api sh -c 'printf "%s" "${API_PORT:-8080}"'
  )" || deploy_fail "cannot read the API container port"
  binding="$(deploy_compose port api "${container_port}" | sed -n '1p')" ||
    deploy_fail "cannot discover the published API port"
  [ -n "${binding}" ] ||
    deploy_fail "cannot discover the published API port"
  printf 'http://localhost:%s' "${binding##*:}"
}

deploy_discover_web_url() {
  container_port="$(
    deploy_compose exec -T web sh -c 'printf "%s" "${PORT:-3000}"'
  )" || deploy_fail "cannot read the Web container port"
  binding="$(deploy_compose port web "${container_port}" | sed -n '1p')" ||
    deploy_fail "cannot discover the published Web port"
  [ -n "${binding}" ] ||
    deploy_fail "cannot discover the published Web port"
  printf 'http://localhost:%s' "${binding##*:}"
}

deploy_normalize_origin() {
  origin="${1%/}"
  case "${origin}" in
    http://* | https://*) ;;
    *) deploy_fail "URL must use an explicit http:// or https:// origin: ${origin}" ;;
  esac
  authority="${origin#*://}"
  case "${authority}" in
    "" | */* | *\?* | *\#* | *@*)
      deploy_fail "URL must be an origin without path, query, fragment, or credentials: ${origin}"
      ;;
  esac
  printf '%s' "${origin}"
}

deploy_make_http_dir() {
  deploy_http_dir="$(
    mktemp -d "${TMPDIR:-/tmp}/medpaperhub-deploy-http.XXXXXX"
  )" || deploy_fail "cannot create HTTP verification directory"
}

deploy_cleanup_http_dir() {
  if [ -n "${deploy_http_dir:-}" ] && [ -d "${deploy_http_dir}" ]; then
    find "${deploy_http_dir}" -type f -delete 2>/dev/null || true
    rmdir "${deploy_http_dir}" 2>/dev/null || true
  fi
}

deploy_request() {
  request_name="$1"
  request_url="$2"
  request_origin="${3:-}"
  request_headers="${deploy_http_dir}/${request_name}.headers"
  request_body="${deploy_http_dir}/${request_name}.body"

  if [ -n "${request_origin}" ]; then
    if ! request_status="$(
      curl --silent --show-error --max-time 15 \
        --dump-header "${request_headers}" \
        --output "${request_body}" \
        --write-out '%{http_code}' \
        --header "Origin: ${request_origin}" \
        "${request_url}"
    )"; then
      deploy_fail "request failed: ${request_url}"
    fi
  else
    if ! request_status="$(
      curl --silent --show-error --max-time 15 \
        --dump-header "${request_headers}" \
        --output "${request_body}" \
        --write-out '%{http_code}' \
        "${request_url}"
    )"; then
      deploy_fail "request failed: ${request_url}"
    fi
  fi

  printf '%s' "${request_status}"
}

deploy_header_value() {
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

deploy_assert_content_type() {
  headers="$1"
  expected_prefix="$2"
  actual="$(deploy_header_value "${headers}" "Content-Type")"
  case "${actual}" in
    "${expected_prefix}"*) ;;
    *)
      deploy_fail "expected Content-Type ${expected_prefix}*, got ${actual:-missing}"
      ;;
  esac
}

deploy_assert_health_boundaries() {
  api_origin="$1"
  web_origin="$2"

  api_health_status="$(deploy_request api-health "${api_origin}/health")"
  [ "${api_health_status}" = "200" ] ||
    deploy_fail "API health returned ${api_health_status}, expected 200"
  deploy_assert_content_type \
    "${deploy_http_dir}/api-health.headers" \
    "application/json"
  [ "$(cat "${deploy_http_dir}/api-health.body")" = '{"status":"ok","service":"medpaperhub-api"}' ] ||
    deploy_fail "API health returned an unexpected payload"

  web_health_status="$(deploy_request web-health "${web_origin}/health")"
  [ "${web_health_status}" = "200" ] ||
    deploy_fail "Web health returned ${web_health_status}, expected 200"
  deploy_assert_content_type \
    "${deploy_http_dir}/web-health.headers" \
    "application/json"
  [ "$(cat "${deploy_http_dir}/web-health.body")" = '{"status":"ok","service":"medpaperhub-web"}' ] ||
    deploy_fail "Web health returned an unexpected payload"

  web_root_status="$(deploy_request web-root "${web_origin}/")"
  [ "${web_root_status}" = "200" ] ||
    deploy_fail "Web root returned ${web_root_status}, expected 200"
  deploy_assert_content_type \
    "${deploy_http_dir}/web-root.headers" \
    "text/html"
  grep -qi 'medpaperhub' "${deploy_http_dir}/web-root.body" ||
    deploy_fail "Web root does not contain the medpaperhub application shell"
}
