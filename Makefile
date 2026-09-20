.PHONY: lint lint-fix test build run docker-build

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
