BINARY := bin/doomer
DEV_BIN_DIR ?= $(HOME)/.local/bin
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || printf unknown)
BUILD_DATE ?= unknown
VERSION_PKG := github.com/doomerlabs/doomer/internal/version
LDFLAGS := -X $(VERSION_PKG).Version=$(VERSION) -X $(VERSION_PKG).Commit=$(COMMIT) -X $(VERSION_PKG).BuildDate=$(BUILD_DATE)

.PHONY: build dev test verify ci clean

# Builds are secret-free; release metadata is stamped explicitly.
build:
	mkdir -p $(dir $(BINARY))
	go build -trimpath -ldflags='$(LDFLAGS)' -o $(BINARY) .

# Replace the development executable without overwriting running watches.
dev: build
	mkdir -p "$(DEV_BIN_DIR)"
	@set -eu; dev_binary_tmp=$$(mktemp "$(DEV_BIN_DIR)/.doomer-dev.XXXXXX"); \
	trap 'rm -f "$$dev_binary_tmp"' EXIT HUP INT TERM; \
	cp "$(BINARY)" "$$dev_binary_tmp"; \
	chmod 755 "$$dev_binary_tmp"; \
	mv -f "$$dev_binary_tmp" "$(DEV_BIN_DIR)/doomer-dev"

test:
	go test ./...

verify:
	scripts/ci-verify.sh quality
	scripts/ci-verify.sh native

# Run the same authoritative stages used by the required CI aggregate. This is
# intentionally comprehensive and includes networked npm/vulnerability checks.
ci:
	scripts/ci-verify.sh all

clean:
	test "$(BINARY)" = "bin/doomer"
	rm -f -- $(BINARY)
