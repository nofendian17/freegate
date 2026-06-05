.PHONY: help test test-v test-cover test-race build run up down restart logs ps ps-all clean tidy vet fmt check compose-build compose-pull

BINARY   := server
PKG      := ./...
COMPOSE  := docker compose

help: ## Show this help
	@awk 'BEGIN {FS = ":.*##"; printf "Usage:\n  make \033[36m<target>\033[0m\n\nTargets:\n"} /^[a-zA-Z_-]+:.*?##/ { printf "  \033[36m%-15s\033[0m %s\n", $$1, $$2 }' $(MAKEFILE_LIST)

test: ## Run all tests
	go test $(PKG)

test-v: ## Run all tests (verbose)
	go test -v $(PKG)

test-cover: ## Run tests with coverage report
	go test -coverprofile=coverage.out $(PKG) && go tool cover -html=coverage.out -o coverage.html

test-race: ## Run tests with race detector
	go test -race $(PKG)

test-one: ## Run a single test (usage: make test-one name=TestFoo)
	go test -v -run $(name) $(PKG)

build: ## Build the server binary
	CGO_ENABLED=0 go build -ldflags="-s -w" -o $(BINARY) ./cmd/server

run: ## Run the server locally
	go run ./cmd/server

tidy: ## Tidy go modules
	go mod tidy

vet: ## Run go vet
	go vet $(PKG)

fmt: ## Format Go source code
	gofmt -s -w .

check: fmt vet test ## Format, vet, and test

up: ## Start services (docker compose up -d)
	$(COMPOSE) up -d

down: ## Stop services
	$(COMPOSE) down

restart: ## Restart services
	$(COMPOSE) restart

logs: ## Tail service logs (usage: make logs svc=proxy)
	$(COMPOSE) logs -f $(svc)

ps: ## List running services
	$(COMPOSE) ps

ps-all: ## List all services including stopped
	$(COMPOSE) ps -a

compose-build: ## Build service images
	$(COMPOSE) build

compose-pull: ## Pull service images
	$(COMPOSE) pull

rebuild: ## Rebuild and restart a service (usage: make rebuild svc=proxy)
	$(COMPOSE) up -d --build $(svc)

clean: ## Stop services and remove build artifacts
	$(COMPOSE) down -v
	rm -f $(BINARY) coverage.out coverage.html
