.PHONY: lint lint-fix

BIN_DIR := ./bin
LINT_BIN := $(BIN_DIR)/golangci-lint
LINT_VER := v2.13.2

$(LINT_BIN):
	curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(BIN_DIR) $(LINT_VER)

lint: $(LINT_BIN)
	$(LINT_BIN) run ./...

lint-fix: $(LINT_BIN)
	$(LINT_BIN) run --fix ./...
