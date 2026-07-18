#!/bin/sh

set -eu

DEPLOY_SCRIPT_NAME=up-empty
export DEPLOY_SCRIPT_NAME
. "$(dirname "$0")/lib.sh"

COMPOSE_PROJECT_NAME="$(deploy_resolve_compose_project_name)"
export COMPOSE_PROJECT_NAME

wait_timeout="${LOCAL_WAIT_TIMEOUT:-240}"
case "${wait_timeout}" in
  *[!0-9]* | "") deploy_fail "LOCAL_WAIT_TIMEOUT must be a positive integer" ;;
esac
[ "${wait_timeout}" -gt 0 ] ||
  deploy_fail "LOCAL_WAIT_TIMEOUT must be a positive integer"

deploy_compose down --volumes --remove-orphans

if ! deploy_compose up \
  --build \
  --detach \
  --wait \
  --wait-timeout "${wait_timeout}" \
  postgres api web; then
  deploy_compose ps --all || true
  deploy_compose logs --no-color || true
  deploy_fail "empty deployment failed to start"
fi

sh "${deploy_repo_root}/deploy/scripts/verify-empty.sh"
