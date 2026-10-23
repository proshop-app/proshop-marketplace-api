# Все команды — из корня. Инструменты не ставятся глобально: golangci-lint и
# air запускаются через go run с закреплёнными версиями, goose встроен в
# cmd/migrate и закреплён в go.mod. Новее go1.23.2 toolchain не нужен.
SHELL := /bin/bash

export GOTOOLCHAIN := local

GOLANGCI_LINT_VERSION := v2.3.1
AIR_VERSION           := v1.61.7

GOLANGCI_LINT := go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)
AIR           := go run github.com/air-verse/air@$(AIR_VERSION)

# Окружение разработки указывает на docker-compose.yml. В кластере те же
# переменные задаёт выкат; любую можно переопределить: make dev HTTP_ADDR=:9000
DATABASE_URL        ?= postgres://marketplace:marketplace@localhost:25432/marketplace?sslmode=disable
OPENSEARCH_URLS     ?= http://localhost:29200
OPENSEARCH_REPLICAS ?= 0
KAFKA_BROKERS       ?= localhost:29094
KAFKA_TOPIC_PREFIX  ?=
OPERATOR_TOKEN      ?= local-operator-token-0123456789abcdef
HTTP_ADDR           ?= :8080
export DATABASE_URL OPENSEARCH_URLS OPENSEARCH_REPLICAS KAFKA_BROKERS KAFKA_TOPIC_PREFIX OPERATOR_TOKEN HTTP_ADDR

IMAGE ?= proshop-marketplace:dev

.PHONY: dev infra-up infra-down migrate reindex test test-integration lint build image

## dev: инфраструктура в compose, миграции и сервис с перезапуском при правках
dev: infra-up migrate
	$(AIR) -c .air.toml

## infra-up: PostgreSQL, OpenSearch, Kafka (KRaft) и Redis, ждать готовности
infra-up:
	docker compose up -d --wait

## infra-down: остановить инфраструктуру, данные остаются в томах
infra-down:
	docker compose down

## migrate: применить миграции к DATABASE_URL
migrate:
	go run ./cmd/migrate up

## reindex: перестроить индекс предложений из проекции
reindex:
	go run ./cmd/marketplace reindex

## test: юнит-тесты домена, приложения, транспорта и сторож слоистости
test:
	go test -race -count=1 ./...

## test-integration: интеграционные тесты инфраструктуры на compose
test-integration: infra-up
	go test -race -count=1 -tags integration ./internal/infrastructure/...

## lint: golangci-lint с depguard по слоям
lint:
	$(GOLANGCI_LINT) run ./...

## build: бинарники сервиса и миграций в bin/
build:
	CGO_ENABLED=0 go build -trimpath -o bin/marketplace ./cmd/marketplace
	CGO_ENABLED=0 go build -trimpath -o bin/migrate ./cmd/migrate

## image: образ сервиса
image:
	docker build -t $(IMAGE) .
