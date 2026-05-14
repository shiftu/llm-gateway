VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS    := -s -w -X main.version=$(VERSION)
BIN        := bin/llm-gateway
INSTALL_DIR ?= /opt/homebrew/bin
INSTALL_BIN := $(INSTALL_DIR)/llm-gateway

.PHONY: build install reinstall test lint tidy clean run mcp-serve mcp-tools mcp-tools-http release snapshot

build:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/llm-gateway

install:
	CGO_ENABLED=0 go build -ldflags "$(LDFLAGS)" -o $(INSTALL_BIN) ./cmd/llm-gateway
	@echo "installed: $(INSTALL_BIN)"
	@echo "note: restart Claude Code MCP subprocess to pick up new tools (/mcp)"

reinstall: clean install

test:
	go test ./...

lint:
	go vet ./...

tidy:
	go mod tidy

clean:
	rm -rf bin/ dist/

run: build
	./$(BIN) start

mcp-serve: build
	./$(BIN) mcp-serve

mcp-tools: build
	@printf '%s\n' \
	  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"make","version":"0"}}}' \
	  '{"jsonrpc":"2.0","method":"notifications/initialized"}' \
	  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
	  | ./$(BIN) mcp-serve 2>/dev/null \
	  | grep -o '"name":"[^"]*"' | sort -u

# Probe the live HTTP MCP endpoint on a running gateway. Unlike `mcp-tools`,
# this hits the long-running process — useful for confirming a rebuild has
# been picked up after `launchctl kickstart -k`. Requires the gateway to be
# running and LLM_GATEWAY_TOKEN to be set in the env.
LGW_MCP_URL ?= http://127.0.0.1:7421/mcp
mcp-tools-http:
	@if [ -z "$$LLM_GATEWAY_TOKEN" ]; then echo "LLM_GATEWAY_TOKEN env var required"; exit 1; fi
	@curl -s -X POST $(LGW_MCP_URL) \
	  -H "Authorization: Bearer $$LLM_GATEWAY_TOKEN" \
	  -H 'Content-Type: application/json' \
	  -H 'Accept: application/json, text/event-stream' \
	  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
	  | tr ',' '\n' | grep -oE '"name":"[^"]*"' | sort -u

snapshot:
	goreleaser release --snapshot --clean

release:
	goreleaser release --clean
