.PHONY: build run-linux down logs test clean tunnel help

BOT_BIN := bin/giveawaybot-bot
CRON_BIN := bin/giveawaybot-cron

help: ## Показать список команд
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

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

test: ## Тесты
	go test ./...
