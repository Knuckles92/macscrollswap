.PHONY: build clean fmt lint test vet install-local run-daemon

APP      := macscrollswap
BIN_DIR  := bin
CMD_DIR  := ./cmd/macscrollswap
PREFIX   ?= $(HOME)/.local

CGO_ENABLED := 1

build:
	mkdir -p $(BIN_DIR)
	CGO_ENABLED=$(CGO_ENABLED) go build -o $(BIN_DIR)/$(APP) $(CMD_DIR)

clean:
	rm -rf $(BIN_DIR)

fmt:
	gofmt -s -w .

vet:
	go vet ./...

lint: vet
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || echo "golangci-lint not installed; skipping"

test:
	go test ./...

run-daemon: build
	./$(BIN_DIR)/$(APP) daemon

install-local: build
	mkdir -p $(PREFIX)/bin
	cp $(BIN_DIR)/$(APP) $(PREFIX)/bin/$(APP)

tidy:
	go mod tidy
