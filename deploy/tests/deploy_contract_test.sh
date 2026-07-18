#!/bin/sh

set -eu

repo_root="$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)"
cd "${repo_root}"

fail() {
  printf 'deploy-contract: FAIL: %s\n' "$*" >&2
  exit 1
}

assert_fails_with() {
  label="$1"
  expected="$2"
  shift 2
  failure_index=$((failure_index + 1))
  output="${test_tmp}/failure-${failure_index}.log"

  if "$@" >"${output}" 2>&1; then
    fail "${label}: command unexpectedly succeeded"
  fi
  grep -F "${expected}" "${output}" >/dev/null ||
    fail "${label}: expected output containing '${expected}', got: $(cat "${output}")"
}

required_files='
Makefile
.gitleaks.toml
.github/workflows/container.yml
deploy/README.md
deploy/compose/compose.local.yml
deploy/env/local.env.example
deploy/env/pipeline.env.example
deploy/manifests/replay.example.yaml
deploy/scripts/lib.sh
deploy/scripts/up-empty.sh
deploy/scripts/verify-empty.sh
deploy/scripts/local-release.sh
deploy/scripts/verify-release.sh
deploy/sql/verify-release.sql
deploy/tests/deploy_contract_test.sh
'

for required_file in ${required_files}; do
  [ -f "${required_file}" ] ||
    fail "missing required file: ${required_file}"
done

test_tmp="$(mktemp -d "${TMPDIR:-/tmp}/medpaperhub-deploy-contract.XXXXXX")"
trap 'find "${test_tmp}" -type f -delete 2>/dev/null; find "${test_tmp}" -depth -type d -exec rmdir {} \; 2>/dev/null' 0 1 2 3 15
failure_index=0

grep -Eq '^[[:space:]]*useDefault[[:space:]]*=[[:space:]]*true[[:space:]]*$' \
  .gitleaks.toml ||
  fail ".gitleaks.toml must extend the Gitleaks default rules"
grep -F 'development-only-' .gitleaks.toml >/dev/null ||
  fail ".gitleaks.toml must allow the explicit development-only placeholder prefix"
grep -F 'test-' .gitleaks.toml >/dev/null ||
  fail ".gitleaks.toml must allow the explicit test placeholder prefix"
if grep -Eq '^[[:space:]]*paths[[:space:]]*=' .gitleaks.toml; then
  fail ".gitleaks.toml must not bypass scanning with a file allowlist"
fi
grep -F 'uses: gitleaks/gitleaks-action@v3' \
  .github/workflows/container.yml >/dev/null ||
  fail "container workflow must run the official Gitleaks v3 action"
grep -F 'GITLEAKS_CONFIG: .gitleaks.toml' \
  .github/workflows/container.yml >/dev/null ||
  fail "container workflow must load the repository Gitleaks configuration"
legacy_secret_scanner='deploy_contract_secret_''value_is_forbidden'
if grep -F "${legacy_secret_scanner}" deploy/tests/deploy_contract_test.sh >/dev/null; then
  fail "deploy contract must delegate repository secret scanning to Gitleaks"
fi

