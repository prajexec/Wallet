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
WALLET_SOURCE_TEST_DATABASE_URL ?= postgres://wallet_source_role:wallet_source_dev_password@127.0.0.1:5432/wallet_source_test?sslmode=disable
WALLET_DESTINATION_TEST_DATABASE_URL ?= postgres://wallet_destination_role:wallet_destination_dev_password@127.0.0.1:5432/wallet_destination_test?sslmode=disable
READ_TEST_DATABASE_URL ?= postgres://projector_role:projector_dev_password@127.0.0.1:5432/read_test?sslmode=disable
TRANSFER_TEST_DATABASE_URL ?= postgres://transfer_role:transfer_dev_password@127.0.0.1:5432/transfer_test?sslmode=disable
WALLET_SOURCE_MIGRATOR_DATABASE_URL ?= postgres://migrator_role:migrator_dev_password@127.0.0.1:5432/wallet_source?sslmode=disable
WALLET_DESTINATION_MIGRATOR_DATABASE_URL ?= postgres://migrator_role:migrator_dev_password@127.0.0.1:5432/wallet_destination?sslmode=disable
TRANSFER_MIGRATOR_DATABASE_URL ?= postgres://migrator_role:migrator_dev_password@127.0.0.1:5432/transfer?sslmode=disable
READ_MIGRATOR_DATABASE_URL ?= postgres://migrator_role:migrator_dev_password@127.0.0.1:5432/read?sslmode=disable
WALLET_SOURCE_TEST_MIGRATOR_DATABASE_URL ?= postgres://migrator_role:migrator_dev_password@127.0.0.1:5432/wallet_source_test?sslmode=disable
WALLET_DESTINATION_TEST_MIGRATOR_DATABASE_URL ?= postgres://migrator_role:migrator_dev_password@127.0.0.1:5432/wallet_destination_test?sslmode=disable
TRANSFER_TEST_MIGRATOR_DATABASE_URL ?= postgres://migrator_role:migrator_dev_password@127.0.0.1:5432/transfer_test?sslmode=disable
READ_TEST_MIGRATOR_DATABASE_URL ?= postgres://migrator_role:migrator_dev_password@127.0.0.1:5432/read_test?sslmode=disable

.PHONY: build test test-race vet lint sqlc sqlc-check projectorctl walletctl transferctl outboxctl reconcile dev-wipe-active-slot up down migrate migrate-test migrate-wallet-source migrate-wallet-destination migrate-wallet-source-test migrate-wallet-destination-test migrate-transfer migrate-transfer-test migrate-read migrate-read-test docs-check check-boundaries role-isolation test-integration ci

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
	@git diff --exit-code -- sqlc.yaml migrations/wallet migrations/projector services/wallet/internal/storage/generated services/wallet/internal/storage/queries services/projector/internal/storage/generated services/projector/internal/storage/queries

projectorctl:
	mkdir -p bin
	go build -o bin/projectorctl ./services/projector/cmd/projectorctl

walletctl:
	mkdir -p bin
	go build -o bin/walletctl ./services/wallet/cmd/walletctl

transferctl:
	mkdir -p bin
	go build -o bin/transferctl ./services/transfer/cmd/transferctl

outboxctl:
	mkdir -p bin
	go build -o bin/outboxctl ./services/wallet/cmd/outboxctl

reconcile: transferctl
	bin/transferctl reconcile $(ARGS)

dev-wipe-active-slot:
	DATABASE_URL="$(READ_MIGRATOR_DATABASE_URL)" go run ./services/projector/cmd/projectorctl wipe-active

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

migrate-wallet-source-test:
	$(GOOSE) -dir migrations/wallet postgres "$(WALLET_SOURCE_TEST_MIGRATOR_DATABASE_URL)" up

migrate-wallet-destination-test:
	$(GOOSE) -dir migrations/wallet postgres "$(WALLET_DESTINATION_TEST_MIGRATOR_DATABASE_URL)" up

migrate-transfer:
	$(GOOSE) -dir migrations/transfer postgres "$(TRANSFER_MIGRATOR_DATABASE_URL)" up

migrate-transfer-test:
	$(GOOSE) -dir migrations/transfer postgres "$(TRANSFER_TEST_MIGRATOR_DATABASE_URL)" up

migrate-read:
	$(GOOSE) -dir migrations/projector postgres "$(READ_MIGRATOR_DATABASE_URL)" up

migrate-read-test:
	$(GOOSE) -dir migrations/projector postgres "$(READ_TEST_MIGRATOR_DATABASE_URL)" up

migrate-test: migrate-wallet-source-test migrate-wallet-destination-test migrate-transfer-test migrate-read-test

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
	$(MAKE) migrate-test
	(cd services/wallet && WALLET_TEST_DATABASE_URL="$(WALLET_SOURCE_TEST_DATABASE_URL)" WALLET_TEST_MIGRATOR_DATABASE_URL="$(WALLET_SOURCE_TEST_MIGRATOR_DATABASE_URL)" NATS_TEST_URL="nats://127.0.0.1:4222" NATS_TEST_STREAM="WALLET_EVENTS_TEST" NATS_TEST_SUBJECT_PREFIX="events-test" go test -race -count=1 ./internal/integration)
	(cd services/transfer && TRANSFER_TEST_DATABASE_URL="$(TRANSFER_TEST_DATABASE_URL)" TRANSFER_TEST_MIGRATOR_DATABASE_URL="$(TRANSFER_TEST_MIGRATOR_DATABASE_URL)" go test -race -count=1 ./...)
	(cd services/projector && READ_TEST_DATABASE_URL="$(READ_TEST_DATABASE_URL)" READ_TEST_MIGRATOR_DATABASE_URL="$(READ_TEST_MIGRATOR_DATABASE_URL)" WALLET_TEST_DATABASE_URL="$(WALLET_SOURCE_TEST_DATABASE_URL)" WALLET_TEST_MIGRATOR_DATABASE_URL="$(WALLET_SOURCE_TEST_MIGRATOR_DATABASE_URL)" NATS_TEST_URL="nats://127.0.0.1:4222" NATS_TEST_STREAM="WALLET_EVENTS_TEST" NATS_TEST_SUBJECT_PREFIX="events-test" go test -race -count=1 ./internal/integration)

ci: docs-check build test test-race vet lint sqlc-check check-boundaries role-isolation test-integration
