GO ?= go
BIN := bin/easyloom

# The release version, from VERSION (the shared release tool reads the same
# file). Stamped in with a leading v, as `go install ...@vX.Y.Z` reports it.
VERSION := $(shell cat VERSION)
LDFLAGS := -X main.version=v$(VERSION)

# The loom `make run` and `make web` point at. Empty means what the binary does
# with no argument: find the loom from the current directory upward.
LOOM ?=

# No CGO: the binary must cross-compile cleanly and run with no runtime.
export CGO_ENABLED = 0

PLATFORMS := darwin/amd64 darwin/arm64 linux/amd64 linux/arm64

.PHONY: all build dist test test-go test-web golden lint run web web-dev clean

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

test: test-go test-web

test-go:
	$(GO) test ./...

# The frontend's deciding half. web/unblock.js is graph arithmetic with no DOM
# in it, and it must agree with the loom about the present to be trusted about
# the future — so it is tested, against real map --json output in
# web/testdata/.
#
# node is a test convenience and never a build step: nothing in web/ is
# compiled, bundled or installed, and a clean checkout still builds with
# nothing beyond Go. So a machine without node skips these rather than failing.
# The files are named by glob: newer node no longer accepts a directory here.
test-web:
	@if command -v node >/dev/null 2>&1; then \
		node --test web/*.test.mjs; \
	else \
		echo "skipping web tests: node is not installed"; \
	fi

# The cross-check between the two implementations of the ranking. web/unblock.js
# and internal/unblock answer the same question for the two frontends, and
# testing each against itself would let them tell the user different things
# about what to work on next with nothing going red. So the JS writes its
# answers to web/testdata/rankings.golden.json, its own test fails when that
# file is stale, and go test ./internal/unblock fails when the port disagrees
# with it.
#
# Regenerating is deliberate and the diff is the point: it says which rankings
# moved. Not wired into `test` for exactly that reason — a target that silently
# rewrites the thing being asserted asserts nothing.
golden:
	node web/gen-rankings-golden.mjs

lint:
	$(GO) vet ./...

run: build
	$(BIN) $(LOOM)

# The browser frontend. The frontend is embedded in and served by the binary,
# so this is the only way to open it: web/index.html is not a page that can be
# opened from disk — its asset and API paths are absolute and there is no API
# without a server behind them.
web: build
	$(BIN) web $(LOOM)

# The same, serving web/ from disk. With no build step, iterating on the
# frontend is: edit a file, reload the page.
web-dev: build
	$(BIN) web --dev $(LOOM)

clean:
	rm -rf bin dist
