VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS := -s -w -X main.version=$(VERSION)
BIN     := bin/llm-gateway

.PHONY: build test lint clean release snapshot

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/llm-gateway

test:
	go test ./...

lint:
	go vet ./...

clean:
	rm -rf bin/ dist/

snapshot:
	goreleaser release --snapshot --clean

release:
	goreleaser release --clean
