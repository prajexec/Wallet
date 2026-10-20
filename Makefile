.SHELL := /bin/bash

GO_MODULES := shared services/wallet services/transfer services/projector
COMPOSE := docker compose -f deploy/docker-compose.yml
GOOSE_VERSION ?= v3.24.3
GOOSE := go run github.com/pressly/goose/v3/cmd/goose@$(GOOSE_VERSION)
SQLC_VERSION ?= v1.31.1
SQLC := GOWORK=off GOTOOLCHAIN=local go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)
WALLET_SOURCE_DATABASE_URL ?= postgres://wallet_source_role:wallet_source_dev_password@127.0.0.1:5432/wallet_source?sslmode=disable
WALLET_DESTINATION_DATABASE_URL ?= postgres://wallet_destination_role:wallet_destination_dev_password@127.0.0.1:5432/wallet_destination?sslmode=disable
TRANSFER_DATABASE_URL ?= postgres://transfer_role:transfer_dev_password@127.0.0.1:5432/transfer?sslmode=disable
READ_DATABASE_URL ?= postgres://projector_role:projector_dev_password@127.0.0.1:5432/read?sslmode=disable
WALLET_SOURCE_MIGRATOR_DATABASE_URL ?= postgres://migrator_role:migrator_dev_password@127.0.0.1:5432/wallet_source?sslmode=disable
WALLET_DESTINATION_MIGRATOR_DATABASE_URL ?= postgres://migrator_role:migrator_dev_password@127.0.0.1:5432/wallet_destination?sslmode=disable
TRANSFER_MIGRATOR_DATABASE_URL ?= postgres://migrator_role:migrator_dev_password@127.0.0.1:5432/transfer?sslmode=disable
READ_MIGRATOR_DATABASE_URL ?= postgres://migrator_role:migrator_dev_password@127.0.0.1:5432/read?sslmode=disable

.PHONY: build test test-race vet lint sqlc sqlc-check up down migrate migrate-wallet-source migrate-wallet-destination migrate-transfer migrate-read docs-check check-boundaries role-isolation test-integration ci

build:
	@set -e; for module in $(GO_MODULES); do (cd $$module && go build ./...); done

test:
	@set -e; for module in $(GO_MODULES); do (cd $$module && go test ./...); done

test-race:
	@set -e; for module in $(GO_MODULES); do (cd $$module && go test -race ./...); done

vet:
	@set -e; for module in $(GO_MODULES); do (cd $$module && go vet ./...); done

sqlc:
	$(SQLC) generate -f ./sqlc.yaml

sqlc-check: sqlc
	@git diff --exit-code -- sqlc.yaml migrations/wallet services/wallet/internal/storage/generated services/wallet/internal/storage/queries

lint:
	@test -z "$$(gofmt -l $$(git ls-files '*.go'))" || (printf 'gofmt required for:\n%s\n' "$$(gofmt -l $$(git ls-files '*.go'))"; exit 1)
	@bash -n scripts/*.sh

up:
	$(COMPOSE) up -d --build

down:
	$(COMPOSE) down

migrate: migrate-wallet-source migrate-wallet-destination migrate-transfer migrate-read

migrate-wallet-source:
	$(GOOSE) -dir migrations/wallet postgres "$(WALLET_SOURCE_MIGRATOR_DATABASE_URL)" up

migrate-wallet-destination:
	$(GOOSE) -dir migrations/wallet postgres "$(WALLET_DESTINATION_MIGRATOR_DATABASE_URL)" up

migrate-transfer:
	$(GOOSE) -dir migrations/transfer postgres "$(TRANSFER_MIGRATOR_DATABASE_URL)" up

migrate-read:
	$(GOOSE) -dir migrations/projector postgres "$(READ_MIGRATOR_DATABASE_URL)" up

docs-check:
	./scripts/check-docs.sh

check-boundaries:
	./scripts/check-boundaries.sh
	./scripts/test-boundaries.sh

role-isolation:
	$(COMPOSE) up -d postgres
	@until $(COMPOSE) exec -T postgres pg_isready -U postgres -d postgres >/dev/null 2>&1; do sleep 1; done
	$(MAKE) migrate
	./scripts/test-role-isolation.sh

test-integration:
	$(COMPOSE) up -d postgres nats
	@until $(COMPOSE) exec -T postgres pg_isready -U postgres -d postgres >/dev/null 2>&1; do sleep 1; done
	$(MAKE) migrate
	(cd services/wallet && WALLET_TEST_DATABASE_URL="$(WALLET_SOURCE_DATABASE_URL)" NATS_TEST_URL="nats://127.0.0.1:4222" go test -race ./internal/integration)
	(cd services/projector && READ_TEST_DATABASE_URL="$(READ_DATABASE_URL)" WALLET_TEST_DATABASE_URL="$(WALLET_SOURCE_DATABASE_URL)" NATS_TEST_URL="nats://127.0.0.1:4222" go test -race ./internal/integration)

ci: docs-check build test test-race vet lint sqlc-check check-boundaries role-isolation test-integration
