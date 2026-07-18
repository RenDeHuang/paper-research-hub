#!/bin/sh

set -eu

repo_root="$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)"
cd "${repo_root}"

fail() {
  printf 'deploy-contract: FAIL: %s\n' "$*" >&2
  exit 1
}

secret_pattern='(sk-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{20,}|(AKIA|ASIA)[A-Z0-9]{16}|AIza[A-Za-z0-9_-]{20,}|xox[baprs]-[A-Za-z0-9-]{10,}|sk_(live|test)_[A-Za-z0-9]{16,}|(Bearer[[:space:]]+)?eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})'
assignment_pattern="^[[:space:]]*(#[[:space:]]*)?(export[[:space:]]+)?[\"']?[A-Z][A-Z0-9_]*_(API_KEY|TOKEN|SECRET)[\"']?[[:space:]]*[:=]"

deploy_contract_secret_value_is_forbidden() {
  printf '%s\n' "$1" |
    grep -E "${secret_pattern}" >/dev/null 2>&1
}

deploy_contract_secret_assignment_is_allowed() {
  assignment_value="$1"
  assignment_path="$2"

  case "${assignment_value}" in
    "")
      return 0
      ;;
    replace-* | optional-* | development-only-*)
      [ -n "${assignment_value#*-}" ]
      return
      ;;
  esac

  if printf '%s\n' "${assignment_value}" |
    grep -E '^<[^<>]+>$|^\$\{[A-Za-z_][A-Za-z0-9_]*\}$|^\$\{\{[[:space:]]*(secrets|env)\.[A-Za-z_][A-Za-z0-9_]*[[:space:]]*\}\}$|^\$\{[A-Za-z_][A-Za-z0-9_]*:-((replace|optional|development-only)-[^}]+)?\}$' >/dev/null 2>&1; then
    return 0
  fi

  case "${assignment_path}" in
    */test/* | */tests/* | */e2e/* | */fixture/* | */fixtures/* | \
      *_test.* | *.test.* | *.spec.* | docker-compose.test.yml)
      [ "${#assignment_value}" -le 32 ] || return 1
      printf '%s\n' "${assignment_value}" |
        grep -E '^[A-Za-z0-9._-]+$' >/dev/null 2>&1
      return
      ;;
  esac

  return 1
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

for shell_file in deploy/scripts/*.sh deploy/tests/*.sh; do
  sh -n "${shell_file}" ||
    fail "invalid POSIX shell syntax: ${shell_file}"
done

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

if ! command -v deploy_contract_secret_value_is_forbidden >/dev/null 2>&1; then
  fail "secret scanner does not validate common real key and token shapes"
fi

openai_key_candidate='sk-''abcdefghijklmnopqrstuvwxyz012345'
github_token_candidate='ghp_''abcdefghijklmnopqrstuvwxyz0123456789'
aws_key_candidate='AKIA''ABCDEFGHIJKLMNOP'
google_key_candidate='AIza''abcdefghijklmnopqrstuvwxyz0123456789'
slack_token_candidate='xoxb-''123456789012-abcdefghijklmnopqrstuvwxyz'
jwt_candidate='Bearer eyJ''hbGciOiJIUzI1NiJ9.eyJ''zdWIiOiIxMjM0NTY3ODkwIn0.signaturevalue'

for forbidden_secret in \
  "${openai_key_candidate}" \
  "${github_token_candidate}" \
  "${aws_key_candidate}" \
  "${google_key_candidate}" \
  "${slack_token_candidate}" \
  "${jwt_candidate}"; do
  deploy_contract_secret_value_is_forbidden "${forbidden_secret}" ||
    fail "secret scanner accepted a common real key or token shape"
done

if deploy_contract_secret_value_is_forbidden '<replace-with-api-key>'; then
  fail "secret scanner rejected an explicit placeholder"
fi

for allowed_assignment in \
  "" \
  '<replace-with-api-key>' \
  'replace-with-api-key' \
  'optional-authorized-key' \
  'development-only-local-secret' \
  '${OPENAI_API_KEY}' \
  '${{ secrets.GITHUB_TOKEN }}'; do
  deploy_contract_secret_assignment_is_allowed \
    "${allowed_assignment}" \
    "config/example.env" ||
    fail "secret assignment scanner rejected an allowed placeholder form"
done
deploy_contract_secret_assignment_is_allowed \
  "short-test-secret" \
  "services/example/config_test.go" ||
  fail "secret assignment scanner rejected a short test fixture"
if deploy_contract_secret_assignment_is_allowed \
  "production-catalog-cursor-secret-value" \
  ".env.example"; then
  fail "secret assignment scanner accepted an obvious non-placeholder value"
fi

if git grep -I -n -E "${secret_pattern}" -- \
  >"${test_tmp}/secret-shape-matches"; then
  fail "Git tracked worktree contains a common real key or token shape"
else
  grep_status="$?"
  [ "${grep_status}" -eq 1 ] ||
    fail "git grep failed while scanning tracked files for secret shapes"
fi

if git grep -I -n -E "${assignment_pattern}" -- \
  >"${test_tmp}/sensitive-assignments"; then
  :
else
  grep_status="$?"
  [ "${grep_status}" -eq 1 ] ||
    fail "git grep failed while scanning tracked sensitive assignments"
fi

while IFS=: read -r assignment_path assignment_line assignment_text; do
  [ -n "${assignment_path}" ] || continue
  normalized_assignment="$(
    printf '%s\n' "${assignment_text}" |
      sed \
        -e 's/^[[:space:]]*//' \
        -e 's/^#[[:space:]]*//' \
        -e 's/^export[[:space:]][[:space:]]*//' \
        -e 's/^"//' \
        -e "s/^'//"
  )"
  assignment_key="$(
    printf '%s\n' "${normalized_assignment}" |
      sed \
        -e 's/[[:space:]]*[:=].*$//' \
        -e 's/"$//' \
        -e "s/'$//"
  )"
  assignment_value="$(
    printf '%s\n' "${normalized_assignment}" |
      sed \
        -e 's/^[^:=]*[[:space:]]*[:=][[:space:]]*//' \
        -e 's/[[:space:]]*,[[:space:]]*$//' \
        -e 's/[[:space:]]*$//'
  )"
  case "${assignment_value}" in
    \"*\")
      assignment_value="${assignment_value#\"}"
      assignment_value="${assignment_value%\"}"
      ;;
    \'*\')
      assignment_value="${assignment_value#\'}"
      assignment_value="${assignment_value%\'}"
      ;;
  esac
  deploy_contract_secret_assignment_is_allowed \
    "${assignment_value}" \
    "${assignment_path}" ||
    fail "tracked sensitive assignment must use a placeholder: ${assignment_path}:${assignment_line}:${assignment_key}"
done <"${test_tmp}/sensitive-assignments"

grep -F 'count(paper.paper_id)' deploy/sql/verify-release.sql >/dev/null ||
  fail "release SQL must count the public_catalog_papers primary-key column"

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

mock_log="${test_tmp}/curl.log"
: >"${mock_log}"
api_url='http://api.test.invalid'
web_url='http://web.test.invalid'

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
