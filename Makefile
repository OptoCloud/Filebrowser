.PHONY: all web build test vet lint dev-db clean

GO ?= go
FB_TEST_DATABASE_URL ?= postgres://filebrowser:devpass@127.0.0.1:55432/filebrowser?sslmode=disable

all: build

web:
	cd web && pnpm install --frozen-lockfile && pnpm run build

build: web
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags="-s -w" -o dist/filebrowser ./cmd/filebrowser

test:
	FB_TEST_DATABASE_URL="$(FB_TEST_DATABASE_URL)" $(GO) test -race ./...

vet:
	$(GO) vet ./...

lint: vet
	gosec -exclude-generated ./...
	govulncheck ./...
	cd web && pnpm audit --prod

# Throwaway PostgreSQL for development and tests.
dev-db:
	docker run -d --name fb-postgres -e POSTGRES_USER=filebrowser -e POSTGRES_PASSWORD=devpass \
		-e POSTGRES_DB=filebrowser -p 127.0.0.1:55432:5432 postgres:17-alpine

clean:
	rm -rf dist web/build web/.svelte-kit
