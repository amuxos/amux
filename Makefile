.PHONY: build test check check-fast cli-doc config-doc mcp-doc feishu-doc docs-index docs docs-check proto sidecar install upgrade deploy-local-fast uninstall package lint fmt clean

BINARIES := amux amuxd amuxlet amux-isolation-helper
VERSION ?= dev
BUILD_DIR ?= bin
export GOWORK := off
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)
EXE := $(if $(filter windows,$(GOOS)),.exe,)

build:
	@mkdir -p "$(BUILD_DIR)"
	go build -trimpath -buildvcs=false -ldflags "-X main.version=$(VERSION)" -o "$(BUILD_DIR)/" $(addprefix ./cmd/,$(BINARIES))
	@if [ "$(GOOS)" = "windows" ]; then \
		go build -trimpath -buildvcs=false -ldflags "-X main.version=$(VERSION)" -o "$(BUILD_DIR)/Setup.exe" ./cmd/amux-setup; \
	fi

test:
	go test ./...

check:
	bash scripts/check.sh

check-fast:
	bash scripts/check-fast.sh $(FAST_ARGS)

cli-doc:
	go run -tags cli_doc ./cmd/amux --language en --output docs/reference/cli.md
	go run -tags cli_doc ./cmd/amux --language zh-CN --output docs/reference/cli.zh-CN.md

config-doc:
	go run ./cmd/amux-config-doc --language en --output docs/reference/configuration.md
	go run ./cmd/amux-config-doc --language zh-CN --output docs/reference/configuration.zh-CN.md

mcp-doc:
	go run -tags mcp_doc ./cmd/amux --language en --output docs/reference/mcp.md
	go run -tags mcp_doc ./cmd/amux --language zh-CN --output docs/reference/mcp.zh-CN.md

feishu-doc:
	go run ./cmd/amux-feishu-doc --language en --output docs/reference/feishu-commands.md
	go run ./cmd/amux-feishu-doc --language zh-CN --output docs/reference/feishu-commands.zh-CN.md

docs-index:
	python3 scripts/docs_catalog.py

docs: cli-doc config-doc mcp-doc feishu-doc
	$(MAKE) docs-index

docs-check:
	@set -eu; tmp="$$(mktemp -d)"; trap 'rm -rf "$$tmp"' EXIT; \
	go build -o "$$tmp/" ./cmd/amux-config-doc ./cmd/amux-feishu-doc; \
	go build -tags cli_doc -o "$$tmp/amux-cli-doc$(EXE)" ./cmd/amux; \
	go build -tags mcp_doc -o "$$tmp/amux-mcp-doc$(EXE)" ./cmd/amux; \
	"$$tmp/amux-cli-doc$(EXE)" --language en --check --output docs/reference/cli.md; \
	"$$tmp/amux-cli-doc$(EXE)" --language zh-CN --check --output docs/reference/cli.zh-CN.md; \
	"$$tmp/amux-config-doc$(EXE)" --language en --check --output docs/reference/configuration.md; \
	"$$tmp/amux-config-doc$(EXE)" --language zh-CN --check --output docs/reference/configuration.zh-CN.md; \
	"$$tmp/amux-mcp-doc$(EXE)" --language en --check --output docs/reference/mcp.md; \
	"$$tmp/amux-mcp-doc$(EXE)" --language zh-CN --check --output docs/reference/mcp.zh-CN.md; \
	"$$tmp/amux-feishu-doc$(EXE)" --language en --check --output docs/reference/feishu-commands.md; \
	"$$tmp/amux-feishu-doc$(EXE)" --language zh-CN --check --output docs/reference/feishu-commands.zh-CN.md
	python3 scripts/docs_catalog.py --check
	python3 -B -m unittest discover -s scripts -p test_docs_catalog.py

proto:
	buf generate

sidecar:
	cd sidecars/claude-sdk && npm ci --registry=https://registry.npmjs.org --ignore-scripts && npm run build && npm test

install: build
	"$(BUILD_DIR)/amux" install --source "$(BUILD_DIR)" --force

upgrade deploy-local-fast: build
	"$(BUILD_DIR)/amux" update --source "$(BUILD_DIR)" --force --restart

uninstall: build
	"$(BUILD_DIR)/amux" system uninstall --yes

package:
	bash scripts/package-release.sh --version "$(VERSION)" $(PACKAGE_ARGS)

lint:
	golangci-lint run ./...

fmt:
	gofmt -w agent-packs app cmd internal pkg
	goimports -w agent-packs app cmd internal pkg

clean:
	go clean ./...
	rm -f -- $(foreach bin,$(BINARIES),"$(BUILD_DIR)/$(bin)")

.DEFAULT_GOAL := build
