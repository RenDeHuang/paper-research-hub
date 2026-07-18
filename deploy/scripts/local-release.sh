#!/bin/sh

set -eu

DEPLOY_SCRIPT_NAME=local-release
export DEPLOY_SCRIPT_NAME
. "$(dirname "$0")/lib.sh"

manifest="${MANIFEST:-}"
[ "$#" -le 1 ] ||
  deploy_fail "usage: MANIFEST=/absolute/path/to/replay.yaml local-release.sh"
if [ "$#" -eq 1 ]; then
  [ -z "${manifest}" ] || [ "${manifest}" = "$1" ] ||
    deploy_fail "MANIFEST and the positional manifest path disagree"
  manifest="$1"
fi

[ -n "${manifest}" ] ||
  deploy_fail "MANIFEST is required"
case "${manifest}" in
  /*) ;;
  *) deploy_fail "MANIFEST must be an absolute path" ;;
esac
[ -f "${manifest}" ] ||
  deploy_fail "manifest does not exist: ${manifest}"

if ! awk '
  /^[[:space:]]*$/ || /^[[:space:]]*#/ {
    next
  }
  {
    if ($0 !~ /^[A-Z][A-Z0-9_]*: [^[:space:]]/) {
      exit 1
    }
    key = $0
    sub(/:.*/, "", key)
    value = $0
    sub(/^[^:]*: /, "", value)
    first = substr(value, 1, 1)
    if (value ~ /[[:space:]]$/ || value ~ /[[:space:]]#/ || first == "\"" || first == sprintf("%c", 39) || first == "[" || first == "{" || first == "&" || first == "*" || first == "!" || first == ">" || first == "|" || seen[key]++) {
      exit 1
    }
    if (key != "MANIFEST_VERSION" && key != "JCR_IMPORT_PATH" && key != "REPLAY_INPUT_PATH") {
      exit 1
    }
  }
' "${manifest}"; then
  deploy_fail "manifest must use the restricted KEY: value format"
fi

manifest_value() {
  manifest_key="$1"
  awk -v expected="${manifest_key}" '
    {
      key = $0
      sub(/:.*/, "", key)
      if (key == expected) {
        sub(/^[^:]*: /, "", $0)
        print
        exit
      }
    }
  ' "${manifest}"
}

manifest_version="$(manifest_value MANIFEST_VERSION)"
[ -n "${manifest_version}" ] ||
  deploy_fail "MANIFEST_VERSION is required"
[ "${manifest_version}" = "1" ] ||
  deploy_fail "MANIFEST_VERSION must be 1"

for input_key in JCR_IMPORT_PATH REPLAY_INPUT_PATH; do
  input_path="$(manifest_value "${input_key}")"
  [ -n "${input_path}" ] ||
    deploy_fail "${input_key} is required"
  case "${input_path}" in
    /*) ;;
    *) deploy_fail "${input_key} must be an absolute path" ;;
  esac
  [ -f "${input_path}" ] ||
    deploy_fail "${input_key} does not exist: ${input_path}"
done

deploy_fail "pipeline stage not implemented: Registry import and fixed-input replay"
