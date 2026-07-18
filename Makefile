.DEFAULT_GOAL := help

COMPOSE := docker compose
COMPOSE_FILES := -f docker-compose.yml
COMPOSE_TEST_FILES := -f docker-compose.yml -f docker-compose.test.yml
COMPOSE_LOCAL_FILES := -f docker-compose.yml -f deploy/compose/compose.local.yml
SMOKE_PROJECT := paper-research-hub-smoke

.PHONY: help install generate generate-api-types dev-web build build-core build-web typecheck lint-web \
	test test-core test-web test-race compose-config compose-build compose-up verify-local \
	compose-down compose-logs smoke local-up local-verify-empty local-release \
	local-verify-release

help:
	@printf '%s\n' \
		'install         Install locked pnpm dependencies' \
		'generate        Generate Nuxt type metadata' \
		'generate-api-types Generate TypeScript types from contracts/openapi.yaml' \
		'dev-web         Start the Nuxt development server' \
		'build           Build Go binaries and the Nuxt production bundle' \
		'typecheck       Run the Nuxt TypeScript checker' \
		'lint-web        Run ESLint for the Nuxt application' \
		'test            Run Go and Nuxt tests' \
		'test-race       Run the Go race detector suite' \
		'compose-config  Validate local and smoke Compose models' \
		'compose-build   Build the Core and Web images' \
		'compose-up      Build, start, and verify the complete local stack' \
		'verify-local    Verify the running API/Web deployment boundary' \
		'compose-down    Stop the local stack' \
		'compose-logs    Follow local stack logs' \
		'smoke           Build and verify an isolated empty-database stack' \
		'local-up        Reset, start, and verify the deploy/ empty stack' \
		'local-verify-empty Verify the deploy/ empty Catalog contract' \
		'local-release   Validate and run MANIFEST=/absolute/path release' \
		'local-verify-release Verify that a Catalog release is published'

install:
	pnpm install --frozen-lockfile

generate:
	pnpm --dir apps/web exec nuxt prepare

generate-api-types:
	pnpm --dir apps/web generate:api-types

dev-web:
	pnpm dev:web

build: build-core build-web

build-core:
	go -C services/core build ./...

build-web:
	pnpm --dir apps/web build

typecheck:
	pnpm typecheck

lint-web:
	pnpm --dir apps/web lint

test: test-core test-web

test-core:
	go -C services/core test ./...

test-web:
	pnpm test:web

test-race:
	go -C services/core test -race ./...

compose-config:
	$(COMPOSE) $(COMPOSE_FILES) config --quiet
	$(COMPOSE) $(COMPOSE_TEST_FILES) --profile smoke config --quiet
	$(COMPOSE) $(COMPOSE_LOCAL_FILES) config --quiet

compose-build: compose-config
	$(COMPOSE) $(COMPOSE_FILES) build

compose-up: compose-config
	$(COMPOSE) $(COMPOSE_FILES) up --build --detach --wait --wait-timeout 240
	@$(MAKE) --no-print-directory verify-local

verify-local:
	@sh docs/deployment/verify-local.sh

compose-down:
	$(COMPOSE) $(COMPOSE_FILES) down --remove-orphans

compose-logs:
	$(COMPOSE) $(COMPOSE_FILES) logs --follow

smoke: compose-config
	@status=0; \
	$(COMPOSE) -p $(SMOKE_PROJECT) $(COMPOSE_TEST_FILES) \
		up --build --detach --wait --wait-timeout 240 postgres api web \
		|| status=$$?; \
	if [ "$$status" -eq 0 ]; then \
		$(COMPOSE) -p $(SMOKE_PROJECT) $(COMPOSE_TEST_FILES) \
			--profile smoke run --rm --no-deps smoke \
			|| status=$$?; \
	fi; \
	if [ "$$status" -ne 0 ]; then \
		$(COMPOSE) -p $(SMOKE_PROJECT) $(COMPOSE_TEST_FILES) ps --all; \
		$(COMPOSE) -p $(SMOKE_PROJECT) $(COMPOSE_TEST_FILES) logs --no-color; \
	fi; \
	$(COMPOSE) -p $(SMOKE_PROJECT) $(COMPOSE_TEST_FILES) \
		down --volumes --remove-orphans; \
	exit "$$status"

local-up:
	@sh deploy/scripts/up-empty.sh

local-verify-empty:
	@sh deploy/scripts/verify-empty.sh

local-release:
	@MANIFEST="$(MANIFEST)" sh deploy/scripts/local-release.sh

local-verify-release:
	@sh deploy/scripts/verify-release.sh
