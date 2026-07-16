.PHONY: install dev-web build build-core build-web typecheck lint-web test-web

install:
	pnpm install

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

test-web:
	pnpm test:web
