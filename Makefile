.PHONY: help db run web build test lint bench check docker clean
export GOFLAGS ?=
DATABASE_URL ?= postgres://postgres:pw@localhost:5432/payoutrescue?sslmode=disable
TEST_DATABASE_URL ?= postgres://postgres:pw@localhost:5432/payoutrescue_test?sslmode=disable

help:           ## list targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | sed 's/:.*##/ -/'

db:             ## start Postgres 16 in Docker (databases payoutrescue and payoutrescue_test)
	docker run -d --name payoutrescue-db -e POSTGRES_PASSWORD=pw -p 5432:5432 postgres:16 || docker start payoutrescue-db
	sleep 4
	docker exec payoutrescue-db psql -U postgres -c 'CREATE DATABASE payoutrescue' || true
	docker exec payoutrescue-db psql -U postgres -c 'CREATE DATABASE payoutrescue_test' || true

web:            ## build the React app into web/dist (embedded by the Go binary)
	cd web && npm ci && npm run build

build: web      ## build the single server binary
	go build -o bin/server ./cmd/server
	go build -o bin/bench ./cmd/bench
	go build -o bin/check ./cmd/check

run: build      ## run everything on http://localhost:8080
	DATABASE_URL=$(DATABASE_URL) ADMIN_TOKEN=dev-token ./bin/server

test:           ## Go tests with the race detector, then frontend typecheck and lint
	TEST_DATABASE_URL=$(TEST_DATABASE_URL) go test -race -p 1 -count=1 ./cmd/... ./internal/... ./web
	cd web && npm run typecheck && npm run lint

lint:           ## gofmt, vet, eslint, tsc
	test -z "$$(gofmt -l cmd internal web/embed.go)"
	go vet ./cmd/... ./internal/... ./web
	cd web && npm run typecheck && npm run lint

bench:          ## run the 32-scenario benchmark and write bench-results.json
	DATABASE_URL=$(DATABASE_URL) go run ./cmd/bench

check:          ## verify database, PayPal sandbox token and LLM connections
	DATABASE_URL=$(DATABASE_URL) go run ./cmd/check

docker:         ## build the production image
	docker build -t payoutrescue .

clean:
	rm -rf bin web/dist/assets web/dist/index.html bench-results.json
