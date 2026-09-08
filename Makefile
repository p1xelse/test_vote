COMPOSE ?= docker compose
BACKEND := backend

.DEFAULT_GOAL := help

.PHONY: help
help: ## Показать список целей
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk -F':.*?## ' '{printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# ---------------------------------------------------------------- инфраструктура

.PHONY: up
up: ## Поднять postgres и redis
	$(COMPOSE) up -d postgres redis

.PHONY: up-all
up-all: ## Поднять всё, включая сам сервис в контейнере
	$(COMPOSE) --profile app up -d --build

.PHONY: down
down: ## Остановить всё и удалить данные
	$(COMPOSE) --profile app down -v

.PHONY: logs
logs: ## Логи сервиса в контейнере
	$(COMPOSE) --profile app logs -f service

# ---------------------------------------------------------------- приложение

.PHONY: migrate
migrate: ## Применить миграции к локальной БД
	cd $(BACKEND) && go run ./cmd/service --migrate-only

.PHONY: run
run: ## Запустить сервис локально (нужен make up)
	cd $(BACKEND) && go run ./cmd/service

.PHONY: build
build: ## Собрать бинарь в backend/bin/service
	cd $(BACKEND) && go build -o bin/service ./cmd/service

# ---------------------------------------------------------------- качество

.PHONY: generate
generate: ## Перегенерировать моки (mockgen)
	cd $(BACKEND) && go generate ./...

.PHONY: test
test: ## Юнит-тесты
	cd $(BACKEND) && go test -race ./...

.PHONY: test-integration
test-integration: ## Интеграционные тесты (нужен make up)
	cd $(BACKEND) && go test -tags=integration -count=1 ./...

.PHONY: bench
bench: ## Бенчмарки счётчиков и дедупликации
	cd $(BACKEND) && go test -bench=. -benchmem -run='^$$' ./internal/counters/... ./internal/dedup/...

.PHONY: lint
lint: ## golangci-lint
	cd $(BACKEND) && golangci-lint run

.PHONY: tidy
tidy: ## go mod tidy
	cd $(BACKEND) && go mod tidy

# ---------------------------------------------------------------- нагрузка

.PHONY: loadtest
loadtest: ## Нагрузочный тест k6, см. loadtest/README.md
	docker run --rm -i \
		-e BASE_URL=$${BASE_URL:-http://host.docker.internal:8080} \
		-e ADMIN_TOKEN=$${ADMIN_TOKEN:-local-admin-token} \
		-e PEAK_RPS=$${PEAK_RPS:-3000} \
		-e RAMP=$${RAMP:-15s} -e HOLD=$${HOLD:-45s} \
		-v $(PWD)/loadtest:/scripts \
		grafana/k6:latest run /scripts/vote.js

.PHONY: seed
seed: ## Создать демо-опрос и вывести ссылку для QR
	@./loadtest/seed.sh
