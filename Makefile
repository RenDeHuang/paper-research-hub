ENV_FILE ?= .env

-include $(ENV_FILE)

export DATABASE_URL NEXT_PUBLIC_API_URL UVICORN_HOST UVICORN_PORT

.PHONY: install dev-api dev-web test test-api test-web build

install:
	pnpm install
	uv sync --project services/api

dev-api:
	uv run --directory services/api uvicorn paper_hub.main:app --reload --reload-dir src

dev-web:
	pnpm --dir apps/web dev

test: test-api test-web

test-api:
	uv run --directory services/api pytest -v

test-web:
	pnpm --dir apps/web test

build:
	pnpm --dir apps/web build
