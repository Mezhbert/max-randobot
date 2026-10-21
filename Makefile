.PHONY: build run-linux down logs test test-unit test-integration test-store vet clean tunnel help

BOT_BIN := bin/giveawaybot-bot
CRON_BIN := bin/giveawaybot-cron

# Go-тулчейн: локально или через docker (если go не установлен / нужны
# интеграционные тесты с testcontainers). Образ go-test:alpine содержит
# git + docker-cli (собирается один раз: docker build -t go-test:alpine).
GO_IMAGE := go-test:alpine
DOCKER_RUN := docker run --rm -v "$(PWD):/src" -w /src \
	-v /var/run/docker.sock:/var/run/docker.sock \
	-e TESTCONTAINERS_RYUK_DISABLED=true $(GO_IMAGE)

help: ## Показать список команд
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

deploy-up: ## Поднять прод-стек на сервере (postgres+bot+caddy, авто-TLS)
	DOMAIN=$${DOMAIN:?DOMAIN=bot.example.com} docker compose -f docker-compose.prod.yml up -d --build

deploy-update: ## Обновить бота на сервере: git pull + пересборка + рестарт
	./scripts/update.sh

build: ## Собрать бинарники бота и cron
	go build -o $(BOT_BIN) ./cmd/bot
	go build -o $(CRON_BIN) ./cmd/cron
	@echo "Готово: $(BOT_BIN), $(CRON_BIN)"

run-linux: ## На Linux: поднять всё (postgres+bot+туннель+webhook) одной командой
	./scripts/run-linux.sh

run: ## Запустить бота локально (нужны .env и Postgres)
	./$(BOT_BIN)

down: ## Остановить docker-compose
	docker compose -f docker-compose.prod.yml down

logs: ## Логи бота (docker)
	docker compose -f docker-compose.prod.yml logs -f bot

up: ## Полный docker-compose стек (postgres+bot)
	docker compose -f docker-compose.prod.yml up -d --build

db: ## Только Postgres в docker
	docker compose up -d postgres

migrate: ## Применить миграции (через запуск бота с DB_MIGRATE=true)
	@echo "Миграции применяются автоматически при старте бота (DB_MIGRATE=true)"

cron-publish: ## Опубликовать наступившие розыгрыши
	./$(CRON_BIN) publish

cron-finish: ## Подвести итоги завершившихся розыгрышей
	./$(CRON_BIN) finish

cron-unsub: ## Перепроверить отписки участников
	./$(CRON_BIN) unsub-check

clean: ## Удалить бинарники
	rm -rf bin/

test: ## Все тесты (нужен Docker для интеграционных)
	go test -buildvcs=false ./...

test-unit: ## Только юнит-тесты (без Docker/сети, быстро)
	go test -buildvcs=false -short ./...

test-store: ## Интеграционные тесты store (реальный PG через testcontainers)
	$(DOCKER_RUN) sh -c 'go test -buildvcs=false ./internal/store/...'

test-integration: ## Все интеграционные тесты (testcontainers PG + mock BotAPI)
	$(DOCKER_RUN) sh -c 'go test -buildvcs=false ./internal/store/... ./internal/scheduler/... ./internal/handlers/...'

vet: ## go vet по всем пакетам
	go vet -buildvcs=false ./...
