.PHONY: lint lint-fix test build run docker-build generate migrate-new migrate-status migrate-down

BIN_DIR := ./bin
LINT_BIN := $(BIN_DIR)/golangci-lint
LINT_VER := v2.13.2

$(LINT_BIN):
	curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b $(BIN_DIR) $(LINT_VER)

lint: $(LINT_BIN)
	$(LINT_BIN) run ./...

lint-fix: $(LINT_BIN)
	$(LINT_BIN) run --fix ./...

test:
	go test -race -count=1 -coverprofile=coverage.out ./...

build:
	go build -o bin/server ./cmd/main

run:
	docker compose up --build

docker-build:
	docker build -f build/main.Dockerfile -t colab-backend:local .

SPEC_FILE := internal/api/openapi.yaml

generate:
	go run ./internal/api/fetchspec -o $(SPEC_FILE)
	cd internal/api && go tool oapi-codegen -config cfg.yaml openapi.yaml

-include .env

GOOSE := go run github.com/pressly/goose/v3/cmd/goose@v3.28.0

DB_URL := postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@localhost:5432/$(POSTGRES_DB)?sslmode=disable

migrate-new:
	@test -n "$(name)" || (echo 'usage: make migrate-new name=create_something' && exit 1)
	$(GOOSE) -dir migrations create $(name) sql

migrate-status:
	$(GOOSE) -dir migrations postgres "$(DB_URL)" status

migrate-down:
	$(GOOSE) -dir migrations postgres "$(DB_URL)" down