for shell_file in deploy/scripts/*.sh deploy/tests/*.sh; do
  sh -n "${shell_file}" ||
    fail "invalid POSIX shell syntax: ${shell_file}"
done

project_path_a="${test_tmp}/repo-a"
project_path_b="${test_tmp}/repo-b"
if ! project_name_a="$(
  sh -c \
    '. "$1"; deploy_default_compose_project_name "$2"' \
    "${repo_root}/deploy/scripts/project-name-probe" \
    "${repo_root}/deploy/scripts/lib.sh" \
    "${project_path_a}"
)"; then
  fail "deploy library must derive a default Compose project name from a repo path"
fi
project_name_a_again="$(
  sh -c \
    '. "$1"; deploy_default_compose_project_name "$2"' \
    "${repo_root}/deploy/scripts/project-name-probe" \
    "${repo_root}/deploy/scripts/lib.sh" \
    "${project_path_a}"
)"
project_name_b="$(
  sh -c \
    '. "$1"; deploy_default_compose_project_name "$2"' \
    "${repo_root}/deploy/scripts/project-name-probe" \
    "${repo_root}/deploy/scripts/lib.sh" \
    "${project_path_b}"
)"
[ "${project_name_a}" = "${project_name_a_again}" ] ||
  fail "default Compose project name must be stable for the same repo path"
[ "${project_name_a}" != "${project_name_b}" ] ||
  fail "different repo paths must not share the default Compose project name"
project_checksum_a="$(printf '%s' "${project_path_a}" | cksum | awk '{print $1}')"
[ "${project_name_a}" = "paper-research-hub-empty-${project_checksum_a}" ] ||
  fail "default Compose project name must include the repo path cksum"

if grep -n '^[[:space:]]*command:' deploy/compose/compose.local.yml >/dev/null; then
  fail "compose.local.yml must not duplicate canonical service commands"
fi

docker compose \
  -f docker-compose.yml \
  -f deploy/compose/compose.local.yml \
  config --quiet ||
  fail "root Compose model and local overlay do not merge"

for env_example in deploy/env/local.env.example deploy/env/pipeline.env.example; do
  awk '
    /^[[:space:]]*#/ || /^[[:space:]]*$/ {
      next
    }
    $0 !~ /^[A-Z][A-Z0-9_]*=<[^<>]+>$/ {
      exit 1
    }
  ' "${env_example}" ||
    fail "${env_example} must contain only VARIABLE=<placeholder> entries"
done

for local_secret_file in deploy/env/.env.local deploy/env/.env.pipeline; do
  git check-ignore -q "${local_secret_file}" ||
    fail "operator secret file must be ignored by Git: ${local_secret_file}"
done
grep -F 'path: ./deploy/env/.env.pipeline' \
  deploy/compose/compose.local.yml >/dev/null ||
  fail "local Compose overlay must read the ignored pipeline environment file"

grep -F 'count(paper.paper_id)' deploy/sql/verify-release.sql >/dev/null ||
  fail "release SQL must count the public_catalog_papers primary-key column"

json_with_charset_headers="${test_tmp}/json-with-charset.headers"
printf 'Content-Type: Application/JSON; Charset=UTF-8\r\n' \
  >"${json_with_charset_headers}"
sh -c \
  '. "$1"; deploy_assert_content_type "$2" "$3"' \
  "${repo_root}/deploy/scripts/content-type-probe" \
  "${repo_root}/deploy/scripts/lib.sh" \
  "${json_with_charset_headers}" \
  "application/json" ||
  fail "Content-Type comparison must be case-insensitive and allow parameters"

html_with_charset_headers="${test_tmp}/html-with-charset.headers"
printf 'Content-Type: Text/HTML; charset=utf-8\r\n' \
  >"${html_with_charset_headers}"
sh -c \
  '. "$1"; deploy_assert_content_type "$2" "$3"' \
  "${repo_root}/deploy/scripts/content-type-probe" \
  "${repo_root}/deploy/scripts/lib.sh" \
  "${html_with_charset_headers}" \
  "text/html" ||
  fail "HTML Content-Type comparison must be case-insensitive and allow parameters"

jsonp_headers="${test_tmp}/jsonp.headers"
printf 'Content-Type: application/jsonp\r\n' >"${jsonp_headers}"
assert_fails_with \
  "Content-Type rejects application/jsonp" \
  "expected Content-Type application/json" \
  sh -c \
    '. "$1"; deploy_assert_content_type "$2" "$3"' \
    "${repo_root}/deploy/scripts/content-type-probe" \
    "${repo_root}/deploy/scripts/lib.sh" \
    "${jsonp_headers}" \
    "application/json"

htmlx_headers="${test_tmp}/htmlx.headers"
printf 'Content-Type: text/htmlx\r\n' >"${htmlx_headers}"
assert_fails_with \
  "Content-Type rejects text/htmlx" \
  "expected Content-Type text/html" \
  sh -c \
    '. "$1"; deploy_assert_content_type "$2" "$3"' \
    "${repo_root}/deploy/scripts/content-type-probe" \
    "${repo_root}/deploy/scripts/lib.sh" \
    "${htmlx_headers}" \
    "text/html"

assert_fails_with \
  "missing manifest" \
  "MANIFEST is required" \
  sh -c 'unset MANIFEST; sh deploy/scripts/local-release.sh'

relative_manifest="relative.yaml"
assert_fails_with \
  "relative manifest" \
  "MANIFEST must be an absolute path" \
  env MANIFEST="${relative_manifest}" sh deploy/scripts/local-release.sh

missing_manifest="${test_tmp}/missing.yaml"
assert_fails_with \
  "nonexistent manifest" \
  "manifest does not exist" \
  env MANIFEST="${missing_manifest}" sh deploy/scripts/local-release.sh

jcr_input="${test_tmp}/authorized-jcr.csv"
replay_input="${test_tmp}/fixed-input.ndjson"
: >"${jcr_input}"
: >"${replay_input}"

missing_input_manifest="${test_tmp}/missing-input.yaml"
cat >"${missing_input_manifest}" <<EOF
MANIFEST_VERSION: 1
JCR_IMPORT_PATH: ${jcr_input}
EOF
assert_fails_with \
  "manifest missing input path" \
  "REPLAY_INPUT_PATH is required" \
  env MANIFEST="${missing_input_manifest}" sh deploy/scripts/local-release.sh

relative_input_manifest="${test_tmp}/relative-input.yaml"
cat >"${relative_input_manifest}" <<EOF
MANIFEST_VERSION: 1
JCR_IMPORT_PATH: ${jcr_input}
REPLAY_INPUT_PATH: relative/input.ndjson
EOF
assert_fails_with \
  "manifest relative input path" \
  "REPLAY_INPUT_PATH must be an absolute path" \
  env MANIFEST="${relative_input_manifest}" sh deploy/scripts/local-release.sh

missing_path_manifest="${test_tmp}/missing-path.yaml"
cat >"${missing_path_manifest}" <<EOF
MANIFEST_VERSION: 1
JCR_IMPORT_PATH: ${test_tmp}/missing-jcr.csv
REPLAY_INPUT_PATH: ${replay_input}
EOF
assert_fails_with \
  "manifest nonexistent input path" \
  "JCR_IMPORT_PATH does not exist" \
  env MANIFEST="${missing_path_manifest}" sh deploy/scripts/local-release.sh

complex_manifest="${test_tmp}/complex.yaml"
cat >"${complex_manifest}" <<EOF
MANIFEST_VERSION: 1
JCR_IMPORT_PATH: ${jcr_input}
REPLAY_INPUT_PATH:
  - ${replay_input}
EOF
assert_fails_with \
  "complex manifest" \
  "restricted KEY: value format" \
  env MANIFEST="${complex_manifest}" sh deploy/scripts/local-release.sh

duplicate_key_manifest="${test_tmp}/duplicate-key.yaml"
cat >"${duplicate_key_manifest}" <<EOF
MANIFEST_VERSION: 1
JCR_IMPORT_PATH: ${jcr_input}
JCR_IMPORT_PATH: ${jcr_input}
REPLAY_INPUT_PATH: ${replay_input}
EOF
assert_fails_with \
  "manifest duplicate key" \
  "restricted KEY: value format" \
  env MANIFEST="${duplicate_key_manifest}" sh deploy/scripts/local-release.sh

unknown_key_manifest="${test_tmp}/unknown-key.yaml"
cat >"${unknown_key_manifest}" <<EOF
MANIFEST_VERSION: 1
JCR_IMPORT_PATH: ${jcr_input}
REPLAY_INPUT_PATH: ${replay_input}
UNEXPECTED_INPUT_PATH: ${replay_input}
EOF
assert_fails_with \
  "manifest unknown key" \
  "restricted KEY: value format" \
  env MANIFEST="${unknown_key_manifest}" sh deploy/scripts/local-release.sh

wrong_version_manifest="${test_tmp}/wrong-version.yaml"
cat >"${wrong_version_manifest}" <<EOF
MANIFEST_VERSION: 2
JCR_IMPORT_PATH: ${jcr_input}
REPLAY_INPUT_PATH: ${replay_input}
EOF
assert_fails_with \
  "manifest wrong version" \
  "MANIFEST_VERSION must be 1" \
  env MANIFEST="${wrong_version_manifest}" sh deploy/scripts/local-release.sh

quoted_value_manifest="${test_tmp}/quoted-value.yaml"
cat >"${quoted_value_manifest}" <<EOF
MANIFEST_VERSION: 1
JCR_IMPORT_PATH: "${jcr_input}"
REPLAY_INPUT_PATH: ${replay_input}
EOF
assert_fails_with \
  "manifest quoted value" \
  "restricted KEY: value format" \
  env MANIFEST="${quoted_value_manifest}" sh deploy/scripts/local-release.sh

inline_comment_manifest="${test_tmp}/inline-comment.yaml"
cat >"${inline_comment_manifest}" <<EOF
MANIFEST_VERSION: 1
JCR_IMPORT_PATH: ${jcr_input}
REPLAY_INPUT_PATH: ${replay_input} # fixed replay
EOF
assert_fails_with \
  "manifest inline comment" \
  "restricted KEY: value format" \
  env MANIFEST="${inline_comment_manifest}" sh deploy/scripts/local-release.sh

indented_manifest="${test_tmp}/indented.yaml"
cat >"${indented_manifest}" <<EOF
MANIFEST_VERSION: 1
 JCR_IMPORT_PATH: ${jcr_input}
REPLAY_INPUT_PATH: ${replay_input}
EOF
assert_fails_with \
  "manifest indentation" \
  "restricted KEY: value format" \
  env MANIFEST="${indented_manifest}" sh deploy/scripts/local-release.sh

trailing_whitespace_manifest="${test_tmp}/trailing-whitespace.yaml"
{
  printf '%s\n' 'MANIFEST_VERSION: 1'
  printf 'JCR_IMPORT_PATH: %s \n' "${jcr_input}"
  printf 'REPLAY_INPUT_PATH: %s\n' "${replay_input}"
} >"${trailing_whitespace_manifest}"
assert_fails_with \
  "manifest trailing whitespace" \
  "restricted KEY: value format" \
  env MANIFEST="${trailing_whitespace_manifest}" sh deploy/scripts/local-release.sh

valid_manifest="${test_tmp}/valid.yaml"
cat >"${valid_manifest}" <<EOF
MANIFEST_VERSION: 1
JCR_IMPORT_PATH: ${jcr_input}
REPLAY_INPUT_PATH: ${replay_input}
EOF
assert_fails_with \
  "unimplemented release stage" \
  "pipeline stage not implemented" \
  env MANIFEST="${valid_manifest}" sh deploy/scripts/local-release.sh

positional_manifest="${test_tmp}/positional.yaml"
cp "${valid_manifest}" "${positional_manifest}"
assert_fails_with \
  "manifest environment and positional conflict" \
  "MANIFEST and the positional manifest path disagree" \
  env MANIFEST="${valid_manifest}" \
  sh deploy/scripts/local-release.sh "${positional_manifest}"

fake_bin="${test_tmp}/bin"
mkdir -p "${fake_bin}"
cat >"${fake_bin}/curl" <<'EOF'
#!/bin/sh

set -eu

headers=
body=
url=

while [ "$#" -gt 0 ]; do
  case "$1" in
    --dump-header | --output | --write-out | --header | --max-time)
      option="$1"
      shift
      [ "$#" -gt 0 ] || exit 64
      case "${option}" in
        --dump-header) headers="$1" ;;
        --output) body="$1" ;;
      esac
      shift
      ;;
    --silent | --show-error | --fail)
      shift
      ;;
    http://* | https://*)
      url="$1"
      shift
      ;;
    *)
      exit 64
      ;;
  esac
done

[ -n "${headers}" ] && [ -n "${body}" ] && [ -n "${url}" ] || exit 64
printf '%s\n' "${url}" >>"${MOCK_CURL_LOG}"

status=404
content_type=application/problem+json
payload='{"code":"route_not_found"}'
extra_headers=

case "${url}" in
  "${API_URL%/}/health")
    status=200
    content_type=application/json
    payload='{"status":"ok","service":"medpaperhub-api"}'
    ;;
  "${API_URL%/}/api/v1/home")
    status="${MOCK_CATALOG_STATUS:-503}"
    if [ "${status}" = "200" ]; then
      content_type=application/json
      payload='{"catalog_generation":"00000000-0000-4000-8000-000000000701"}'
      if [ "${MOCK_OMIT_GENERATION:-0}" != "1" ]; then
        extra_headers='X-Catalog-Generation: 00000000-0000-4000-8000-000000000701'
      fi
    else
      content_type=application/problem+json
      payload='{"code":"catalog_not_published"}'
    fi
    ;;
  "${WEB_URL%/}/health")
    status=200
    content_type=application/json
    payload='{"status":"ok","service":"medpaperhub-web"}'
    ;;
  "${WEB_URL%/}/")
    status=200
    content_type=text/html
    payload='<!doctype html><html><body>medpaperhub</body></html>'
    ;;
esac

{
  printf 'HTTP/1.1 %s Mock\r\n' "${status}"
  printf 'Content-Type: %s\r\n' "${content_type}"
  if [ -n "${extra_headers}" ]; then
    printf '%s\r\n' "${extra_headers}"
  fi
  printf '\r\n'
} >"${headers}"
printf '%s' "${payload}" >"${body}"
printf '%s' "${status}"
EOF
chmod +x "${fake_bin}/curl"

cat >"${fake_bin}/docker" <<'EOF'
#!/bin/sh

set -eu

{
  printf 'project=%s' "${COMPOSE_PROJECT_NAME:-}"
  for argument in "$@"; do
    printf '|%s' "${argument}"
  done
  printf '\n'
} >>"${MOCK_DOCKER_LOG}"
EOF
chmod +x "${fake_bin}/docker"

mock_log="${test_tmp}/curl.log"
: >"${mock_log}"
api_url='http://api.test.invalid'
web_url='http://web.test.invalid'

project_env_file="${test_tmp}/local-project.env"
printf '%s\n' 'COMPOSE_PROJECT_NAME=env-file-project' >"${project_env_file}"
docker_log="${test_tmp}/docker.log"
: >"${docker_log}"
if ! sh -c '
  unset COMPOSE_PROJECT_NAME
  export LOCAL_ENV_FILE="$1"
  export PATH="$2"
  export API_URL="$3"
  export WEB_URL="$4"
  export MOCK_CATALOG_STATUS=503
  export MOCK_CURL_LOG="$5"
  export MOCK_DOCKER_LOG="$6"
  export LOCAL_WAIT_TIMEOUT=1
  sh deploy/scripts/up-empty.sh
' sh \
  "${project_env_file}" \
  "${fake_bin}:${PATH}" \
  "${api_url}" \
  "${web_url}" \
  "${mock_log}" \
  "${docker_log}" >/dev/null; then
  fail "up-empty rejected a valid LOCAL_ENV_FILE Compose project name"
fi
grep -F 'project=env-file-project' "${docker_log}" |
  grep -F '|down|--volumes|--remove-orphans' >/dev/null ||
  fail "up-empty down --volumes targeted the wrong LOCAL_ENV_FILE project"

invalid_project_env_file="${test_tmp}/invalid-local-project.env"
printf '%s\n' 'COMPOSE_PROJECT_NAME=<replace-project-name>' \
  >"${invalid_project_env_file}"
assert_fails_with \
  "invalid LOCAL_ENV_FILE Compose project name" \
  "invalid COMPOSE_PROJECT_NAME" \
  sh -c '
    unset COMPOSE_PROJECT_NAME
    export LOCAL_ENV_FILE="$1"
    export PATH="$2"
    export API_URL="$3"
    export WEB_URL="$4"
    export MOCK_CATALOG_STATUS=503
    export MOCK_CURL_LOG="$5"
    export MOCK_DOCKER_LOG="$6"
    export LOCAL_WAIT_TIMEOUT=1
    sh deploy/scripts/up-empty.sh
  ' sh \
    "${invalid_project_env_file}" \
    "${fake_bin}:${PATH}" \
    "${api_url}" \
    "${web_url}" \
    "${mock_log}" \
    "${docker_log}"

duplicate_project_env_file="${test_tmp}/duplicate-local-project.env"
printf '%s\n' \
  'COMPOSE_PROJECT_NAME=first-project' \
  'COMPOSE_PROJECT_NAME=second-project' \
  >"${duplicate_project_env_file}"
assert_fails_with \
  "duplicate LOCAL_ENV_FILE Compose project name" \
  "COMPOSE_PROJECT_NAME must be defined at most once" \
  sh -c '
    unset COMPOSE_PROJECT_NAME
    export LOCAL_ENV_FILE="$1"
    export PATH="$2"
    export API_URL="$3"
    export WEB_URL="$4"
    export MOCK_CATALOG_STATUS=503
    export MOCK_CURL_LOG="$5"
    export MOCK_DOCKER_LOG="$6"
    export LOCAL_WAIT_TIMEOUT=1
    sh deploy/scripts/up-empty.sh
  ' sh \
    "${duplicate_project_env_file}" \
    "${fake_bin}:${PATH}" \
    "${api_url}" \
    "${web_url}" \
    "${mock_log}" \
    "${docker_log}"

: >"${docker_log}"
env \
  COMPOSE_PROJECT_NAME=explicit-project \
  LOCAL_ENV_FILE="${project_env_file}" \
  PATH="${fake_bin}:${PATH}" \
  API_URL="${api_url}" \
  WEB_URL="${web_url}" \
  MOCK_CATALOG_STATUS=503 \
  MOCK_CURL_LOG="${mock_log}" \
  MOCK_DOCKER_LOG="${docker_log}" \
  LOCAL_WAIT_TIMEOUT=1 \
  sh deploy/scripts/up-empty.sh >/dev/null ||
  fail "up-empty rejected an explicit Compose project name"
grep -F 'project=explicit-project' "${docker_log}" |
  grep -F '|down|--volumes|--remove-orphans' >/dev/null ||
  fail "explicit COMPOSE_PROJECT_NAME must override LOCAL_ENV_FILE"

: >"${docker_log}"
if ! sh -c '
  unset COMPOSE_PROJECT_NAME
  unset LOCAL_ENV_FILE
  export PATH="$1"
  export API_URL="$2"
  export WEB_URL="$3"
  export MOCK_CATALOG_STATUS=503
  export MOCK_CURL_LOG="$4"
  export MOCK_DOCKER_LOG="$5"
  export LOCAL_WAIT_TIMEOUT=1
  sh deploy/scripts/up-empty.sh
' sh \
  "${fake_bin}:${PATH}" \
  "${api_url}" \
  "${web_url}" \
  "${mock_log}" \
  "${docker_log}" >/dev/null; then
  fail "up-empty rejected its stable isolated default project"
fi
default_project_name="$(
  sh -c \
    '. "$1"; deploy_default_compose_project_name "$2"' \
    "${repo_root}/deploy/scripts/project-name-probe" \
    "${repo_root}/deploy/scripts/lib.sh" \
    "${repo_root}"
)"
grep -F "project=${default_project_name}" "${docker_log}" |
  grep -F '|down|--volumes|--remove-orphans' >/dev/null ||
  fail "up-empty down --volumes did not target the repo-isolated default project"

assert_fails_with \
  "release verification rejects Catalog 503" \
  "expected published Catalog (HTTP 200), got 503" \
  env \
    PATH="${fake_bin}:${PATH}" \
    API_URL="${api_url}" \
    WEB_URL="${web_url}" \
    MOCK_CATALOG_STATUS=503 \
    MOCK_CURL_LOG="${mock_log}" \
    sh deploy/scripts/verify-release.sh

assert_fails_with \
  "release verification rejects missing generation" \
  "missing X-Catalog-Generation" \
  env \
    PATH="${fake_bin}:${PATH}" \
    API_URL="${api_url}" \
    WEB_URL="${web_url}" \
    MOCK_CATALOG_STATUS=200 \
    MOCK_OMIT_GENERATION=1 \
    MOCK_CURL_LOG="${mock_log}" \
    sh deploy/scripts/verify-release.sh

env \
  PATH="${fake_bin}:${PATH}" \
  API_URL="${api_url}" \
  WEB_URL="${web_url}" \
  MOCK_CATALOG_STATUS=200 \
  MOCK_CURL_LOG="${mock_log}" \
  sh deploy/scripts/verify-release.sh >/dev/null ||
  fail "release verification rejected a published Catalog"

grep -F "${api_url}/api/v1/home" "${mock_log}" >/dev/null ||
  fail "release verification did not use injected API_URL"
grep -F "${web_url}/" "${mock_log}" >/dev/null ||
  fail "release verification did not use injected WEB_URL"

printf '%s\n' 'deploy-contract: PASS'
