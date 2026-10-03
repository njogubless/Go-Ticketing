# Every task a contributor needs, discoverable with `make help`.
#
# The alternative — a README section listing commands — goes stale silently.
# A Makefile target that no longer works fails loudly the first time someone
# runs it.

SHELL := /bin/bash
.DEFAULT_GOAL := help

# POSTGRES_PORT moves the published database port when 5432 is already taken.
POSTGRES_PORT ?= 5432
DB_URL ?= postgres://ticketing:ticketing@localhost:$(POSTGRES_PORT)/ticketing?sslmode=disable
export POSTGRES_PORT
JWT_SECRET ?= development-only-secret-not-for-production-use

export DATABASE_URL = $(DB_URL)
export JWT_SECRET

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| sort \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

# --- running -----------------------------------------------------------------

.PHONY: up
up: ## Start the whole stack (Postgres, API, web) in Docker
	docker compose up --build

.PHONY: down
down: ## Stop the stack, keeping the database volume
	docker compose down

.PHONY: reset
reset: ## Stop the stack and DELETE the database volume
	docker compose down -v

.PHONY: db
db: ## Start only Postgres, for running the API and web locally
	docker compose up -d postgres

.PHONY: api
api: ## Run the API locally (needs `make db`)
	cd backend && go run ./cmd/api

.PHONY: web
web: ## Run the web app locally (needs the API on :8080)
	cd web && npm run dev

.PHONY: seed
seed: ## Fill the database with a realistic demo desk
	cd backend && go run ./cmd/seed --tickets 180

# --- quality -----------------------------------------------------------------

.PHONY: test
test: test-backend test-web ## Run every test

.PHONY: test-backend
test-backend: ## Run the Go tests
	cd backend && go test ./... -count=1

.PHONY: test-race
test-race: ## Run the Go tests with the race detector
	cd backend && go test ./... -race -count=1

.PHONY: cover
cover: ## Go test coverage, printed per package
	cd backend && go test ./... -coverprofile=coverage.out -count=1 \
		&& go tool cover -func=coverage.out | tail -25

.PHONY: test-web
test-web: ## Typecheck the frontend
	cd web && npm run typecheck

.PHONY: lint
lint: ## Vet the Go code and typecheck the frontend
	cd backend && go vet ./...
	cd web && npm run typecheck

.PHONY: build
build: ## Build both applications
	cd backend && go build -o /dev/null ./...
	cd web && npm run build

.PHONY: check
check: lint test build ## Everything CI runs, locally

# --- utilities ---------------------------------------------------------------

.PHONY: psql
psql: ## Open a psql shell against the dev database
	docker compose exec postgres psql -U ticketing -d ticketing

.PHONY: secret
secret: ## Generate a JWT secret suitable for production
	@openssl rand -base64 48

.PHONY: tidy
tidy: ## Tidy Go modules
	cd backend && go mod tidy
