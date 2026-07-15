.PHONY: install dev-api dev-web test test-api test-web build

install:
	pnpm install
	uv sync --project services/api

dev-api:
	uv run --project services/api uvicorn paper_hub.main:app --reload --app-dir services/api/src

dev-web:
	pnpm --dir apps/web dev

test: test-api test-web

test-api:
	uv run --project services/api pytest -v

test-web:
	pnpm --dir apps/web test

build:
	pnpm --dir apps/web build
