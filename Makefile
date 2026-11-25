GO ?= go
BIN := bin/easyloom

# The release version, from VERSION (the shared release tool reads the same
# file). Stamped in with a leading v, as `go install ...@vX.Y.Z` reports it.
VERSION := $(shell cat VERSION)
LDFLAGS := -X main.version=v$(VERSION)

# The loom `make run` opens. Empty means what the binary does
# with no argument: find the loom from the current directory upward.
LOOM ?=

# No CGO: the binary must cross-compile cleanly and run with no runtime.
export CGO_ENABLED = 0

PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64

.PHONY: all build dist test test-go lint run clean

all: build

build:
	$(GO) build -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/easyloom

# The release binaries: one per platform, dist/easyloom-<os>-<arch>, built
# from scratch so dist/ holds nothing else. The release tool's assets command.
dist:
	rm -rf dist
	mkdir -p dist
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		echo "build dist/easyloom-$$os-$$arch"; \
		GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags '$(LDFLAGS)' \
			-o dist/easyloom-$$os-$$arch ./cmd/easyloom || exit 1; \
	done

test: test-go

test-go:
	$(GO) test ./...

lint:
	$(GO) vet ./...

run: build
	$(BIN) $(LOOM)

clean:
	rm -rf bin dist
