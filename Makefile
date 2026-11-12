.PHONY: doctor check-fast check up health logs down lint lint-fix test build run docker-build generate contract-tools contract-lint contract-check migrate-new migrate-status migrate-down

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
	@bash scripts/test.sh

build:
	go build -o bin/server ./cmd/main

doctor:
	@bash scripts/doctor.sh

check-fast:
	bash -n scripts/*.sh
	@files="$$(git ls-files --cached --others --exclude-standard --deduplicate '*.go' | while IFS= read -r file; do if [ -f "$$file" ]; then gofmt -l "$$file"; fi; done)"; \
	test -z "$$files" || { printf '%s\n' "$$files"; exit 1; }
	go vet ./...

check: release-check check-fast
	go mod tidy -diff
	go build ./...
	$(MAKE) test
	$(MAKE) lint
	$(MAKE) contract-check

up:
	docker compose up --build --wait --wait-timeout 120

health:
	curl --fail --silent --show-error --max-time 10 http://localhost:$(or $(PORT),8080)/health

logs:
	docker compose logs --tail=100 api db redis s3

down:
	docker compose down

run:
	docker compose up --build

docker-build:
	docker build -f build/main.Dockerfile -t colab-backend:local \
		--build-arg RUNTIME_PYTHON_IMAGE=cellestial-kernel-python:local \
		--build-arg RUNTIME_AGENT_VERSION=local .

# Контракт: spec/openapi.json — единственный исходник; Redocly закреплён в spec/package-lock.json.
REDOCLY := spec/node_modules/.bin/redocly

$(REDOCLY): spec/package.json spec/package-lock.json
	npm ci --prefix spec --no-audit --no-fund
	touch $@

contract-tools: $(REDOCLY)

contract-lint: $(REDOCLY)
	$(REDOCLY) lint spec/openapi.json --config spec/redocly.yaml

generate: contract-lint
	cd internal/transport/http/api && go tool oapi-codegen -config cfg.yaml ../../../../spec/openapi.json

# Generated-код должен совпадать с контрактом в том же коммите.
contract-check: generate
	git diff --exit-code -- internal/transport/http/api

-include .env

GOOSE := go run github.com/pressly/goose/v3/cmd/goose@v3.28.0

DB_URL := postgres://$(POSTGRES_USER):$(POSTGRES_PASSWORD)@localhost:$(or $(POSTGRES_PORT),5432)/$(POSTGRES_DB)?sslmode=disable

migrate-new:
	@test -n "$(name)" || (echo 'usage: make migrate-new name=create_something' && exit 1)
	$(GOOSE) -dir migrations create $(name) sql

migrate-status:
	$(GOOSE) -dir migrations postgres "$(DB_URL)" status

migrate-down:
	$(GOOSE) -dir migrations postgres "$(DB_URL)" down

# Runtime agent является отдельным Python-проектом, Go-команды не меняются.
AGENT_PYTHON ?= python3.13
AGENT_VENV ?= $(CURDIR)/runtime-agent/.venv
AGENT_BIN := $(AGENT_VENV)/bin

.PHONY: agent-setup agent-lint agent-test agent-test-jupyter agent-build agent-images agent-integration agent-network-check

agent-setup:
	$(AGENT_PYTHON) -m venv $(AGENT_VENV)
	cd runtime-agent && $(AGENT_BIN)/python -m pip install -r requirements-dev.txt

agent-lint:
	cd runtime-agent && $(AGENT_BIN)/ruff check src tests images
	cd runtime-agent && $(AGENT_BIN)/ruff format --check src tests images

agent-test:
	cd runtime-agent && $(AGENT_BIN)/python -m pytest -q -m 'not docker and not jupyter'

agent-test-jupyter:
	cd runtime-agent && $(AGENT_BIN)/python -m pip install -r requirements-kernel.txt
	cd runtime-agent && RUN_JUPYTER_TESTS=1 $(AGENT_BIN)/python -m pytest -q -m jupyter

agent-build:
	cd runtime-agent && $(AGENT_BIN)/python -m build

agent-images:
	docker build -f runtime-agent/images/python.Dockerfile -t cellestial-kernel-python:local runtime-agent

agent-integration:
	cd runtime-agent && RUN_DOCKER_TESTS=1 $(AGENT_BIN)/python -m pytest -q -m docker

# Требует файрвол из infra и живые probe-эндпоинты; в обычном CI не запускается.
agent-network-check:
	cd runtime-agent && RUN_NETWORK_TESTS=1 $(AGENT_BIN)/python -m pytest -q -m network

.PHONY: generate-mocks
generate-mocks:
	bash scripts/generate-mocks.sh

# Выделенный настоящий Redis; явный адрес делает ошибки фатальными, тесты не пропускаются.
.PHONY: redis-test
redis-test:
	@test -n "$(REDIS_TEST_ADDR)" || (echo 'set REDIS_TEST_ADDR=host:port'; exit 1)
	go test -race -count=1 ./internal/user/adapters/redis ./internal/transport/http

.PHONY: release-check
release-check:
	python3 -m unittest discover -s scripts/release/tests -v
