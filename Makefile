.PHONY: run build fmt vet test test-integration lint tidy \
	migrate-up migrate-down migrate-new migrate-install \
	docker-up docker-down docker-build docker-logs

APP_NAME := server
BIN_DIR  := bin
MIGRATIONS_DIR := migrations

run: ## Run the service locally (loads .env).
	go run ./cmd/server

build: ## Build the service binary into bin/.
	CGO_ENABLED=0 go build -o $(BIN_DIR)/$(APP_NAME) ./cmd/server

fmt: ## Format all Go source.
	go fmt ./...

vet: ## Run go vet on all packages.
	go vet ./...

test: ## Run the unit test suite.
	go test ./... -v

test-integration: ## Run integration tests against TEST_DATABASE_URL (docker compose up -d postgres first).
	go test -tags=integration ./... -v

lint: ## Run golangci-lint, if installed.
	golangci-lint run

tidy: ## Sync go.mod/go.sum with imports.
	go mod tidy

migrate-up: ## Apply all pending migrations to DATABASE_URL. Requires golang-migrate (make migrate-install).
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" up

migrate-down: ## Roll back the last migration on DATABASE_URL.
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" down 1

migrate-new: ## Create a new migration pair: make migrate-new name=add_something
	migrate create -ext sql -dir $(MIGRATIONS_DIR) -seq $(name)

migrate-install: ## Install the golang-migrate CLI used by the targets above.
	go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@latest

docker-up: ## Start postgres + app via docker-compose.
	docker compose up --build

docker-down: ## Stop and remove docker-compose services.
	docker compose down

docker-build: ## Build the app image only.
	docker compose build app

docker-logs: ## Tail app logs.
	docker compose logs -f app
