#!/bin/sh

deploy_script_name="${DEPLOY_SCRIPT_NAME:-deploy}"
deploy_repo_root="$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)"

deploy_fail() {
  printf '%s: %s\n' "${deploy_script_name}" "$*" >&2
  exit 1
}

deploy_validate_local_env_file() {
  case "${LOCAL_ENV_FILE}" in
    /*) ;;
    *) deploy_fail "LOCAL_ENV_FILE must be an absolute path" ;;
  esac
  [ -f "${LOCAL_ENV_FILE}" ] ||
    deploy_fail "LOCAL_ENV_FILE does not exist: ${LOCAL_ENV_FILE}"
}

deploy_validate_compose_project_name() {
  project_name="$1"
  case "${project_name}" in
    [a-z0-9]*) ;;
    *) deploy_fail "invalid COMPOSE_PROJECT_NAME: ${project_name:-empty}" ;;
  esac
  case "${project_name}" in
    *[!a-z0-9_-]*)
      deploy_fail "invalid COMPOSE_PROJECT_NAME: ${project_name}"
      ;;
  esac
  printf '%s' "${project_name}"
}

deploy_default_compose_project_name() {
  project_repo_path="$1"
  case "${project_repo_path}" in
    /*) ;;
    *) deploy_fail "repo path must be absolute: ${project_repo_path}" ;;
  esac
  project_repo_checksum="$(
    printf '%s' "${project_repo_path}" | cksum | awk '{print $1}'
  )" || deploy_fail "cannot calculate repo path cksum"
  [ -n "${project_repo_checksum}" ] ||
    deploy_fail "cannot calculate repo path cksum"
  printf 'paper-research-hub-empty-%s' "${project_repo_checksum}"
}

deploy_local_env_project_name() {
  deploy_validate_local_env_file
  local_env_project_name=
  local_env_project_status=0
  local_env_project_name="$(
    awk '
      /^[[:space:]]*($|#)/ {
        next
      }
      /^COMPOSE_PROJECT_NAME=/ {
        count++
        if (count > 1) {
          exit 2
        }
        print substr($0, length("COMPOSE_PROJECT_NAME=") + 1)
        next
      }
      /^[[:space:]]*COMPOSE_PROJECT_NAME/ {
        exit 3
      }
    ' "${LOCAL_ENV_FILE}"
  )" || local_env_project_status="$?"
  case "${local_env_project_status}" in
    0) ;;
    2)
      deploy_fail "LOCAL_ENV_FILE COMPOSE_PROJECT_NAME must be defined at most once"
      ;;
    *)
      deploy_fail "LOCAL_ENV_FILE contains an invalid COMPOSE_PROJECT_NAME assignment"
      ;;
  esac
  printf '%s' "${local_env_project_name}"
}

deploy_resolve_compose_project_name() {
  if [ -n "${COMPOSE_PROJECT_NAME:-}" ]; then
    deploy_validate_compose_project_name "${COMPOSE_PROJECT_NAME}"
    return
  fi

  if [ -n "${LOCAL_ENV_FILE:-}" ]; then
    env_project_name="$(deploy_local_env_project_name)"
    if [ -n "${env_project_name}" ]; then
      deploy_validate_compose_project_name "${env_project_name}"
      return
    fi
  fi

  deploy_default_compose_project_name "${deploy_repo_root}"
}

deploy_compose() {
  if [ -n "${LOCAL_ENV_FILE:-}" ]; then
    deploy_validate_local_env_file
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
  expected_media_type="$2"
  actual="$(deploy_header_value "${headers}" "Content-Type")"
  actual_media_type="${actual%%;*}"
  actual_media_type="$(
    printf '%s' "${actual_media_type}" |
      sed \
        -e 's/^[[:space:]]*//' \
        -e 's/[[:space:]]*$//' |
      LC_ALL=C tr '[:upper:]' '[:lower:]'
  )"
  expected_media_type="$(
    printf '%s' "${expected_media_type}" |
      LC_ALL=C tr '[:upper:]' '[:lower:]'
  )"
  [ "${actual_media_type}" = "${expected_media_type}" ] ||
    deploy_fail "expected Content-Type ${expected_media_type}, got ${actual:-missing}"
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
